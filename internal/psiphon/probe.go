package psiphon

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ProbeResult is what one probe of a tunnel found: how long a request through it takes, and where it leaves.
// Error says what did not work; the latency is still there when only the lookup of the exit failed.
type ProbeResult struct {
	LatencyMs int64  `json:"latencyMs"`
	IP        string `json:"ip,omitempty"`
	Country   string `json:"country,omitempty"`
	Error     string `json:"error,omitempty"`
}

// probeURLs answer 204 with no body, the usual way to time a round trip through a proxy. The second is asked
// only when the first does not answer, so one blocked address does not read as a dead tunnel.
var probeURLs = []string{
	"http://www.gstatic.com/generate_204",
	"http://cp.cloudflare.com/generate_204",
}

const (
	// probeBudget is how long a tunnel that is still coming up is given to answer the first request: right
	// after a start the SOCKS port is open for seconds before a tunnel is there to carry anything.
	probeBudget   = 10 * time.Second
	probeInterval = 2 * time.Second
	probeTimeout  = 15 * time.Second
)

// Probe measures the country tunnel whose SOCKS proxy is on port: the best of two timed requests through it, and
// the address and country the exit shows. A tunnel that is not up yet gets probeBudget to come up.
func Probe(ctx context.Context, port int) ProbeResult {
	client, err := socksHTTPClientOn(port, probeTimeout)
	if err != nil {
		return ProbeResult{Error: err.Error()}
	}
	latency, err := retry(ctx, probeBudget, probeInterval, func() (time.Duration, error) {
		return bestLatency(ctx, client)
	})
	if err != nil {
		return ProbeResult{Error: fmt.Sprintf("no answer through the tunnel: %v", err)}
	}
	result := ProbeResult{LatencyMs: latency.Milliseconds()}
	exit, err := fetchExit(ctx, client)
	if err != nil {
		result.Error = fmt.Sprintf("the exit could not be looked up: %v", err)
		return result
	}
	result.IP, result.Country = exit.IP, exit.Country
	return result
}

// bestLatency is the shorter of two requests, each on a connection of its own: the first pays for a name the
// tunnel has not looked up yet, and what is wanted is how far the exit is, not how cold it was.
func bestLatency(ctx context.Context, client *http.Client) (time.Duration, error) {
	first, err := timedRequest(ctx, client)
	if err != nil {
		return 0, err
	}
	if second, err := timedRequest(ctx, client); err == nil && second < first {
		return second, nil
	}
	return first, nil
}

// timedRequest asks the probe addresses in turn and returns how long the first that answered took.
func timedRequest(ctx context.Context, client *http.Client) (time.Duration, error) {
	var lastErr error
	for _, url := range probeURLs {
		elapsed, err := timeOne(ctx, client, url)
		if err == nil {
			return elapsed, nil
		}
		lastErr = err
	}
	return 0, lastErr
}

func timeOne(ctx context.Context, client *http.Client, url string) (time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	req.Close = true
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	elapsed := time.Since(start)
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	_ = resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return 0, fmt.Errorf("%s answered HTTP %d", url, resp.StatusCode)
	}
	return elapsed, nil
}
