package runtime

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/agentproto"
	"github.com/mhsanaei/3x-ui/v3/internal/crypto/nodetoken"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/util/netproxy"
	"github.com/mhsanaei/3x-ui/v3/internal/util/netsafe"
)

const (
	agentStatusTimeout  = 10 * time.Second
	agentStatsTimeout   = 10 * time.Second
	agentPushTimeout    = 2 * time.Minute
	agentRestartTimeout = time.Minute
	agentGeoListTimeout = 10 * time.Second
	// A geo file is tens of megabytes and the link to a node may be slow.
	agentGeoSendTimeout = 15 * time.Minute
	// The agent downloads the installer, in up to 30 s, before it answers that an update has started.
	agentUpdateTimeout = 90 * time.Second
	// Status, config and restart answers are a handful of scalars, and every heartbeat reads
	// one; the stats hold a counter for every inbound and user.
	maxAnswerBytes = 1 << 20
	maxStatsBytes  = 4 << 20
)

// AgentError is an answer from the agent that is not a success. Status 422 is the agent
// refusing the pushed config, which keeps running its previous one.
type AgentError struct {
	Status  int
	Message string
}

func (e *AgentError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("agent answered HTTP %d", e.Status)
	}
	return fmt.Sprintf("agent answered HTTP %d: %s", e.Status, e.Message)
}

// Refused reports whether the agent turned the config down, as opposed to failing.
func (e *AgentError) Refused() bool { return e.Status == http.StatusUnprocessableEntity }

// AgentClient speaks the agent's /v1 protocol to one node.
type AgentClient struct {
	node   *model.Node
	secret string
	base   string
	client *http.Client
}

// NewAgentClient builds the client for an agent node: its own TLS config sends the server
// name derived from the secret and trusts only the pinned certificate.
func NewAgentClient(n *model.Node, proxyURL string) (*AgentClient, error) {
	if n.Kind != model.NodeKindAgent {
		return nil, fmt.Errorf("node %s is not an agent", n.Name)
	}
	if n.ApiToken == "" {
		return nil, errors.New("agent has no secret configured")
	}
	secret, err := nodetoken.Decrypt(n.Id, n.ApiToken)
	if err != nil {
		return nil, fmt.Errorf("decrypt agent secret: %w", err)
	}
	addr, err := netsafe.NormalizeHost(n.Address)
	if err != nil {
		return nil, err
	}
	if n.Port <= 0 || n.Port > 65535 {
		return nil, fmt.Errorf("invalid agent port %d", n.Port)
	}
	client, err := agentHTTPClient(n, secret, proxyURL)
	if err != nil {
		return nil, err
	}
	return &AgentClient{
		node:   n,
		secret: secret,
		base:   "https://" + net.JoinHostPort(addr, strconv.Itoa(n.Port)),
		client: client,
	}, nil
}

// agentHTTPClient pools one client per agent like HTTPClientForNode does for panels,
// rebuilding it when the address, the pin or the secret change.
func agentHTTPClient(n *model.Node, secret, proxyURL string) (*http.Client, error) {
	digest := sha256.Sum256([]byte(secret))
	key := fmt.Sprintf("%d|agent|%s|%d|%s|%s|%s", n.Id, n.Address, n.Port, n.PinnedCertSha256, hex.EncodeToString(digest[:6]), proxyURL)

	nodeClientsMu.Lock()
	if entry, ok := nodeClientsCache[key]; ok {
		nodeClientsMu.Unlock()
		return entry.client, nil
	}
	nodeClientsMu.Unlock()

	client, err := buildAgentHTTPClient(n, secret, proxyURL)
	if err != nil {
		return nil, err
	}

	nodeClientsMu.Lock()
	defer nodeClientsMu.Unlock()
	if entry, ok := nodeClientsCache[key]; ok {
		client.CloseIdleConnections()
		return entry.client, nil
	}
	dropNodeClients(n.Id, key)
	nodeClientsCache[key] = nodeClientEntry{nodeID: n.Id, client: client}
	return client, nil
}

func buildAgentHTTPClient(n *model.Node, secret, proxyURL string) (*http.Client, error) {
	pin, err := DecodeCertPin(n.PinnedCertSha256)
	if err != nil {
		return nil, err
	}
	tlsCfg, err := agentproto.ClientTLSConfig(secret, pin)
	if err != nil {
		return nil, err
	}
	if proxyURL == "" {
		return &http.Client{Transport: newNodeTransport(tlsCfg)}, nil
	}
	client, err := netproxy.NewHTTPClient(proxyURL, agentPushTimeout)
	if err != nil {
		return nil, err
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		return nil, errors.New("proxy client transport does not take a TLS config")
	}
	transport.TLSClientConfig = tlsCfg
	return client, nil
}

func (c *AgentClient) Status(ctx context.Context) (*agentproto.Status, error) {
	var out agentproto.Status
	if err := c.do(ctx, http.MethodGet, agentproto.PathStatus, nil, nil, agentStatusTimeout, maxAnswerBytes, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *AgentClient) Stats(ctx context.Context) (*agentproto.Stats, error) {
	var out agentproto.Stats
	if err := c.do(ctx, http.MethodGet, agentproto.PathStats, nil, nil, agentStatsTimeout, maxStatsBytes, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// PushConfig sends the rendered config as the raw body. The agent answers with the
// revision it computed over the same bytes, which must match the one computed here.
func (c *AgentClient) PushConfig(ctx context.Context, body []byte, restartOnUserRemoval bool) (*agentproto.ConfigResponse, error) {
	query := url.Values{agentproto.QueryRestartOnUserRemoval: {strconv.FormatBool(restartOnUserRemoval)}}
	var out agentproto.ConfigResponse
	if err := c.do(ctx, http.MethodPut, agentproto.PathConfig, query, body, agentPushTimeout, maxAnswerBytes, &out); err != nil {
		return nil, err
	}
	if want := agentproto.RevisionOf(body, restartOnUserRemoval); out.Revision != want {
		return nil, fmt.Errorf("agent applied revision %s, sent %s", out.Revision, want)
	}
	return &out, nil
}

func (c *AgentClient) Restart(ctx context.Context) (*agentproto.ConfigResponse, error) {
	var out agentproto.ConfigResponse
	if err := c.do(ctx, http.MethodPost, agentproto.PathRestart, nil, nil, agentRestartTimeout, maxAnswerBytes, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Update starts the agent's self-update, which runs its installer again: on the latest release,
// or on the rolling dev channel when dev is set. The agent restarts when it is done.
func (c *AgentClient) Update(ctx context.Context, dev bool) (*agentproto.UpdateStatus, error) {
	query := url.Values{agentproto.QueryUpdateDev: {strconv.FormatBool(dev)}}
	var out agentproto.UpdateStatus
	if err := c.do(ctx, http.MethodPost, agentproto.PathUpdate, query, nil, agentUpdateTimeout, maxAnswerBytes, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateStatus reports how the agent's last update went.
func (c *AgentClient) UpdateStatus(ctx context.Context) (*agentproto.UpdateStatus, error) {
	var out agentproto.UpdateStatus
	if err := c.do(ctx, http.MethodGet, agentproto.PathUpdate, nil, nil, agentStatusTimeout, maxAnswerBytes, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Geo lists the geo files the agent holds.
func (c *AgentClient) Geo(ctx context.Context) (*agentproto.GeoFiles, error) {
	var out agentproto.GeoFiles
	if err := c.do(ctx, http.MethodGet, agentproto.PathGeo, nil, nil, agentGeoListTimeout, maxAnswerBytes, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// PutGeo sends the file at path to the agent as the geo file called name, gzipped on the way. The
// agent checks what it read against the digest taken here, so a file that changes meanwhile is refused.
func (c *AgentClient) PutGeo(ctx context.Context, name, path string) (*agentproto.GeoFile, error) {
	digest, err := fileSha256(path)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	pr, pw := io.Pipe()
	go func() {
		defer file.Close()
		zw := gzip.NewWriter(pw)
		_, err := io.Copy(zw, file)
		if err == nil {
			err = zw.Close()
		}
		pw.CloseWithError(err)
	}()
	// Closing the read end frees the goroutine above when the request ends before the body does.
	defer pr.Close()

	ctx, cancel := context.WithTimeout(netsafe.ContextWithAllowPrivate(ctx, c.node.AllowPrivateAddress), agentGeoSendTimeout)
	defer cancel()
	query := url.Values{agentproto.QueryGeoName: {name}, agentproto.QueryGeoSha256: {digest}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.base+agentproto.PathGeo+"?"+query.Encode(), pr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.secret)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Content-Encoding", "gzip")

	// The pooled client may carry a timeout sized for a config push, which a geo file outlasts.
	patient := *c.client
	patient.Timeout = 0
	resp, err := patient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("PUT %s: %w", agentproto.PathGeo, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, errBodyDiagBytes))
		return nil, agentErrorFor(resp.StatusCode, snippet)
	}
	raw, err := readCappedBody(resp.Body, maxAnswerBytes)
	if err != nil {
		return nil, fmt.Errorf("read %s response: %w", agentproto.PathGeo, err)
	}
	var stored agentproto.GeoFile
	if err := json.Unmarshal(raw, &stored); err != nil {
		return nil, fmt.Errorf("decode %s response: %w", agentproto.PathGeo, err)
	}
	return &stored, nil
}

func fileSha256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (c *AgentClient) do(ctx context.Context, method, path string, query url.Values, body []byte, timeout time.Duration, limit int64, out any) error {
	target := c.base + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	ctx, cancel := context.WithTimeout(netsafe.ContextWithAllowPrivate(ctx, c.node.AllowPrivateAddress), timeout)
	defer cancel()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.secret)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, errBodyDiagBytes))
		return agentErrorFor(resp.StatusCode, snippet)
	}
	raw, err := readCappedBody(resp.Body, limit)
	if err != nil {
		return fmt.Errorf("read %s response: %w", path, err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode %s response: %w", path, err)
	}
	return nil
}

// agentErrorFor turns a non-200 answer into an AgentError. The agent answers 404 to
// anything it will not serve, so for a well-formed request it means a wrong secret.
func agentErrorFor(status int, snippet []byte) error {
	var body agentproto.ErrorBody
	if json.Unmarshal(snippet, &body) == nil && body.Error != "" {
		return &AgentError{Status: status, Message: body.Error}
	}
	if status == http.StatusNotFound {
		return &AgentError{Status: status, Message: "the agent did not accept the secret, or this is not an agent"}
	}
	// Quoted: whatever answered is not known to be an agent, so keep its text from
	// garbling the log with control characters.
	if text := strings.TrimSpace(string(snippet)); text != "" {
		return &AgentError{Status: status, Message: strconv.Quote(text)}
	}
	return &AgentError{Status: status}
}
