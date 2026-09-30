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

	traffics, clients := naiveTrafficRows(deltas)

	if len(clients) != 3 {
		t.Fatalf("client rows = %d, want one per email (3)", len(clients))
	}
	if clients[0].Email != "alice@x" || clients[0].Up != 100 || clients[0].Down != 2000 {
		t.Errorf("client row 0 = %+v, want alice@x 100/2000 (rows are sorted by email)", clients[0])
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
		{Tag: "routed", Email: "alice@x", Routed: true, Up: 100, Down: 2000},
		{Tag: "plain", Email: "bob@x", Up: 5, Down: 60},
	}

	traffics, clients := naiveTrafficRows(deltas)

	if len(clients) != 2 {
		t.Fatalf("client rows = %d, want both kept (the bridge can't tell users apart)", len(clients))
	}
	if len(traffics) != 1 || traffics[0].Tag != "plain" {
		t.Fatalf("inbound rows = %+v, want only the non-routed inbound", traffics)
	}
}

// AddTraffic keeps a single row per email, so a client on several inbounds must
// arrive already summed or all but one inbound's bytes are silently dropped.
func TestNaiveTrafficRowsSumsAClientServedByTwoInbounds(t *testing.T) {
	deltas := []naiveproxy.Traffic{
		{Tag: "in-a", Email: "alice@x", Up: 100, Down: 200},
		{Tag: "in-b", Email: "alice@x", Up: 1, Down: 2},
		{Tag: "in-r", Email: "alice@x", Routed: true, Up: 7, Down: 9},
		{Tag: "in-b", Email: "bob@x", Up: 3, Down: 4},
	}

	traffics, clients := naiveTrafficRows(deltas)

	if len(clients) != 2 {
		t.Fatalf("client rows = %d, want one per email (2), got %+v", len(clients), clients)
	}
	if a := clients[0]; a.Email != "alice@x" || a.Up != 108 || a.Down != 211 {
		t.Errorf("alice row = %+v, want her three inbounds summed (108/211)", a)
	}
	if b := clients[1]; b.Email != "bob@x" || b.Up != 3 || b.Down != 4 {
		t.Errorf("bob row = %+v, want 3/4", b)
	}
	if len(traffics) != 2 || traffics[0].Tag != "in-a" || traffics[0].Up != 100 || traffics[1].Tag != "in-b" || traffics[1].Up != 4 {
		t.Errorf("inbound rows = %+v, want in-a 100 and in-b 4 (alice's 1 + bob's 3), the routed inbound left out", traffics)
	}
}

// A routed process's last lines are handed over after its inbound has left the
// desired set; the label must travel with the delta, not be looked up from that set.
func TestNaiveTrafficRowsNeedsNoDesiredSetToClassifyARoutedDelta(t *testing.T) {
	traffics, clients := naiveTrafficRows([]naiveproxy.Traffic{
		{Tag: "no-longer-desired", Email: "alice@x", Routed: true, Up: 100, Down: 2000},
	})
	if len(traffics) != 0 {
		t.Fatalf("inbound rows = %+v, want none: the bridge counted those bytes", traffics)
	}
	if len(clients) != 1 || clients[0].Down != 2000 {
		t.Fatalf("client rows = %+v, want alice's 2000 down kept", clients)
	}
}

func TestNaiveTrafficRowsIsEmptyWithoutDeltas(t *testing.T) {
	traffics, clients := naiveTrafficRows(nil)
	if len(traffics) != 0 || len(clients) != 0 {
		t.Errorf("got %d inbound and %d client rows from no traffic, want none", len(traffics), len(clients))
	}
}
