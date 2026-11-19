package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/portguard"
)

// portGuardStore keeps the state of the port guard in the settings table.
type portGuardStore struct{ setting SettingService }

func (s portGuardStore) Load() (portguard.State, error) {
	raw, err := s.setting.GetPortGuardState()
	if err != nil || raw == "" {
		return portguard.State{}, err
	}
	var st portguard.State
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		return portguard.State{}, fmt.Errorf("portguard: the stored state is unreadable: %w", err)
	}
	return st, nil
}

func (s portGuardStore) Save(st portguard.State) error {
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return s.setting.SetPortGuardState(string(raw))
}

var (
	portGuardMu  sync.Mutex
	portGuardMgr *portguard.Manager
)

// PortGuard is the one Manager of the host's closed ports, shared by the API, the job and the start of the panel.
func PortGuard() *portguard.Manager {
	portGuardMu.Lock()
	defer portGuardMu.Unlock()
	if portGuardMgr == nil {
		portGuardMgr = portguard.New(portGuardStore{}, portguard.NftFirewall{}, portGuardProtected)
	}
	return portGuardMgr
}

// SetPortGuard replaces the shared Manager, so that a test can put a firewall of its own under it; nil brings the real one back.
func SetPortGuard(m *portguard.Manager) {
	portGuardMu.Lock()
	defer portGuardMu.Unlock()
	portGuardMgr = m
}

// portGuardProtected is what a closed port must never cut off: sshd, and 443, which the front proxy of a host is reached on.
func portGuardProtected() []portguard.Port {
	return append(portguard.SSHPorts(), portguard.Port{Number: 443, Proto: portguard.TCP})
}

// PortGuardCandidate is a port some service of the panel listens on, which the admin may close.
type PortGuardCandidate struct {
	portguard.Port
	Label     string `json:"label"`
	InboundID int    `json:"inboundId,omitempty"`
	// ViaFrontProxy is true for an inbound that a path of the front proxy leads to, so that it is reached on 443 as well.
	ViaFrontProxy bool `json:"viaFrontProxy"`
	// Loopback is true for a service that listens on this host only, which no firewall rule is needed for.
	Loopback  bool `json:"loopback"`
	Closed    bool `json:"closed"`
	Protected bool `json:"protected"`
}

// PortGuardPending is a change of the closed ports that waits to be confirmed.
type PortGuardPending struct {
	DeadlineMs int64            `json:"deadlineMs"`
	Previous   []portguard.Port `json:"previous"`
}

// PortGuardStatus is what the page of the closed ports shows.
type PortGuardStatus struct {
	Available    bool                 `json:"available"`
	Closed       []portguard.Port     `json:"closed"`
	Pending      *PortGuardPending    `json:"pending"`
	Protected    []portguard.Port     `json:"protected"`
	Candidates   []PortGuardCandidate `json:"candidates"`
	LeaseSeconds int                  `json:"leaseSeconds"`
}

// PortGuardService closes ports of the host for the admin; the rules and the lease are kept by internal/portguard.
type PortGuardService struct {
	SettingService SettingService
}

// Status reports what is closed, what waits to be confirmed, and the ports that could be closed.
func (s *PortGuardService) Status() (PortGuardStatus, error) {
	st, err := PortGuard().State()
	if err != nil {
		return PortGuardStatus{}, err
	}
	protected := PortGuard().Protected()
	candidates, err := s.candidates(st.Closed, protected)
	if err != nil {
		return PortGuardStatus{}, err
	}
	out := PortGuardStatus{
		Available:    portguard.NftFirewall{}.Available(),
		Closed:       nonNil(st.Closed),
		Protected:    protected,
		Candidates:   candidates,
		LeaseSeconds: int(portguard.LeaseWindow / time.Second),
	}
	if st.Pending != nil {
		out.Pending = &PortGuardPending{DeadlineMs: st.Pending.Deadline.UnixMilli(), Previous: nonNil(st.Pending.Previous)}
	}
	return out, nil
}

func nonNil(ports []portguard.Port) []portguard.Port {
	if ports == nil {
		return []portguard.Port{}
	}
	return ports
}

// Plan says what closing ports would do, and does none of it.
func (s *PortGuardService) Plan(ports []portguard.Port) (portguard.Plan, error) {
	return PortGuard().Plan(ports)
}

// Apply closes exactly ports; see portguard.Manager.Apply for the lease it may start.
func (s *PortGuardService) Apply(ctx context.Context, ports []portguard.Port) (portguard.Plan, error) {
	return PortGuard().Apply(ctx, ports)
}

// Confirm keeps the change that waits to be confirmed.
func (s *PortGuardService) Confirm() error { return PortGuard().Confirm() }

// Rollback undoes the change that waits to be confirmed.
func (s *PortGuardService) Rollback(ctx context.Context) error { return PortGuard().Rollback(ctx) }

// candidates lists the TCP and UDP ports of the panel, the subscription server and the enabled inbounds of this host.
func (s *PortGuardService) candidates(closed, protected []portguard.Port) ([]PortGuardCandidate, error) {
	var out []PortGuardCandidate
	add := func(c PortGuardCandidate) {
		c.Closed = slices.Contains(closed, c.Port)
		c.Protected = slices.Contains(protected, c.Port)
		if i := slices.IndexFunc(out, func(o PortGuardCandidate) bool { return o.Port == c.Port }); i >= 0 {
			out[i].Label += ", " + c.Label
			out[i].Loopback = out[i].Loopback && c.Loopback
			out[i].ViaFrontProxy = out[i].ViaFrontProxy || c.ViaFrontProxy
			return
		}
		out = append(out, c)
	}

	if port, err := s.SettingService.GetPort(); err == nil && port > 0 {
		listen, _ := s.SettingService.GetListen()
		add(PortGuardCandidate{Port: portguard.Port{Number: port, Proto: portguard.TCP}, Label: "panel", Loopback: listensOnLoopbackOnly(listen)})
	}
	if enabled, _ := s.SettingService.GetSubEnable(); enabled {
		if port, err := s.SettingService.GetSubPort(); err == nil && port > 0 {
			listen, _ := s.SettingService.GetSubListen()
			add(PortGuardCandidate{Port: portguard.Port{Number: port, Proto: portguard.TCP}, Label: "subscription", Loopback: listensOnLoopbackOnly(listen)})
		}
	}

	var routes []model.FrontProxyPathRoute
	if err := database.GetDB().Find(&routes).Error; err != nil {
		return nil, err
	}
	routed := map[int]bool{}
	for _, r := range routes {
		routed[r.ChildId] = true
	}
	var rows []model.Inbound
	if err := database.GetDB().Where("enable = ? AND node_id IS NULL", true).Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		bits := inboundTransports(row.Protocol, row.StreamSettings, row.Settings)
		label := fmt.Sprintf("%s (%s)", row.Remark, row.Protocol)
		if row.Remark == "" {
			label = fmt.Sprintf("%s (%s)", row.Tag, row.Protocol)
		}
		for _, t := range []struct {
			bit   transportBits
			proto portguard.Proto
		}{{transportTCP, portguard.TCP}, {transportUDP, portguard.UDP}} {
			if bits&t.bit != 0 && row.Port > 0 {
				add(PortGuardCandidate{
					Port: portguard.Port{Number: row.Port, Proto: t.proto}, Label: label, InboundID: row.Id,
					ViaFrontProxy: routed[row.Id], Loopback: listensOnLoopbackOnly(row.Listen),
				})
			}
		}
	}
	slices.SortFunc(out, func(a, b PortGuardCandidate) int {
		if a.Number != b.Number {
			return a.Number - b.Number
		}
		return strings.Compare(string(a.Proto), string(b.Proto))
	})
	return out, nil
}

// listensOnLoopbackOnly is true for a listen address that only this host reaches, and for a unix socket.
func listensOnLoopbackOnly(listen string) bool {
	switch {
	case listen == "" || listen == "0.0.0.0" || listen == "::":
		return false
	case listen == "localhost" || strings.HasPrefix(listen, "/") || strings.HasPrefix(listen, "@"):
		return true
	}
	ip := net.ParseIP(strings.Trim(listen, "[]"))
	return ip != nil && ip.IsLoopback()
}
