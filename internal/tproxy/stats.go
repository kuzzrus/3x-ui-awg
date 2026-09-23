package tproxy

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// statsScrapeTimeout bounds one engine's /stats fetch. Loopback-only and
// tiny (a few KB, per mtfront_prepare_stats), so this is generous, not tight.
const statsScrapeTimeout = 3 * time.Second

// activeConnectionsKey is the /stats text key that reports the engine's
// current (not cumulative) open-connection count. Verified against the
// pinned MTProxy source this fork builds (mtproto-proxy.c,
// mtfront_prepare_stats): despite the "total_" name, the value printed
// under this key is S(conn.active_connections) -- a live snapshot taken by
// fetch_connections_stat() right before the buffer is built, not a
// monotonic counter. There is no key that ever exposes cumulative traffic
// bytes, and no key breaks any of this down per secret/client -- the whole
// buffer is one process-wide aggregate. See tproxy-status.md gap 2.
const activeConnectionsKey = "total_connections"

// scrapeActiveConnections fetches one engine's /stats and returns its
// current open-connection count. Best-effort, matching internal/mtproto's
// own scrapeStats contract: an unreachable engine, a non-200, or an
// unparseable body yields ok=false, never an error the caller must handle.
//
// The server enforces loopback-only itself (hts_stats_execute rejects any
// remote_ip other than 127.0.0.1 with a 404, unconditionally) -- this is
// not this function's own safety measure, just why it can only ever work
// against 127.0.0.1 regardless of what host string is passed.
func scrapeActiveConnections(port int) (n int, ok bool) {
	client := http.Client{Timeout: statsScrapeTimeout}
	url := fmt.Sprintf("http://127.0.0.1:%d/stats", port)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		return 0, false
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, false
	}

	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		key, value, found := strings.Cut(sc.Text(), "\t")
		if !found || key != activeConnectionsKey {
			continue
		}
		v, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return 0, false
		}
		return v, true
	}
	return 0, false
}
