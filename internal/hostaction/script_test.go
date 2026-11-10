package hostaction

import "testing"

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
