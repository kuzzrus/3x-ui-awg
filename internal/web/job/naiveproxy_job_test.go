package job

import (
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/naiveproxy"
)

func TestNaiveTrafficRowsRollsUpPlainInboundsPerTag(t *testing.T) {
	deltas := []naiveproxy.Traffic{
		{Tag: "in-b", Email: "bob@x", Up: 5, Down: 60},
		{Tag: "in-a", Email: "alice@x", Up: 100, Down: 2000},
		{Tag: "in-a", Email: "carol@x", Up: 1, Down: 2},
	}

	traffics, clients := naiveTrafficRows(deltas, nil)

	if len(clients) != 3 {
		t.Fatalf("client rows = %d, want one per delta (3)", len(clients))
	}
	if clients[1].Email != "alice@x" || clients[1].Up != 100 || clients[1].Down != 2000 {
		t.Errorf("client row 1 = %+v, want alice@x 100/2000", clients[1])
	}
	if len(traffics) != 2 {
		t.Fatalf("inbound rows = %d, want one per tag (2)", len(traffics))
	}
	a, b := traffics[0], traffics[1]
	if !a.IsInbound || a.Tag != "in-a" || a.Up != 101 || a.Down != 2002 {
		t.Errorf("in-a row = %+v, want its two clients summed (101/2002)", a)
	}
	if !b.IsInbound || b.Tag != "in-b" || b.Up != 5 || b.Down != 60 {
		t.Errorf("in-b row = %+v, want 5/60", b)
	}
}

// The Xray bridge already counts a routed inbound's bytes under its tag, so
// rolling them up here would double them -- but the clients still need theirs.
func TestNaiveTrafficRowsLeavesARoutedInboundsTotalToItsBridge(t *testing.T) {
	deltas := []naiveproxy.Traffic{
		{Tag: "routed", Email: "alice@x", Up: 100, Down: 2000},
		{Tag: "plain", Email: "bob@x", Up: 5, Down: 60},
	}

	traffics, clients := naiveTrafficRows(deltas, map[string]bool{"routed": true})

	if len(clients) != 2 {
		t.Fatalf("client rows = %d, want both kept (the bridge can't tell users apart)", len(clients))
	}
	if len(traffics) != 1 || traffics[0].Tag != "plain" {
		t.Fatalf("inbound rows = %+v, want only the non-routed inbound", traffics)
	}
}

func TestNaiveTrafficRowsIsEmptyWithoutDeltas(t *testing.T) {
	traffics, clients := naiveTrafficRows(nil, map[string]bool{"x": true})
	if len(traffics) != 0 || len(clients) != 0 {
		t.Errorf("got %d inbound and %d client rows from no traffic, want none", len(traffics), len(clients))
	}
}
