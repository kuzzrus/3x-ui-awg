package naiveproxy

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// accessLine is a Caddy access-log line shaped like the pinned build's own
// (captured live), minus the request/resp_headers fields the Caddyfile deletes.
func accessLine(user string, bytesRead, size int64) string {
	return fmt.Sprintf(`{"level":"info","ts":1759150000.1,"logger":"http.log.access.log0","msg":"handled request","bytes_read":%d,"user_id":%q,"duration":0.5,"size":%d,"status":200}`+"\n", bytesRead, user, size)
}

func feed(t *testing.T, m *meter, s string) {
	t.Helper()
	n, err := m.Write([]byte(s))
	if err != nil || n != len(s) {
		t.Fatalf("Write = (%d, %v), want (%d, nil): a bad line must never stall the child's pipe", n, err, len(s))
	}
}

func TestMeterCountsBytesPerUser(t *testing.T) {
	m := newMeter("inbound-1")
	feed(t, m, accessLine("bob@x", 5, 60)+accessLine("alice@x", 100, 2000)+accessLine("alice@x", 1, 2))

	got := m.drain()
	want := []Traffic{
		{Tag: "inbound-1", Email: "alice@x", Up: 101, Down: 2002},
		{Tag: "inbound-1", Email: "bob@x", Up: 5, Down: 60},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("drain = %+v, want %+v (up is bytes_read, down is size)", got, want)
	}
	if again := m.drain(); len(again) != 0 {
		t.Errorf("second drain = %+v, want nothing: a delta must be handed over once", again)
	}
}

func TestMeterReassemblesALineSplitAcrossWrites(t *testing.T) {
	m := newMeter("t")
	line := accessLine("alice@x", 7, 9)
	for i := 0; i < len(line); i++ {
		feed(t, m, line[i:i+1])
	}
	got := m.drain()
	if len(got) != 1 || got[0].Up != 7 || got[0].Down != 9 {
		t.Fatalf("drain = %+v, want one record of 7 up / 9 down counted exactly once", got)
	}
}

func TestMeterIgnoresLinesItCannotAttributeToAUser(t *testing.T) {
	cases := map[string]string{
		"failed login":        accessLine("invalid:eve@x", 9, 9),
		"anonymous request":   accessLine("", 9, 9),
		"nothing moved":       accessLine("alice@x", 0, 0),
		"operational log":     `{"level":"info","ts":1.1,"logger":"tls","msg":"cleaning storage unit","user_id":"alice@x","bytes_read":9,"size":9}` + "\n",
		"no logger":           `{"msg":"handled request","user_id":"alice@x","bytes_read":9,"size":9}` + "\n",
		"not json":            "caddy is starting\n",
		"truncated json":      `{"logger":"http.log.access.log0","user_id":"alice@x","bytes_read":9` + "\n",
		"blank line":          "\n",
		"wrong field types":   `{"logger":"http.log.access.log0","user_id":7,"bytes_read":"9","size":[]}` + "\n",
		"json array not line": `[1,2,3]` + "\n",
	}
	for name, line := range cases {
		t.Run(name, func(t *testing.T) {
			m := newMeter("t")
			feed(t, m, line)
			if got := m.drain(); len(got) != 0 {
				t.Errorf("drain = %+v, want nothing for %q", got, line)
			}
		})
	}
}

// A line the log filter cannot make smaller than this is not an access line;
// dropping it must not swallow the well-formed lines that follow.
func TestMeterDropsAnOversizedLineAndRecovers(t *testing.T) {
	huge := strings.Repeat("x", maxAccessLine+10)

	t.Run("newline arrives in a later write", func(t *testing.T) {
		m := newMeter("t")
		feed(t, m, huge[:maxAccessLine/2])
		feed(t, m, huge[maxAccessLine/2:])
		feed(t, m, huge)
		feed(t, m, "\n"+accessLine("alice@x", 3, 4))
		got := m.drain()
		if len(got) != 1 || got[0].Email != "alice@x" || got[0].Up != 3 || got[0].Down != 4 {
			t.Fatalf("drain = %+v, want only the line after the oversized one", got)
		}
	})

	t.Run("newline arrives in the same write", func(t *testing.T) {
		m := newMeter("t")
		feed(t, m, huge+"\n"+accessLine("alice@x", 3, 4))
		got := m.drain()
		if len(got) != 1 || got[0].Up != 3 {
			t.Fatalf("drain = %+v, want only the line after the oversized one", got)
		}
	})

	// paddedLine is a well-formed access line exactly n bytes long, newline aside.
	paddedLine := func(n int) string {
		const base = `{"logger":"http.log.access.log0","user_id":"alice@x","bytes_read":1,"size":1,"pad":""}`
		return base[:len(base)-2] + strings.Repeat("x", n-len(base)) + base[len(base)-2:]
	}

	t.Run("a valid line one byte over the cap is not parsed", func(t *testing.T) {
		m := newMeter("t")
		feed(t, m, paddedLine(maxAccessLine+1)+"\n")
		if got := m.drain(); len(got) != 0 {
			t.Errorf("drain = %+v, want the oversized line dropped even though it parses", got)
		}
	})

	t.Run("a valid line exactly at the cap still counts", func(t *testing.T) {
		for name, chunks := range map[string][]string{
			"in one write":             {paddedLine(maxAccessLine) + "\n"},
			"newline in a later write": {paddedLine(maxAccessLine), "\n"},
		} {
			m := newMeter("t")
			for _, c := range chunks {
				feed(t, m, c)
			}
			if got := m.drain(); len(got) != 1 || got[0].Up != 1 {
				t.Errorf("%s: drain = %+v, want the at-cap line counted", name, got)
			}
		}
	})

	t.Run("a huge unterminated run keeps no memory", func(t *testing.T) {
		m := newMeter("t")
		for range 4 {
			feed(t, m, huge)
		}
		if len(m.partial) != 0 {
			t.Errorf("buffered %d bytes of a line already being skipped", len(m.partial))
		}
	})
}

// A killed Caddy can die mid-line; that half line must not be glued onto the
// first line its replacement writes.
func TestMeterDiscardPartialNeverGluesLines(t *testing.T) {
	m := newMeter("t")
	line := accessLine("alice@x", 3, 4)
	feed(t, m, line[:len(line)/2])
	m.discardPartial()
	feed(t, m, line)
	got := m.drain()
	if len(got) != 1 || got[0].Up != 3 || got[0].Down != 4 {
		t.Fatalf("drain = %+v, want the second line counted once", got)
	}
}

func TestMeterFollowsARetag(t *testing.T) {
	m := newMeter("old")
	feed(t, m, accessLine("alice@x", 1, 1))
	m.setTag("new")
	got := m.drain()
	if len(got) != 1 || got[0].Tag != "new" {
		t.Fatalf("drain = %+v, want it rolled up under the inbound's current tag", got)
	}
}

func TestMeterNeverCountsANegativeDirection(t *testing.T) {
	m := newMeter("t")
	feed(t, m, `{"logger":"http.log.access.log0","user_id":"alice@x","bytes_read":-50,"size":10}`+"\n")
	got := m.drain()
	if len(got) != 1 || got[0].Up != 0 || got[0].Down != 10 {
		t.Fatalf("drain = %+v, want 0 up / 10 down: a negative counter must not subtract usage", got)
	}
}
