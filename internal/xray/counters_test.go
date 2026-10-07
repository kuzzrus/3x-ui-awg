package xray

import (
	"maps"
	"testing"

	statsService "github.com/xtls/xray-core/app/stats/command"
)

func TestParseCounters(t *testing.T) {
	stat := func(name string, value int64) *statsService.Stat {
		return &statsService.Stat{Name: name, Value: value}
	}
	tests := []struct {
		name         string
		stats        []*statsService.Stat
		wantInbounds map[string]Counter
		wantUsers    map[string]Counter
	}{
		{
			name:         "nothing yet",
			wantInbounds: map[string]Counter{},
			wantUsers:    map[string]Counter{},
		},
		{
			name: "both directions are paired per tag and per email",
			stats: []*statsService.Stat{
				stat("inbound>>>in-1>>>traffic>>>uplink", 10),
				stat("inbound>>>in-1>>>traffic>>>downlink", 20),
				stat("user>>>a@x>>>traffic>>>uplink", 3),
				stat("user>>>a@x>>>traffic>>>downlink", 4),
				stat("user>>>b@x>>>traffic>>>downlink", 5),
			},
			wantInbounds: map[string]Counter{"in-1": {Up: 10, Down: 20}},
			wantUsers:    map[string]Counter{"a@x": {Up: 3, Down: 4}, "b@x": {Down: 5}},
		},
		{
			name: "the api inbound and outbounds are not reported",
			stats: []*statsService.Stat{
				stat("inbound>>>api>>>traffic>>>uplink", 99),
				stat("outbound>>>direct>>>traffic>>>uplink", 98),
				stat("inbound>>>in-2>>>traffic>>>uplink", 1),
			},
			wantInbounds: map[string]Counter{"in-2": {Up: 1}},
			wantUsers:    map[string]Counter{},
		},
		{
			name: "names that are not counters are skipped",
			stats: []*statsService.Stat{
				stat("user>>>a@x>>>online", 1),
				stat("inbound>>>in-3>>>other", 7),
			},
			wantInbounds: map[string]Counter{},
			wantUsers:    map[string]Counter{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotInbounds, gotUsers := parseCounters(tt.stats)
			if !maps.Equal(gotInbounds, tt.wantInbounds) {
				t.Fatalf("inbounds = %v, want %v", gotInbounds, tt.wantInbounds)
			}
			if !maps.Equal(gotUsers, tt.wantUsers) {
				t.Fatalf("users = %v, want %v", gotUsers, tt.wantUsers)
			}
		})
	}
}
