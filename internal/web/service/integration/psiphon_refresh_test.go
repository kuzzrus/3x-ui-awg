package integration

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
)

// refreshLines counts the lines of the log that mention text.
func refreshLines(text string) int {
	n := 0
	for _, line := range logger.GetLogs(10240, "debug") {
		if strings.Contains(line, text) {
			n++
		}
	}
	return n
}

// withBinaryRetry shortens the retry of the refresh for the length of the test: one delay per retry, so none is one attempt.
func withBinaryRetry(t *testing.T, delays ...time.Duration) {
	t.Helper()
	was := binaryRefreshDelays
	binaryRefreshDelays = delays
	t.Cleanup(func() { binaryRefreshDelays = was })
}

// asks is how many times the refresh has asked for the build to be brought to the pinned one.
func (a *answeringTunnels) asks() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.upgrades
}

func TestRefreshBinarySaysWhenItReplacedTheBuild(t *testing.T) {
	fake := useTunnels(t, nil)
	installedPsiphon(t)
	fake.upgrade = func(context.Context, *http.Client) (bool, error) { return true, nil }
	const said = "the installed build was replaced by the pinned one"
	before := refreshLines(said)

	(&PsiphonService{}).refreshBinary()

	if got := refreshLines(said) - before; got != 1 {
		t.Fatalf("%d lines say that the build was replaced, want one", got)
	}
}

// The first attempt can fall before Xray carries the panel's own outbound, so a failed fetch is tried again.
func TestRefreshBinaryFetchesTheBuildAgainAfterAFailedAttempt(t *testing.T) {
	fake := useTunnels(t, nil)
	installedPsiphon(t)
	fake.upgrade = func(context.Context, *http.Client) (bool, error) {
		if fake.asks() == 1 {
			return false, errors.New("no route to github")
		}
		return true, nil
	}
	withBinaryRetry(t, time.Millisecond, time.Millisecond, time.Millisecond)
	const said = "the installed build was replaced by the pinned one"
	const warned = "cannot bring the installed build to the pinned one:"
	saidBefore, warnedBefore := refreshLines(said), refreshLines(warned)

	(&PsiphonService{}).refreshBinary()

	if got := fake.asks(); got != 2 {
		t.Fatalf("the refresh asked %d times, want a second attempt after the first one failed", got)
	}
	if got := refreshLines(said) - saidBefore; got != 1 {
		t.Errorf("%d lines say that the build was replaced, want one", got)
	}
	if got := refreshLines(warned) - warnedBefore; got != 0 {
		t.Errorf("%d warnings for a fetch that succeeded on its second attempt, want none", got)
	}
}

// A fetch that keeps failing is tried as often as the refresh allows and then said once.
func TestRefreshBinaryGivesUpAfterItsAttempts(t *testing.T) {
	fake := useTunnels(t, nil)
	installedPsiphon(t)
	fake.upgrade = func(context.Context, *http.Client) (bool, error) { return false, errors.New("no route to github") }
	withBinaryRetry(t, time.Millisecond, time.Millisecond)
	const warned = "cannot bring the installed build to the pinned one: no route to github"
	before := refreshLines(warned)

	(&PsiphonService{}).refreshBinary()

	if got := fake.asks(); got != 3 {
		t.Errorf("the refresh asked %d times, want the 3 attempts it is allowed", got)
	}
	if got := refreshLines(warned) - before; got != 1 {
		t.Errorf("%d warnings for a fetch that failed every time, want one", got)
	}
}

func TestRefreshBinaryWarnsWhenTheBuildCannotBeFetched(t *testing.T) {
	fake := useTunnels(t, nil)
	installedPsiphon(t)
	withBinaryRetry(t)
	fake.upgrade = func(context.Context, *http.Client) (bool, error) { return false, errors.New("no route to github") }
	const warned = "cannot bring the installed build to the pinned one: no route to github"
	const replaced = "the installed build was replaced by the pinned one"
	warnedBefore, replacedBefore := refreshLines(warned), refreshLines(replaced)

	(&PsiphonService{}).refreshBinary()

	if got := refreshLines(warned) - warnedBefore; got != 1 {
		t.Errorf("%d lines give the reason, want one", got)
	}
	if got := refreshLines(replaced) - replacedBefore; got != 0 {
		t.Errorf("%d lines say that the build was replaced, want none for a failed fetch", got)
	}
}

// The file reached the disk and a tunnel did not come back: the log must not say that the build could not be fetched.
func TestRefreshBinarySaysWhenOnlyTheRestartFailed(t *testing.T) {
	fake := useTunnels(t, nil)
	installedPsiphon(t)
	withBinaryRetry(t, time.Millisecond, time.Millisecond, time.Millisecond)
	fake.upgrade = func(context.Context, *http.Client) (bool, error) { return true, errors.New("country IT did not start") }
	const said = "replaced by the pinned one, but restarting the tunnels failed: country IT did not start"
	const misreported = "cannot bring the installed build to the pinned one"
	saidBefore, misreportedBefore := refreshLines(said), refreshLines(misreported)

	(&PsiphonService{}).refreshBinary()

	if got := refreshLines(said) - saidBefore; got != 1 {
		t.Errorf("%d lines say that the restart failed after the replacement, want one", got)
	}
	if got := refreshLines(misreported) - misreportedBefore; got != 0 {
		t.Errorf("%d lines say that the build could not be fetched, want none for a build that was put in place", got)
	}
}

// A panel that never installed Psiphon asks nothing of anyone at its boot, and reads no setting for it.
func TestRefreshBinaryDoesNothingWithoutAnInstall(t *testing.T) {
	fake := useTunnels(t, nil)
	t.Setenv("XUI_BIN_FOLDER", t.TempDir())

	(&PsiphonService{}).refreshBinary()

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.upgrades != 0 {
		t.Errorf("the refresh asked %d times for the build to be brought to the pinned one, want none without an install", fake.upgrades)
	}
}

func TestRefreshBinaryIsQuietWhenThereIsNothingToDo(t *testing.T) {
	useTunnels(t, nil)
	installedPsiphon(t)
	before := refreshLines("the installed build") + refreshLines("cannot bring the installed build")

	(&PsiphonService{}).refreshBinary()

	if got := refreshLines("the installed build") + refreshLines("cannot bring the installed build") - before; got != 0 {
		t.Fatalf("%d lines about the build, want none when it is the pinned one", got)
	}
}

// The refresh waits for Xray, which the boot starts after the sidecars: AutoStart must not start it.
func TestAutoStartLeavesTheRefreshOfTheBuildToTheBoot(t *testing.T) {
	fake := useTunnels(t, nil)
	installedPsiphon(t)

	(&PsiphonService{}).AutoStart()
	time.Sleep(100 * time.Millisecond) // long enough for a refresh started in the background to have asked

	if got := fake.asks(); got != 0 {
		t.Errorf("AutoStart asked %d times for the build to be brought to the pinned one, want none before Xray is up", got)
	}
}

// The boot must not wait for a download: a slow host would hold up the whole panel.
func TestRefreshBinaryReturnsAtOnceBesideTheBoot(t *testing.T) {
	fake := useTunnels(t, nil)
	installedPsiphon(t)
	asked, release := make(chan struct{}), make(chan struct{})
	fake.upgrade = func(context.Context, *http.Client) (bool, error) {
		close(asked)
		<-release
		return false, nil
	}
	defer close(release)

	returned := make(chan struct{})
	go func() {
		(&PsiphonService{}).RefreshBinary()
		close(returned)
	}()

	select {
	case <-returned:
	case <-time.After(10 * time.Second):
		t.Fatal("RefreshBinary waited for the download")
	}
	select {
	case <-asked:
	case <-time.After(10 * time.Second):
		t.Fatal("RefreshBinary never asked for the build to be brought to the pinned one")
	}
}
