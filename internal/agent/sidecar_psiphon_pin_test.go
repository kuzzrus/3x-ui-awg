//go:build linux && amd64

package agent

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/psiphon"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// A binary that an earlier build downloaded is not the one this build pins, and nothing else removes it from a node.
func TestPsiphonProgramFetchesAgainABinaryThatIsNotThePinnedOne(t *testing.T) {
	t.Setenv("XUI_BIN_FOLDER", t.TempDir())
	if err := os.MkdirAll(psiphon.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(psiphon.BinPath(), []byte("a binary of an earlier build"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(psiphon.ConfigPath(), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	var asked []string
	program := &psiphonProgram{client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		asked = append(asked, r.URL.String())
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("not the pinned binary"))}, nil
	})}}

	if program.Installed() {
		t.Fatal("a binary that is not the pinned one counts as installed")
	}
	err := program.Install(context.Background())
	if err == nil || !strings.Contains(err.Error(), "checksum verification") {
		t.Fatalf("Install = %v, want the download refused by its checksum, since the stand-in is not the pinned binary", err)
	}
	if len(asked) != 1 || !strings.Contains(asked[0], "psiphon-tunnel-core-binaries") {
		t.Fatalf("asked %q, want the pinned file asked for once", asked)
	}
	if _, err := os.Stat(psiphon.BinPath()); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the binary of the earlier build is still there (%v), and would be run", err)
	}
}
