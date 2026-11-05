package job

import (
	"errors"
	"testing"
	"time"
)

type recordingScanner struct {
	asked   []time.Time
	started bool
	err     error
}

func (r *recordingScanner) ScanIfDue(now time.Time) (bool, error) {
	r.asked = append(r.asked, now)
	return r.started, r.err
}

func TestPsiphonScanJobAsksTheServiceWhetherAScanIsDueAtTheTimeItRuns(t *testing.T) {
	at := time.Date(2026, 11, 1, 3, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		svc  *recordingScanner
	}{
		{"nothing is due", &recordingScanner{}},
		{"a scan starts", &recordingScanner{started: true}},
		{"a scan cannot start", &recordingScanner{err: errors.New("Psiphon is not installed")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j := &PsiphonScanJob{psiphon: tc.svc, now: func() time.Time { return at }}

			j.Run()

			if len(tc.svc.asked) != 1 || !tc.svc.asked[0].Equal(at) {
				t.Fatalf("asked at %v, want once, at %v", tc.svc.asked, at)
			}
		})
	}
}
