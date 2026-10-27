package integration

import (
	"path/filepath"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/dbtest"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func setSetting(t *testing.T, key, value string) {
	t.Helper()
	row := model.Setting{Key: key}
	if err := database.GetDB().Where(&row).Assign(model.Setting{Value: value}).FirstOrCreate(&model.Setting{}).Error; err != nil {
		t.Fatalf("set %s: %v", key, err)
	}
}

// The subscription server answers the JSON and Clash subscriptions under paths of their own, so the routing the reverse
// proxy is given must carry them, each with its own switch.
func TestFrontProxyOptionsCarryTheJSONAndClashSubscriptions(t *testing.T) {
	dbtest.InitDB(t, filepath.Join(t.TempDir(), "x-ui.db"))
	svc := &FrontProxyService{}
	setSetting(t, "subEnable", "true")
	setSetting(t, "subPath", "/rawpath/")
	setSetting(t, "subJsonEnable", "true")
	setSetting(t, "subJsonPath", "/jsonpath/")
	setSetting(t, "subClashEnable", "false")
	setSetting(t, "subClashPath", "/clashpath/")

	opts, err := svc.Options()
	if err != nil {
		t.Fatalf("Options: %v", err)
	}
	r := opts.Routing
	if r.SubPath != "/rawpath/" || !r.SubEnabled {
		t.Errorf("raw subscription = %q, enabled %v", r.SubPath, r.SubEnabled)
	}
	if r.SubJsonPath != "/jsonpath/" || !r.SubJsonEnabled {
		t.Errorf("JSON subscription = %q, enabled %v, want /jsonpath/ and on", r.SubJsonPath, r.SubJsonEnabled)
	}
	if r.SubClashPath != "/clashpath/" || r.SubClashEnabled {
		t.Errorf("Clash subscription = %q, enabled %v, want /clashpath/ and off", r.SubClashPath, r.SubClashEnabled)
	}

	setSetting(t, "subJsonEnable", "false")
	setSetting(t, "subClashEnable", "true")
	opts, err = svc.Options()
	if err != nil {
		t.Fatalf("Options: %v", err)
	}
	if opts.Routing.SubJsonEnabled || !opts.Routing.SubClashEnabled {
		t.Errorf("after the switches were flipped: JSON %v, Clash %v, want off and on", opts.Routing.SubJsonEnabled, opts.Routing.SubClashEnabled)
	}
}
