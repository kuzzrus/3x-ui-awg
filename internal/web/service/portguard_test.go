package service

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/portguard"
)

type memPortGuardStore struct{ state portguard.State }

func (s *memPortGuardStore) Load() (portguard.State, error) { return s.state, nil }
func (s *memPortGuardStore) Save(st portguard.State) error  { s.state = st; return nil }

type memPortGuardFirewall struct{ closed []portguard.Port }

func (f *memPortGuardFirewall) Apply(_ context.Context, p []portguard.Port) error {
	f.closed = slices.Clone(p)
	return nil
}

func (f *memPortGuardFirewall) Remove(context.Context) error {
	f.closed = nil
	return nil
}

// setupPortGuardTest gives the service a database and a shared Manager whose store and firewall live in memory.
func setupPortGuardTest(t *testing.T, protected ...portguard.Port) (*memPortGuardStore, *memPortGuardFirewall) {
	t.Helper()
	setupConflictDB(t)
	store, fw := &memPortGuardStore{}, &memPortGuardFirewall{}
	SetPortGuard(portguard.New(store, fw, func() []portguard.Port { return protected }))
	t.Cleanup(func() { SetPortGuard(nil) })
	return store, fw
}

func TestPortGuardStoreKeepsTheStateInTheSettings(t *testing.T) {
	setupConflictDB(t)
	store := portGuardStore{}
	if st, err := store.Load(); err != nil || len(st.Closed) != 0 || st.Pending != nil {
		t.Fatalf("Load before anything was saved = %+v, %v, want the empty state", st, err)
	}
	want := portguard.State{
		Closed:  []portguard.Port{{Number: 8443, Proto: portguard.TCP}, {Number: 51820, Proto: portguard.UDP}},
		Pending: &portguard.Pending{Deadline: time.Unix(1_800_000_000, 0).UTC(), Previous: []portguard.Port{{Number: 8443, Proto: portguard.TCP}}},
	}
	if err := store.Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !slices.Equal(got.Closed, want.Closed) || got.Pending == nil || !got.Pending.Deadline.Equal(want.Pending.Deadline) || !slices.Equal(got.Pending.Previous, want.Pending.Previous) {
		t.Errorf("Load = %+v, want %+v", got, want)
	}
}

func TestPortGuardStoreRefusesAStateThatCannotBeRead(t *testing.T) {
	setupConflictDB(t)
	if err := (&SettingService{}).SetPortGuardState("{not json"); err != nil {
		t.Fatalf("SetPortGuardState: %v", err)
	}
	if _, err := (portGuardStore{}).Load(); err == nil || !strings.Contains(err.Error(), "unreadable") {
		t.Errorf("Load error = %v, want it to say the stored state is unreadable", err)
	}
}

func TestPortGuardCandidatesListThePortsOfThisHost(t *testing.T) {
	setupPortGuardTest(t)
	settings := &SettingService{}
	if err := settings.SetPort(2053); err != nil {
		t.Fatalf("SetPort: %v", err)
	}
	if err := settings.setBool("subEnable", true); err != nil {
		t.Fatalf("subEnable: %v", err)
	}
	if err := settings.setInt("subPort", 2096); err != nil {
		t.Fatalf("subPort: %v", err)
	}
	node := 7
	seedInboundConflict(t, "ws", "0.0.0.0", 44431, model.VLESS, `{"network":"ws"}`, `{}`)
	seedInboundConflict(t, "hy", "", 44432, model.Hysteria, ``, `{}`)
	seedInboundConflict(t, "wg", "127.0.0.1", 44433, model.WireGuard, ``, `{}`)
	seedInboundConflict(t, "off", "0.0.0.0", 44434, model.VLESS, `{"network":"tcp"}`, `{}`)
	seedInboundConflictNode(t, "remote", "0.0.0.0", 44435, model.VLESS, `{"network":"tcp"}`, `{}`, &node)
	seedInboundConflict(t, "tp", "", 44436, model.Tproxy, ``, `{}`)
	seedInboundConflict(t, "kcp", "0.0.0.0", 44431, model.VLESS, `{"network":"kcp"}`, `{}`)
	disableInboundRow(t, loadInboundByTag(t, "off").Id)
	if err := database.GetDB().Create(&model.FrontProxyPathRoute{ChildId: loadInboundByTag(t, "ws").Id, Path: "cdn"}).Error; err != nil {
		t.Fatalf("seed path route: %v", err)
	}

	got, err := (&PortGuardService{}).candidates([]portguard.Port{{Number: 44432, Proto: portguard.UDP}}, []portguard.Port{{Number: 2053, Proto: portguard.TCP}})
	if err != nil {
		t.Fatalf("candidates: %v", err)
	}
	type row struct {
		port   int
		proto  portguard.Proto
		label  string
		routed bool
		loop   bool
		closed bool
		prot   bool
	}
	var rows []row
	for _, c := range got {
		rows = append(rows, row{c.Number, c.Proto, c.Label, c.ViaFrontProxy, c.Loopback, c.Closed, c.Protected})
	}
	// An inbound without a remark is named by its tag.
	want := []row{
		{2053, portguard.TCP, "panel", false, false, false, true},
		{2096, portguard.TCP, "subscription", false, false, false, false},
		{44431, portguard.TCP, "ws (vless)", true, false, false, false},
		{44431, portguard.UDP, "kcp (vless)", false, false, false, false},
		{44432, portguard.UDP, "hy (hysteria)", false, false, true, false},
		{44433, portguard.UDP, "wg (wireguard)", false, true, false, false},
	}
	if !slices.Equal(rows, want) {
		t.Errorf("candidates =\n%+v\nwant\n%+v", rows, want)
	}
}

func TestPortGuardCandidatesJoinTheServicesOfOnePort(t *testing.T) {
	setupPortGuardTest(t)
	if err := (&SettingService{}).SetPort(44440); err != nil {
		t.Fatalf("SetPort: %v", err)
	}
	seedInboundConflict(t, "shares", "127.0.0.1", 44440, model.VLESS, `{"network":"tcp"}`, `{}`)
	got, err := (&PortGuardService{}).candidates(nil, nil)
	if err != nil {
		t.Fatalf("candidates: %v", err)
	}
	var onPort []PortGuardCandidate
	for _, c := range got {
		if c.Number == 44440 {
			onPort = append(onPort, c)
		}
	}
	if len(onPort) != 1 {
		t.Fatalf("%d candidates on the port, want the services joined into 1: %+v", len(onPort), onPort)
	}
	if !strings.HasPrefix(onPort[0].Label, "panel, ") {
		t.Errorf("label = %q, want it to name the panel and the inbound", onPort[0].Label)
	}
	if onPort[0].Loopback {
		t.Error("Loopback = true although the panel listens on every address: the port is open as long as one of them is")
	}
}

func TestListensOnLoopbackOnly(t *testing.T) {
	cases := map[string]bool{
		"":                      false,
		"0.0.0.0":               false,
		"::":                    false,
		"203.0.113.9":           false,
		"127.0.0.1":             true,
		"127.1.2.3":             true,
		"::1":                   true,
		"[::1]":                 true,
		"localhost":             true,
		"/run/x-ui/panel.sock":  true,
		"@xray-inbound":         true,
		"not an address at all": false,
	}
	for listen, want := range cases {
		if got := listensOnLoopbackOnly(listen); got != want {
			t.Errorf("listensOnLoopbackOnly(%q) = %v, want %v", listen, got, want)
		}
	}
}

func TestPortGuardStatusShowsTheLeaseAndTheProtectedPorts(t *testing.T) {
	store, _ := setupPortGuardTest(t, portguard.Port{Number: 22, Proto: portguard.TCP})
	deadline := time.Unix(1_800_000_000, 0)
	store.state = portguard.State{
		Closed:  []portguard.Port{{Number: 8443, Proto: portguard.TCP}},
		Pending: &portguard.Pending{Deadline: deadline},
	}
	st, err := (&PortGuardService{}).Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.Pending == nil || st.Pending.DeadlineMs != deadline.UnixMilli() {
		t.Errorf("Pending = %+v, want the deadline %d", st.Pending, deadline.UnixMilli())
	}
	if st.Pending != nil && st.Pending.Previous == nil {
		t.Error("Pending.Previous is nil: the page reads a list, not null")
	}
	if want := []portguard.Port{{Number: 22, Proto: portguard.TCP}}; !slices.Equal(st.Protected, want) {
		t.Errorf("Protected = %v, want what the Manager protects: %v", st.Protected, want)
	}
	if st.LeaseSeconds != 120 {
		t.Errorf("LeaseSeconds = %d, want 120", st.LeaseSeconds)
	}
}

func TestPortGuardStatusOfAHostWithNothingClosedHasEmptyListsNotNulls(t *testing.T) {
	setupPortGuardTest(t)
	st, err := (&PortGuardService{}).Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.Closed == nil || st.Pending != nil {
		t.Errorf("Closed = %v, Pending = %+v, want an empty list and nothing pending", st.Closed, st.Pending)
	}
}

func TestPortGuardServiceClosesAndOpensThroughTheSharedManager(t *testing.T) {
	_, fw := setupPortGuardTest(t)
	svc := &PortGuardService{}
	if _, err := svc.Apply(context.Background(), []portguard.Port{{Number: 8443, Proto: portguard.TCP}}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if want := []portguard.Port{{Number: 8443, Proto: portguard.TCP}}; !slices.Equal(fw.closed, want) {
		t.Errorf("the firewall closes %v, want %v", fw.closed, want)
	}
	if err := svc.Rollback(context.Background()); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if len(fw.closed) != 0 {
		t.Errorf("the firewall still closes %v after the rollback", fw.closed)
	}
}
