package runtime

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/agentproto"
)

func TestAgentClientListsTheGeoFiles(t *testing.T) {
	agent := startStubAgent(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		writeJSONReply(w, http.StatusOK, agentproto.GeoFiles{Files: []agentproto.GeoFile{{Name: "geoip.dat", Size: 12}}})
	})
	client, err := NewAgentClient(agent.node, "")
	if err != nil {
		t.Fatal(err)
	}

	got, err := client.Geo(context.Background())
	if err != nil || len(got.Files) != 1 || got.Files[0] != (agentproto.GeoFile{Name: "geoip.dat", Size: 12}) {
		t.Fatalf("Geo = %+v, %v", got, err)
	}
	calls := agent.calls()
	if len(calls) != 1 || calls[0].method != http.MethodGet || calls[0].path != agentproto.PathGeo || calls[0].authorization != "Bearer "+agent.secret {
		t.Fatalf("agent saw %+v, want one authorised GET of the geo list", calls)
	}
}

func TestAgentClientSendsAGeoFileGzippedWithItsDigest(t *testing.T) {
	content := bytes.Repeat([]byte("geo data of a database "), 5000)
	path := filepath.Join(t.TempDir(), "geoip_runet.dat")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	agent := startStubAgent(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		writeJSONReply(w, http.StatusOK, agentproto.GeoFile{Name: r.URL.Query().Get(agentproto.QueryGeoName), Size: int64(len(content))})
	})
	client, err := NewAgentClient(agent.node, "")
	if err != nil {
		t.Fatal(err)
	}

	stored, err := client.PutGeo(context.Background(), "geoip_runet.dat", path)
	if err != nil || stored.Name != "geoip_runet.dat" || stored.Size != int64(len(content)) {
		t.Fatalf("PutGeo = %+v, %v", stored, err)
	}

	calls := agent.calls()
	if len(calls) != 1 {
		t.Fatalf("agent saw %d requests, want 1: %+v", len(calls), calls)
	}
	call := calls[0]
	sum := sha256.Sum256(content)
	wantQuery := agentproto.QueryGeoName + "=geoip_runet.dat&" + agentproto.QueryGeoSha256 + "=" + hex.EncodeToString(sum[:])
	if call.method != http.MethodPut || call.path != agentproto.PathGeo || call.query != wantQuery || call.authorization != "Bearer "+agent.secret {
		t.Fatalf("geo file reached the agent as %+v, want a PUT naming the file and its digest", call)
	}
	zr, err := gzip.NewReader(bytes.NewReader(call.body))
	if err != nil {
		t.Fatalf("the body is not gzip: %v", err)
	}
	unpacked, err := io.ReadAll(zr)
	if err != nil || !bytes.Equal(unpacked, content) {
		t.Fatalf("the body unpacks to %d bytes (%v), want the file's %d", len(unpacked), err, len(content))
	}
	if len(call.body) >= len(content)/2 {
		t.Fatalf("%d bytes on the wire for a compressible file of %d: it was not compressed", len(call.body), len(content))
	}
}

func TestAgentClientReportsWhyTheAgentTurnedAGeoFileDown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "geoip.dat")
	if err := os.WriteFile(path, []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}
	agent := startStubAgent(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		writeJSONReply(w, http.StatusBadRequest, agentproto.ErrorBody{Error: "the body does not match its sha256"})
	})
	client, err := NewAgentClient(agent.node, "")
	if err != nil {
		t.Fatal(err)
	}

	_, err = client.PutGeo(context.Background(), "geoip.dat", path)
	var got *AgentError
	if !errors.As(err, &got) || got.Status != http.StatusBadRequest || !strings.Contains(got.Message, "sha256") {
		t.Fatalf("error = %v, want the agent's own reason as an *AgentError", err)
	}
}

func TestAgentClientSendsNothingForAFileItCannotRead(t *testing.T) {
	agent := startStubAgent(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		writeJSONReply(w, http.StatusOK, agentproto.GeoFile{})
	})
	client, err := NewAgentClient(agent.node, "")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := client.PutGeo(context.Background(), "geoip.dat", filepath.Join(t.TempDir(), "missing.dat")); err == nil {
		t.Fatal("a file that does not exist was reported as sent")
	}
	if calls := agent.calls(); len(calls) != 0 {
		t.Fatalf("agent saw %+v, want no request", calls)
	}
}
