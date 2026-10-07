package xray

import (
	"context"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/util/common"

	statsService "github.com/xtls/xray-core/app/stats/command"
)

// Counter is the cumulative up and down bytes of one inbound tag or client email.
type Counter struct {
	Up   int64
	Down int64
}

// GetCounters returns the core's cumulative counters per inbound tag and per client
// email since it started; unlike GetTraffic it keeps no baseline between calls.
func (x *XrayAPI) GetCounters() (inbounds, users map[string]Counter, err error) {
	if x.grpcClient == nil {
		return nil, nil, common.NewError("xray api is not initialized")
	}
	if x.StatsServiceClient == nil {
		return nil, nil, common.NewError("xray StatsServiceClient is not initialized")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := (*x.StatsServiceClient).QueryStats(ctx, &statsService.QueryStatsRequest{Reset_: false})
	if err != nil {
		return nil, nil, err
	}
	inbounds, users = parseCounters(resp.GetStat())
	return inbounds, users, nil
}

func parseCounters(stats []*statsService.Stat) (inbounds, users map[string]Counter) {
	inbounds = map[string]Counter{}
	users = map[string]Counter{}
	for _, stat := range stats {
		if m := trafficRegex.FindStringSubmatch(stat.GetName()); len(m) == 4 {
			if m[1] == "inbound" && m[2] != "api" {
				inbounds[m[2]] = withDirection(inbounds[m[2]], m[3], stat.GetValue())
			}
		} else if m := clientTrafficRegex.FindStringSubmatch(stat.GetName()); len(m) == 3 {
			users[m[1]] = withDirection(users[m[1]], m[2], stat.GetValue())
		}
	}
	return inbounds, users
}

func withDirection(c Counter, direction string, value int64) Counter {
	if direction == "downlink" {
		c.Down = value
	} else {
		c.Up = value
	}
	return c
}
