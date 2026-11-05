package job

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
)

// scanDueAt is a service whose scan is due at one moment, or cannot start.
type scanDueAt struct {
	at  time.Time
	err error
}

func (s scanDueAt) ScanIfDue(now time.Time) (bool, error) {
	return s.err == nil && now.Equal(s.at), s.err
}

// logged counts the lines of the log at the level, and those above it, that mention the text.
func logged(level, text string) int {
	n := 0
	for _, line := range logger.GetLogs(10240, level) {
		if strings.Contains(line, text) {
			n++
		}
	}
	return n
}

var thirdOfNovember = time.Date(2026, 11, 3, 3, 0, 0, 0, time.UTC)

func runPsiphonScanJob(svc dueScanner, at time.Time) {
	(&PsiphonScanJob{psiphon: svc, now: func() time.Time { return at }}).Run()
}

func TestPsiphonScanJobSaysAtTheInfoLevelThatAScanHasStarted(t *testing.T) {
	const text = "weekly scan of every region has started"
	before := logged("info", text)

	runPsiphonScanJob(scanDueAt{at: thirdOfNovember}, thirdOfNovember)

	if got := logged("info", text) - before; got != 1 {
		t.Fatalf("%d lines say at the info level that the scan started, want one for the scan that was due at the time the job ran", got)
	}
}

func TestPsiphonScanJobSaysNothingWhenNoScanIsDue(t *testing.T) {
	const text = "weekly scan of every region has started"
	before := logged("info", text)

	runPsiphonScanJob(scanDueAt{at: thirdOfNovember.Add(time.Hour)}, thirdOfNovember)

	if got := logged("info", text) - before; got != 0 {
		t.Fatalf("%d lines say that a scan started, want none while none is due", got)
	}
}

func TestPsiphonScanJobKeepsTheReasonAScanCouldNotStartForTheDebugLog(t *testing.T) {
	const started, reason = "weekly scan of every region has started", "Psiphon is not installed"
	startedBefore, infoBefore, debugBefore := logged("info", started), logged("info", reason), logged("debug", reason)

	runPsiphonScanJob(scanDueAt{err: errors.New(reason)}, thirdOfNovember)

	if got := logged("info", started) - startedBefore; got != 0 {
		t.Errorf("%d lines say that a scan started, want none for one that could not", got)
	}
	if got := logged("info", reason) - infoBefore; got != 0 {
		t.Errorf("%d lines give the reason at the info level, want it in the debug log only: a refusal is no failure", got)
	}
	if got := logged("debug", reason) - debugBefore; got != 1 {
		t.Errorf("%d lines in the debug log give the reason, want one", got)
	}
}
