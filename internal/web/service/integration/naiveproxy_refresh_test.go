package integration

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database/dbtest"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/naiveproxy"
)

// engineLines counts the lines of the log that mention text.
func engineLines(text string) int {
	n := 0
	for _, line := range logger.GetLogs(10240, "debug") {
		if strings.Contains(line, text) {
			n++
		}
	}
	return n
}

// useEngineUpgrade makes upgrade the answer of the engine upgrade for the length of the test, with an engine in place:
// the refresh looks at an install, and the service reads the proxy setting from a database, one of its own.
func useEngineUpgrade(t *testing.T, upgrade func(context.Context, *http.Client) (bool, error)) {
	t.Helper()
	dbtest.InitDB(t, filepath.Join(t.TempDir(), "x-ui.db"))
	t.Setenv("XUI_BIN_FOLDER", t.TempDir())
	if err := os.MkdirAll(naiveproxy.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(naiveproxy.BinPath(), []byte("an engine"), 0o750); err != nil {
		t.Fatal(err)
	}
	real := upgradeEngine
	upgradeEngine = upgrade
	t.Cleanup(func() { upgradeEngine = real })
}

func TestRefreshEngineSaysWhenItReplacedTheEngine(t *testing.T) {
	useEngineUpgrade(t, func(context.Context, *http.Client) (bool, error) { return true, nil })
	const said = "the installed engine was replaced by the pinned release"
	before := engineLines(said)

	(&NaiveProxyService{}).refreshEngine()

	if got := engineLines(said) - before; got != 1 {
		t.Fatalf("%d lines say that the engine was replaced, want one", got)
	}
}

func TestRefreshEngineWarnsWhenTheEngineCannotBeFetched(t *testing.T) {
	useEngineUpgrade(t, func(context.Context, *http.Client) (bool, error) { return false, errors.New("no route to github") })
	const warned = "cannot bring the installed engine to the pinned release: no route to github"
	const replaced = "the installed engine was replaced by the pinned release"
	warnedBefore, replacedBefore := engineLines(warned), engineLines(replaced)

	(&NaiveProxyService{}).refreshEngine()

	if got := engineLines(warned) - warnedBefore; got != 1 {
		t.Errorf("%d lines give the reason, want one", got)
	}
	if got := engineLines(replaced) - replacedBefore; got != 0 {
		t.Errorf("%d lines say that the engine was replaced, want none for a failed fetch", got)
	}
}

// A panel that never installed the engine asks nothing of anyone at its boot, and reads no setting for it.
func TestRefreshEngineDoesNothingWithoutAnEngine(t *testing.T) {
	asked := 0
	useEngineUpgrade(t, func(context.Context, *http.Client) (bool, error) {
		asked++
		return false, nil
	})
	if err := os.Remove(naiveproxy.BinPath()); err != nil {
		t.Fatal(err)
	}

	(&NaiveProxyService{}).refreshEngine()

	if asked != 0 {
		t.Errorf("the refresh asked %d times for the engine to be brought to the pinned release, want none without an engine", asked)
	}
}

func TestRefreshEngineIsQuietWhenThereIsNothingToDo(t *testing.T) {
	useEngineUpgrade(t, func(context.Context, *http.Client) (bool, error) { return false, nil })
	count := func() int {
		return engineLines("the installed engine") + engineLines("cannot bring the installed engine")
	}
	before := count()

	(&NaiveProxyService{}).refreshEngine()

	if got := count() - before; got != 0 {
		t.Fatalf("%d lines about the engine, want none when it is the pinned release", got)
	}
}

// The boot must not wait for a download: a slow host would hold up the whole panel.
func TestAutoStartBringsTheEngineToThePinnedReleaseBesideTheBoot(t *testing.T) {
	asked, release := make(chan struct{}), make(chan struct{})
	useEngineUpgrade(t, func(context.Context, *http.Client) (bool, error) {
		close(asked)
		<-release
		return false, nil
	})
	defer close(release)

	returned := make(chan struct{})
	go func() {
		(&NaiveProxyService{}).AutoStart()
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
		t.Fatal("AutoStart never asked for the engine to be brought to the pinned release")
	}
}
