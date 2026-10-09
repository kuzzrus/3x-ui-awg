package agentproto

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// bs is a single backslash and lineSeps are U+2028 and U+2029, spelled with \x
// escapes so the source stays readable.
const (
	bs       = "\x5c"
	lineSeps = "\xe2\x80\xa8\xe2\x80\xa9"
)

func TestRevisionOf(t *testing.T) {
	base := RevisionOf([]byte(`{"a":1,"b":[1,2]}`), false)

	tests := []struct {
		name    string
		config  []byte
		restart bool
		same    bool
	}{
		{"same bytes", []byte(`{"a":1,"b":[1,2]}`), false, true},
		{"restart policy is part of the revision", []byte(`{"a":1,"b":[1,2]}`), true, false},
		{"whitespace is part of the bytes", []byte(`{"a": 1,"b":[1,2]}`), false, false},
		{"different content", []byte(`{"a":2,"b":[1,2]}`), false, false},
		{"empty body differs from a config", nil, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RevisionOf(tt.config, tt.restart); (got == base) != tt.same {
				t.Fatalf("revision equal to base = %v, want %v", got == base, tt.same)
			}
		})
	}

	if RevisionOf(nil, false) != RevisionOf([]byte{}, false) {
		t.Fatal("a nil and an empty body must share a revision")
	}
	if RevisionOf([]byte{1}, false) == RevisionOf(nil, true) {
		t.Fatal("a content byte must not be able to stand in for the restart flag")
	}
}

// The agent hashes what it reads off the connection, so the revision has to
// survive a real request for the bytes JSON encoders rewrite (& < > U+2028/9).
func TestRevisionSurvivesTheWire(t *testing.T) {
	tests := []struct {
		name   string
		config string
	}{
		{"indented", "{\n  \"inbounds\": [\n    {\"tag\": \"in-1\"}\n  ]\n}"},
		{"html sensitive bytes", `{"path":"/ws?ed=2048&x=1","host":"<h>","sep":"` + lineSeps + `"}`},
		{"escape sequences", `{"path":"/ws?ed=2048` + bs + `u0026x=1","host":"` + bs + `u003ch` + bs + `u003e","sep":"` + bs + `u2028"}`},
	}
	for _, tt := range tests {
		for _, restart := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s, restart=%v", tt.name, restart), func(t *testing.T) {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxConfigBytes))
					if err != nil {
						http.Error(w, err.Error(), http.StatusBadRequest)
						return
					}
					flag, _ := strconv.ParseBool(r.URL.Query().Get(QueryRestartOnUserRemoval))
					_ = json.NewEncoder(w).Encode(ConfigResponse{Revision: RevisionOf(body, flag), Applied: AppliedNoop})
				}))
				defer srv.Close()

				url := srv.URL + PathConfig + "?" + QueryRestartOnUserRemoval + "=" + strconv.FormatBool(restart)
				req, err := http.NewRequest(http.MethodPut, url, strings.NewReader(tt.config))
				if err != nil {
					t.Fatalf("NewRequest: %v", err)
				}
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatalf("Do: %v", err)
				}
				defer resp.Body.Close()
				var out ConfigResponse
				if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
					t.Fatalf("Decode: %v", err)
				}
				if want := RevisionOf([]byte(tt.config), restart); out.Revision != want {
					t.Fatalf("agent revision = %q, master revision = %q", out.Revision, want)
				}
			})
		}
	}
}

func TestWireFieldNames(t *testing.T) {
	tests := []struct {
		name string
		v    any
		want string
	}{
		{
			"config response",
			ConfigResponse{Revision: "r", Applied: AppliedHot, XrayState: XrayStateRunning},
			`{"revision":"r","applied":"hot","xrayState":"running"}`,
		},
		{
			"status",
			Status{
				AgentVersion: "v1", Hostname: "h", Guid: "g", ConfigRevision: "r",
				XrayVersion: "26.1.1", XrayState: XrayStateError, XrayError: "e",
				CpuPct: 1.5, MemPct: 2.5, UptimeSecs: 3, NetUp: 4, NetDown: 5,
			},
			`{"agentVersion":"v1","hostname":"h","guid":"g","configRevision":"r",` +
				`"xrayVersion":"26.1.1","xrayState":"error","xrayError":"e",` +
				`"cpuPct":1.5,"memPct":2.5,"uptimeSecs":3,"netUp":4,"netDown":5}`,
		},
		{
			"stats",
			Stats{
				ConfigRevision: "r",
				XrayStartedAt:  7,
				Inbounds:       map[string]Counter{"in-1": {Up: 1, Down: 2}},
				Users:          map[string]Counter{"a@x": {Up: 3, Down: 4}},
				Online:         []string{"a@x"},
			},
			`{"configRevision":"r","xrayStartedAt":7,"inbounds":{"in-1":{"up":1,"down":2}},` +
				`"users":{"a@x":{"up":3,"down":4}},"online":["a@x"]}`,
		},
		{"error body", ErrorBody{Error: "bad"}, `{"error":"bad"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := json.Marshal(tt.v)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			if string(got) != tt.want {
				t.Fatalf("wire form changed:\n got %s\nwant %s", got, tt.want)
			}
		})
	}
}

func TestValidGeoName(t *testing.T) {
	tests := []struct {
		name string
		ok   bool
	}{
		{"geoip.dat", true},
		{"geosite_runet.dat", true},
		{"geoip-IR.v2.dat", true},
		{"", false},
		{".dat", false},
		{"geoip", false},
		{"geoip.DAT", false},
		{"geoip.dat.bak", false},
		{"../geoip.dat", false},
		{"..dat", false},
		{"a..b.dat", false},
		{"dir/geoip.dat", false},
		{"dir" + bs + "geoip.dat", false},
		{"/etc/geoip.dat", false},
		{"geo ip.dat", false},
		{"geoip.dat\n", false},
		{strings.Repeat("a", 97) + ".dat", false},
		{strings.Repeat("a", 96) + ".dat", true},
	}
	for _, tt := range tests {
		if got := ValidGeoName(tt.name); got != tt.ok {
			t.Errorf("ValidGeoName(%q) = %v, want %v", tt.name, got, tt.ok)
		}
	}
}
