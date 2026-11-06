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

func TestRefreshBinaryWarnsWhenTheBuildCannotBeFetched(t *testing.T) {
	fake := useTunnels(t, nil)
	installedPsiphon(t)
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

// The boot must not wait for a download: a slow host would hold up the whole panel.
func TestAutoStartBringsTheBuildToThePinnedOneBesideTheBoot(t *testing.T) {
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
		(&PsiphonService{}).AutoStart()
		close(returned)
	}()

	select {
	case <-returned:
	case <-time.After(10 * time.Second):
		t.Fatal("AutoStart waited for the download")
	}
	select {
	case <-asked:
	case <-time.After(10 * time.Second):
		t.Fatal("AutoStart never asked for the build to be brought to the pinned one")
	}
}
