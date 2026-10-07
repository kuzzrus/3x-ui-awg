package agentproto

import (
	"encoding/json"
	"testing"
)

func TestRevisionOf(t *testing.T) {
	compact := json.RawMessage(`{"a":1,"b":[1,2]}`)
	indented := json.RawMessage("{\n  \"a\": 1,\n  \"b\": [1, 2]\n}")
	base, err := RevisionOf(compact, false)
	if err != nil {
		t.Fatalf("RevisionOf: %v", err)
	}

	tests := []struct {
		name    string
		config  json.RawMessage
		restart bool
		same    bool
	}{
		{"whitespace does not change the revision", indented, false, true},
		{"restart policy is part of the revision", compact, true, false},
		{"different content differs", json.RawMessage(`{"a":2,"b":[1,2]}`), false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := RevisionOf(tt.config, tt.restart)
			if err != nil {
				t.Fatalf("RevisionOf: %v", err)
			}
			if (got == base) != tt.same {
				t.Fatalf("revision equal to base = %v, want %v", got == base, tt.same)
			}
		})
	}

	if _, err := RevisionOf(json.RawMessage(`{"a":`), false); err == nil {
		t.Fatal("RevisionOf accepted invalid JSON")
	}
}

func TestRevisionSurvivesTheWire(t *testing.T) {
	config := json.RawMessage("{\n  \"inbounds\": [\n    {\"tag\": \"in-1\"}\n  ]\n}")
	want, err := RevisionOf(config, true)
	if err != nil {
		t.Fatalf("RevisionOf: %v", err)
	}
	body, err := json.Marshal(ConfigRequest{Revision: want, Config: config, RestartOnUserRemoval: true})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got ConfigRequest
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	gotRev, err := RevisionOf(got.Config, got.RestartOnUserRemoval)
	if err != nil {
		t.Fatalf("RevisionOf after the wire: %v", err)
	}
	if gotRev != want || got.Revision != want {
		t.Fatalf("revision after the wire = %q (field %q), want %q", gotRev, got.Revision, want)
	}
}

func TestWireFieldNames(t *testing.T) {
	tests := []struct {
		name string
		v    any
		want string
	}{
		{
			"config request",
			ConfigRequest{Revision: "r", Config: json.RawMessage(`{"a":1}`), RestartOnUserRemoval: true},
			`{"revision":"r","config":{"a":1},"restartOnUserRemoval":true}`,
		},
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
				XrayStartedAt: 7,
				Inbounds:      map[string]Counter{"in-1": {Up: 1, Down: 2}},
				Users:         map[string]Counter{"a@x": {Up: 3, Down: 4}},
				Online:        []string{"a@x"},
			},
			`{"xrayStartedAt":7,"inbounds":{"in-1":{"up":1,"down":2}},` +
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
