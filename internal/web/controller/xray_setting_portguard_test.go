package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/mhsanaei/3x-ui/v3/internal/database/dbtest"
	"github.com/mhsanaei/3x-ui/v3/internal/portguard"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

type memGuardStore struct{ state portguard.State }

func (s *memGuardStore) Load() (portguard.State, error) { return s.state, nil }
func (s *memGuardStore) Save(st portguard.State) error  { s.state = st; return nil }

type memGuardFirewall struct{ closed []portguard.Port }

func (f *memGuardFirewall) Apply(_ context.Context, p []portguard.Port) error {
	f.closed = slices.Clone(p)
	return nil
}

func (f *memGuardFirewall) Remove(context.Context) error {
	f.closed = nil
	return nil
}

// portGuardAPI serves the routes with a database and a shared Manager whose firewall lives in memory.
func portGuardAPI(t *testing.T) (*gin.Engine, *memGuardFirewall) {
	t.Helper()
	dbtest.InitDB(t, filepath.Join(t.TempDir(), "x-ui.db"))
	fw := &memGuardFirewall{}
	service.SetPortGuard(portguard.New(&memGuardStore{}, fw, func() []portguard.Port {
		return []portguard.Port{{Number: 22, Proto: portguard.TCP}}
	}))
	t.Cleanup(func() { service.SetPortGuard(nil) })
	return sidecarEngine(), fw
}

type guardReply struct {
	Success bool   `json:"success"`
	Msg     string `json:"msg"`
	Obj     struct {
		Closed       []portguard.Port `json:"closed"`
		Close        []portguard.Port `json:"close"`
		Refused      []any            `json:"refused"`
		Lease        bool             `json:"lease"`
		WindowSecond int              `json:"windowSeconds"`
		Ruleset      string           `json:"ruleset"`
		Pending      *struct {
			DeadlineMs int64 `json:"deadlineMs"`
		} `json:"pending"`
		Protected    []portguard.Port `json:"protected"`
		LeaseSeconds int              `json:"leaseSeconds"`
	} `json:"obj"`
}

func guardCall(t *testing.T, engine *gin.Engine, method, path, body string) guardReply {
	t.Helper()
	req := httptest.NewRequest(method, "/panel/api/xray/portguard"+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)
	var out guardReply
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("%s %s answered %q: %v", method, path, w.Body.String(), err)
	}
	return out
}

func TestPortGuardAPIClosesAPortWithALeaseAndConfirmsIt(t *testing.T) {
	engine, fw := portGuardAPI(t)

	st := guardCall(t, engine, http.MethodGet, "", "")
	if !st.Success || st.Obj.Closed == nil || len(st.Obj.Closed) != 0 || st.Obj.Pending != nil {
		t.Fatalf("status of a fresh host = %+v, want success, an empty list of closed ports and nothing pending", st)
	}
	if st.Obj.LeaseSeconds != 120 {
		t.Errorf("leaseSeconds = %d, want 120", st.Obj.LeaseSeconds)
	}

	plan := guardCall(t, engine, http.MethodPost, "/plan", `{"ports":[{"port":8443,"proto":"tcp"}]}`)
	if !plan.Success || !plan.Obj.Lease || plan.Obj.WindowSecond != 120 || !strings.Contains(plan.Obj.Ruleset, "x_ui_portguard") {
		t.Errorf("plan = %+v, want a lease of 120 seconds and the nft script", plan)
	}
	if len(fw.closed) != 0 {
		t.Fatal("the plan touched the firewall")
	}

	applied := guardCall(t, engine, http.MethodPost, "/apply", `{"ports":[{"port":8443,"proto":"tcp"}]}`)
	if !applied.Success || applied.Obj.Pending == nil || applied.Obj.Pending.DeadlineMs <= 0 {
		t.Fatalf("apply = %+v, want success and a pending change with a deadline", applied)
	}
	if want := []portguard.Port{{Number: 8443, Proto: portguard.TCP}}; !slices.Equal(fw.closed, want) {
		t.Errorf("the firewall closes %v, want %v", fw.closed, want)
	}

	confirmed := guardCall(t, engine, http.MethodPost, "/confirm", "")
	if !confirmed.Success || confirmed.Obj.Pending != nil || len(confirmed.Obj.Closed) != 1 {
		t.Errorf("confirm = %+v, want success, nothing pending and 8443 still closed", confirmed)
	}
	if again := guardCall(t, engine, http.MethodPost, "/confirm", ""); again.Success || !strings.Contains(again.Msg, "no change waits") {
		t.Errorf("a second confirm = %+v, want a failure that says no change waits", again)
	}
}

func TestPortGuardAPIRollsBackWhatWaits(t *testing.T) {
	engine, fw := portGuardAPI(t)
	guardCall(t, engine, http.MethodPost, "/apply", `{"ports":[{"port":8443,"proto":"tcp"}]}`)
	rolled := guardCall(t, engine, http.MethodPost, "/rollback", "")
	if !rolled.Success || rolled.Obj.Pending != nil || len(rolled.Obj.Closed) != 0 {
		t.Errorf("rollback = %+v, want success with nothing pending and nothing closed", rolled)
	}
	if len(fw.closed) != 0 {
		t.Errorf("the firewall still closes %v", fw.closed)
	}
}

func TestPortGuardAPIRefusesAProtectedPortAndChangesNothing(t *testing.T) {
	engine, fw := portGuardAPI(t)
	r := guardCall(t, engine, http.MethodPost, "/apply", `{"ports":[{"port":22,"proto":"tcp"},{"port":8443,"proto":"tcp"}]}`)
	if r.Success || !strings.Contains(r.Msg, "22/tcp") {
		t.Errorf("apply = %+v, want a failure that names 22/tcp", r)
	}
	if len(fw.closed) != 0 {
		t.Errorf("the firewall closes %v after a refused change", fw.closed)
	}
	plan := guardCall(t, engine, http.MethodPost, "/plan", `{"ports":[{"port":22,"proto":"tcp"}]}`)
	if !plan.Success || len(plan.Obj.Refused) != 1 {
		t.Errorf("plan = %+v, want the refusal in the answer, not a failure", plan)
	}
}

func TestPortGuardAPIRefusesABodyThatIsNotJSON(t *testing.T) {
	engine, fw := portGuardAPI(t)
	for _, path := range []string{"/plan", "/apply"} {
		if r := guardCall(t, engine, http.MethodPost, path, "{nope"); r.Success {
			t.Errorf("POST %s with a broken body succeeded: %+v", path, r)
		}
	}
	if len(fw.closed) != 0 {
		t.Errorf("the firewall closes %v after a broken request", fw.closed)
	}
}
