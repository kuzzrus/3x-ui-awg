package hostaction

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
)

// Recover settles what an earlier panel process left in the runs folder, once at start: it logs how commands that ended
// meanwhile ended, watches those that still run and drops the rest.
func Recover() {
	base, err := baseDir()
	if err != nil {
		return
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		return
	}
	for _, entry := range entries {
		dir := filepath.Join(base, entry.Name())
		if _, mine := active.Load(dir); entry.IsDir() && !mine {
			settle(dir)
		}
	}
}

func settle(dir string) {
	r := &run{dir: dir}
	var m meta
	raw, err := os.ReadFile(r.path("meta.json"))
	if err != nil || json.Unmarshal(raw, &m) != nil || m.Name == "" {
		if info, statErr := os.Stat(dir); statErr == nil && time.Since(info.ModTime()) > time.Minute {
			_ = os.RemoveAll(dir)
		}
		return
	}
	r.spec = Spec{Name: m.Name, MaxRuntime: time.Duration(m.MaxRuntime) * time.Second}
	r.unit, r.pid, r.started = m.Unit, m.PID, time.Unix(m.Started, 0)
	l := r.launcher()
	switch {
	case r.hasStatus():
		r.report(l)
	case l.alive(r):
		active.Store(dir, struct{}{})
		go r.watch(l)
	default:
		logger.Warningf("hostaction: %s ended without leaving an exit status", m.Name)
		r.finish(l)
	}
}
