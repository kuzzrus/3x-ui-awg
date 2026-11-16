package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/agentproto"
)

func TestAgentClientSendsADecoySiteAsItIsWithItsDigest(t *testing.T) {
	// The client does not look inside a zip, and a zip is compressed already, so it goes as it is.
	content := bytes.Repeat([]byte("PK a zip of a site "), 5000)
	path := filepath.Join(t.TempDir(), "site.zip")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	digest := hex.EncodeToString(sum[:])
	agent := startStubAgent(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		writeJSONReply(w, http.StatusOK, agentproto.DecoySite{Sha256: r.URL.Query().Get(agentproto.QueryDecoySiteSha256), Size: int64(len(content))})
	})
	client, err := NewAgentClient(agent.node, "")
	if err != nil {
		t.Fatal(err)
	}

	stored, err := client.PutDecoySite(context.Background(), digest, path)
	if err != nil || stored.Sha256 != digest || stored.Size != int64(len(content)) {
		t.Fatalf("PutDecoySite = %+v, %v", stored, err)
	}

	calls := agent.calls()
	if len(calls) != 1 {
		t.Fatalf("agent saw %d requests, want 1: %+v", len(calls), calls)
	}
	call := calls[0]
	wantQuery := agentproto.QueryDecoySiteSha256 + "=" + digest
	if call.method != http.MethodPut || call.path != agentproto.PathDecoySite || call.query != wantQuery || call.authorization != "Bearer "+agent.secret {
		t.Fatalf("site reached the agent as %+v, want a PUT naming its digest", call)
	}
	if !bytes.Equal(call.body, content) {
		t.Fatalf("the body is %d bytes, want the zip as it is, %d bytes", len(call.body), len(content))
	}
}

func TestAgentClientReportsWhyTheAgentTurnedADecoySiteDown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "site.zip")
	if err := os.WriteFile(path, []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}
	for status, message := range map[int]string{
		http.StatusBadRequest:            "the body does not match its sha256",
		http.StatusRequestEntityTooLarge: "a decoy site is at most 67108864 bytes",
	} {
		agent := startStubAgent(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
			writeJSONReply(w, status, agentproto.ErrorBody{Error: message})
		})
		client, err := NewAgentClient(agent.node, "")
		if err != nil {
			t.Fatal(err)
		}

		_, err = client.PutDecoySite(context.Background(), strings.Repeat("a", 64), path)
		var got *AgentError
		if !errors.As(err, &got) || got.Status != status || got.Message != message {
			t.Fatalf("error = %v, want the agent's own %d and reason as an *AgentError", err, status)
		}
	}
}

// An agent that predates the endpoint answers every path it does not know with a 404, which is how the panel knows it is too old.
func TestAgentClientSeesAnOldAgentAnswerADecoySiteWithA404(t *testing.T) {
	path := filepath.Join(t.TempDir(), "site.zip")
	if err := os.WriteFile(path, []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}
	agent := startStubAgent(t, func(w http.ResponseWriter, r *http.Request, _ []byte) { http.NotFound(w, r) })
	client, err := NewAgentClient(agent.node, "")
	if err != nil {
		t.Fatal(err)
	}

	_, err = client.PutDecoySite(context.Background(), strings.Repeat("a", 64), path)
	var got *AgentError
	if !errors.As(err, &got) || got.Status != http.StatusNotFound {
		t.Fatalf("error = %v, want an *AgentError with status 404", err)
	}
}

func TestAgentClientSendsNothingForASiteItCannotRead(t *testing.T) {
	agent := startStubAgent(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		writeJSONReply(w, http.StatusOK, agentproto.DecoySite{})
	})
	client, err := NewAgentClient(agent.node, "")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := client.PutDecoySite(context.Background(), strings.Repeat("a", 64), filepath.Join(t.TempDir(), "missing.zip")); err == nil {
		t.Fatal("a file that does not exist was reported as sent")
	}
	if calls := agent.calls(); len(calls) != 0 {
		t.Fatalf("agent saw %+v, want no request", calls)
	}
}
