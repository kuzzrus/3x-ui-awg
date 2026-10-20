package version

import "testing"

func TestCompareRejectsUnexpectedFormats(t *testing.T) {
	if _, ok := Compare("latest", "2.9.3"); ok {
		t.Fatal("expected non-semver latest tag to be rejected")
	}
	if _, ok := Compare("v2.9", "2.9.3"); ok {
		t.Fatal("expected short version to be rejected")
	}
	if _, ok := Compare("3.5.0-awg", "3.5.0"); ok {
		t.Fatal("expected an awg suffix without a build number to be rejected")
	}
}

// This fork's "-awg.N" release tags (see internal/config/version) must compare
// correctly against plain upstream-style tags and against other awg builds.
func TestCompareOrdersAwgBuilds(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"v3.5.0-awg.1", "3.5.0-awg.1", 0},
		{"v3.5.0-awg.2", "3.5.0-awg.1", 1},
		{"v3.5.0-awg.1", "3.5.0-awg.2", -1},
		{"v3.5.0-awg.1", "3.5.0", 1},
		{"v3.5.0", "3.5.0-awg.1", -1},
		{"v3.6.0", "3.5.0-awg.1", 1},
		{"v3.5.0-awg.1", "3.6.0", -1},
		{"3.10.2-awg.1", "3.9.0-awg.7", 1},
	}
	for _, tc := range cases {
		got, ok := Compare(tc.a, tc.b)
		if !ok || got != tc.want {
			t.Fatalf("Compare(%q, %q) = %d, %v, want %d, true", tc.a, tc.b, got, ok, tc.want)
		}
	}
}
