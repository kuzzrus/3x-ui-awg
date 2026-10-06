package job

import (
	"slices"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/mtproto"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestMtprotoTrafficRows(t *testing.T) {
	tests := []struct {
		name         string
		deltas       []mtproto.Traffic
		routedTags   map[string]bool
		wantClients  []xray.ClientTraffic
		wantInbounds []xray.Traffic
	}{
		// AddTraffic keeps a single row per email, so a shared client must arrive already summed.
		{
			name: "client on two inbounds is summed into one row",
			deltas: []mtproto.Traffic{
				{Tag: "in-a", Email: "alice@x", Up: 100, Down: 200},
				{Tag: "in-b", Email: "alice@x", Up: 1, Down: 2},
				{Tag: "in-b", Email: "bob@x", Up: 3, Down: 4},
			},
			wantClients: []xray.ClientTraffic{
				{Email: "alice@x", Up: 101, Down: 202},
				{Email: "bob@x", Up: 3, Down: 4},
			},
			wantInbounds: []xray.Traffic{
				{IsInbound: true, Tag: "in-a", Up: 100, Down: 200},
				{IsInbound: true, Tag: "in-b", Up: 4, Down: 6},
			},
		},
		// The Xray bridge counts a routed inbound's total but cannot tell its users apart.
		{
			name: "routed inbound counts for its clients but not for its total",
			deltas: []mtproto.Traffic{
				{Tag: "in-a", Email: "alice@x", Up: 100, Down: 200},
				{Tag: "in-r", Email: "alice@x", Up: 7, Down: 9},
				{Tag: "in-r", Email: "bob@x", Up: 3, Down: 4},
			},
			routedTags: map[string]bool{"in-r": true},
			wantClients: []xray.ClientTraffic{
				{Email: "alice@x", Up: 107, Down: 209},
				{Email: "bob@x", Up: 3, Down: 4},
			},
			wantInbounds: []xray.Traffic{
				{IsInbound: true, Tag: "in-a", Up: 100, Down: 200},
			},
		},
		{
			name: "no deltas",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			inbounds, clients := mtprotoTrafficRows(tc.deltas, tc.routedTags)

			if got := derefAll(clients); !slices.Equal(got, tc.wantClients) {
				t.Errorf("client rows = %+v, want %+v", got, tc.wantClients)
			}
			// The inbound roll-up ranges a map, so its order is unspecified.
			slices.SortFunc(inbounds, func(a, b *xray.Traffic) int { return strings.Compare(a.Tag, b.Tag) })
			if got := derefAll(inbounds); !slices.Equal(got, tc.wantInbounds) {
				t.Errorf("inbound rows = %+v, want %+v", got, tc.wantInbounds)
			}
		})
	}
}

func derefAll[T any](ptrs []*T) []T {
	out := make([]T, 0, len(ptrs))
	for _, p := range ptrs {
		out = append(out, *p)
	}
	return out
}
