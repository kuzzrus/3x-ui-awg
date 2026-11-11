package hostaction

import (
	"runtime"
	"testing"
)

func TestLabelKeepsOnlyWhatAUnitNameAllows(t *testing.T) {
	for name, want := range map[string]string{
		"warpwp --install-current":    "warpwp-install-current",
		"write /usr/local/bin/warpwp": "write-usr-local-bin-warp",
		"Apt-Get  Install":            "apt-get-install",
		"!!!":                         "action",
	} {
		if got := label(name); got != want {
			t.Errorf("label(%q) = %q, want %q", name, got, want)
		}
	}
}

// A systemd-run child inherits nothing, so the ambient proxy settings have to be handed over under their own names.
func TestProxyEnvHandsOverEachSetVariableUnderItsOwnName(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows env var names are case-insensitive, so both spellings resolve")
	}
	unsetAll := func(t *testing.T) {
		t.Helper()
		for _, key := range proxyEnvKeys {
			t.Setenv(key, "")
		}
	}

	t.Run("nothing set returns nil", func(t *testing.T) {
		unsetAll(t)
		if got := ProxyEnv(); got != nil {
			t.Fatalf("ProxyEnv() = %v, want nil", got)
		}
	})

	t.Run("forwards each set var under its own name", func(t *testing.T) {
		unsetAll(t)
		t.Setenv("https_proxy", "socks5://127.0.0.1:10808")
		t.Setenv("no_proxy", "10.0.0.0/8,localhost")
		got := ProxyEnv()
		want := []string{"https_proxy=socks5://127.0.0.1:10808", "no_proxy=10.0.0.0/8,localhost"}
		if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
			t.Fatalf("ProxyEnv() = %v, want %v", got, want)
		}
	})

	// A deliberately HTTP-only proxy config must not silently gain HTTPS traffic.
	t.Run("http_proxy is not promoted to https_proxy", func(t *testing.T) {
		unsetAll(t)
		t.Setenv("http_proxy", "http://127.0.0.1:8080")
		got := ProxyEnv()
		want := []string{"http_proxy=http://127.0.0.1:8080"}
		if len(got) != len(want) || got[0] != want[0] {
			t.Fatalf("ProxyEnv() = %v, want %v", got, want)
		}
	})
}

func TestSystemdUnavailableRecognisesWhatSystemdRunSaysWithoutASystemdToTalkTo(t *testing.T) {
	for output, want := range map[string]bool{
		"System has not been booted with systemd as init system (PID 1). Can't operate.": true,
		"Failed to connect to bus: No such file or directory":                            true,
		"Failed to start transient service unit: Access denied":                          false,
	} {
		if got := SystemdUnavailable(output); got != want {
			t.Errorf("SystemdUnavailable(%q) = %v, want %v", output, got, want)
		}
	}
}
