package service

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/amneziawg"
	"github.com/mhsanaei/3x-ui/v3/internal/amneziawgkern"
	"github.com/mhsanaei/3x-ui/v3/internal/amneziawgnet"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func kernelDiagnosticsInstance() amneziawg.Instance {
	return amneziawg.Instance{
		Id:            7,
		ListenPort:    51820,
		KernelMode:    true,
		InterfaceName: "awg7",
		Peers: []amneziawg.Peer{
			{Email: "alice", PublicKey: "pkA", AllowedIPs: []string{"10.8.1.2/32"}},
			{Email: "bob", PublicKey: "pkB", AllowedIPs: []string{"10.8.1.3/32", "fd00::3/128"}},
		},
	}
}

// A kernel-mode inbound is not in the embedded manager, so the modal and the
// logs page used to call a healthy tunnel stopped. Its rows come from the
// interface's own dump instead: configured peers in their order, an unknown key
// ignored, a peer the kernel has not seen listed with a zero handshake.
func TestDiagnoseKernelAmneziaWGReadsThePeersOfTheInterface(t *testing.T) {
	handshake := time.Now().Add(-30 * time.Second).Unix()
	var asked string
	dump := func(iface string) ([]amneziawgkern.PeerStat, error) {
		asked = iface
		return []amneziawgkern.PeerStat{
			{PublicKey: "pkA", Endpoint: "198.51.100.4:40000", LatestHandshake: handshake, RxBytes: 1000, TxBytes: 5000},
			{PublicKey: "pkB", Endpoint: "(none)"},
			{PublicKey: "pkStranger", Endpoint: "203.0.113.9:1", LatestHandshake: handshake, RxBytes: 1, TxBytes: 1},
		}, nil
	}

	diag := diagnoseKernelAmneziaWG(kernelDiagnosticsInstance(), true, dump)

	if asked != "xui-awg7" {
		t.Fatalf("dump read interface %q, want the kernel interface xui-awg7", asked)
	}
	if !diag.Running || diag.ListenPort != 51820 {
		t.Fatalf("diagnostics = running %v, port %d, want running on 51820", diag.Running, diag.ListenPort)
	}
	if len(diag.Clients) != 2 || diag.Clients[0].Email != "alice" || diag.Clients[1].Email != "bob" {
		t.Fatalf("clients = %+v, want alice then bob and nobody else", diag.Clients)
	}
	alice, bob := diag.Clients[0], diag.Clients[1]
	if !alice.Connected() || alice.LastHandshake.Unix() != handshake {
		t.Fatalf("alice handshake = %v, want %d", alice.LastHandshake, handshake)
	}
	if alice.RxBytes != 1000 || alice.TxBytes != 5000 || alice.Endpoint != "198.51.100.4:40000" || alice.AllowedIPs != "10.8.1.2/32" {
		t.Fatalf("alice = %+v", alice)
	}
	if bob.Connected() || bob.Endpoint != "" || bob.RxBytes != 0 || bob.AllowedIPs != "10.8.1.3/32, fd00::3/128" {
		t.Fatalf("bob = %+v, want a peer never seen: no handshake, no endpoint, its configured addresses", bob)
	}
}

func TestDiagnoseKernelAmneziaWGOfAnInterfaceThatIsDownIsEmpty(t *testing.T) {
	dump := func(string) ([]amneziawgkern.PeerStat, error) {
		t.Fatal("a stopped interface must not be dumped")
		return nil, nil
	}
	diag := diagnoseKernelAmneziaWG(kernelDiagnosticsInstance(), false, dump)
	if diag.Running || len(diag.Clients) != 0 || diag.ListenPort != 0 {
		t.Fatalf("diagnostics = %+v, want the empty snapshot of a stopped inbound", diag)
	}
}

// The embedded path answers the same way when its UAPI dump fails: the
// interface is up, its peers are simply unknown.
func TestDiagnoseKernelAmneziaWGKeepsRunningWhenTheDumpCannotBeRead(t *testing.T) {
	dump := func(string) ([]amneziawgkern.PeerStat, error) { return nil, errors.New("awg: no such device") }
	diag := diagnoseKernelAmneziaWG(kernelDiagnosticsInstance(), true, dump)
	if !diag.Running || diag.ListenPort != 51820 || len(diag.Clients) != 0 {
		t.Fatalf("diagnostics = %+v, want running with no client rows", diag)
	}
}

// The routing itself: a kernel-mode inbound is read from its interface, an
// embedded one from the userspace manager, which has nothing for this id.
func TestDiagnoseAmneziaWGPicksTheDatapathOfTheInbound(t *testing.T) {
	prevDump, prevUp := kernelPeerDump, kernelInterfaceUp
	t.Cleanup(func() { kernelPeerDump, kernelInterfaceUp = prevDump, prevUp })
	dumped := 0
	kernelPeerDump = func(string) ([]amneziawgkern.PeerStat, error) {
		dumped++
		return []amneziawgkern.PeerStat{{PublicKey: "pkA", LatestHandshake: time.Now().Unix()}}, nil
	}
	kernelInterfaceUp = func(int) bool { return true }

	kernel := diagnoseAmneziaWG(kernelDiagnosticsInstance())
	if !kernel.Running || len(kernel.Clients) != 2 || !kernel.Clients[0].Connected() || dumped != 1 {
		t.Fatalf("kernel inbound = %+v after %d dumps, want its interface read once", kernel, dumped)
	}

	embedded := kernelDiagnosticsInstance()
	embedded.KernelMode = false
	if diag := diagnoseAmneziaWG(embedded); diag.Running || dumped != 1 {
		t.Fatalf("embedded inbound = %+v after %d dumps, want the userspace manager asked, not the kernel", diag, dumped)
	}
}

func TestAmneziawgLogActivityListsKernelAndEmbeddedInbounds(t *testing.T) {
	setupConflictDB(t)
	seedInboundConflict(t, "awg-kernel", "0.0.0.0", 51820, model.AmneziaWG, ``,
		`{"server":{"privateKey":"priv","publicKey":"pub","subnetIp":"10.8.1.0","subnetCidr":24,"kernelMode":true},`+
			`"clients":[{"email":"kernel-user","enable":true,"publicKey":"pkA","allowedIPs":["10.8.1.2/32"]}]}`)
	seedInboundConflict(t, "awg-embedded", "0.0.0.0", 51821, model.AmneziaWG, ``,
		`{"server":{"privateKey":"priv","publicKey":"pub","subnetIp":"10.9.1.0","subnetCidr":24},`+
			`"clients":[{"email":"embedded-user","enable":true,"publicKey":"pkB","allowedIPs":["10.9.1.2/32"]}]}`)

	handshake := time.Now().Add(-20 * time.Second).Unix()
	dump := func(string) ([]amneziawgkern.PeerStat, error) {
		return []amneziawgkern.PeerStat{{PublicKey: "pkA", Endpoint: "198.51.100.4:40000", LatestHandshake: handshake, RxBytes: 1000, TxBytes: 5000}}, nil
	}
	// The kernel inbound is read through its dump, the embedded one through a
	// stand-in for the userspace manager, as the real diagnose does.
	diagnose := func(inst amneziawg.Instance) amneziawgnet.Diagnostics {
		if inst.KernelMode {
			return diagnoseKernelAmneziaWG(inst, true, dump)
		}
		return amneziawgnet.Diagnostics{Running: true, Clients: []amneziawgnet.ClientDiagnostic{{Email: "embedded-user", AllowedIPs: "10.9.1.2/32"}}}
	}

	rows := amneziawgLogActivityWith(diagnose)
	if len(rows) != 2 {
		t.Fatalf("rows = %+v, want one per inbound", rows)
	}
	byEmail := map[string]PeerActivity{}
	for _, row := range rows {
		byEmail[row.Email] = row
	}

	var kernel model.Inbound
	if err := database.GetDB().Where("tag = ?", "awg-kernel").First(&kernel).Error; err != nil {
		t.Fatalf("read kernel inbound: %v", err)
	}
	k := byEmail["kernel-user"]
	if k.Interface != amneziawgkern.InterfaceName(kernel.Id) || !k.Online || k.Up != 1000 || k.Down != 5000 || k.Endpoint != "198.51.100.4:40000" {
		t.Fatalf("kernel row = %+v, want the kernel interface name, online, up 1000, down 5000", k)
	}
	var embedded model.Inbound
	if err := database.GetDB().Where("tag = ?", "awg-embedded").First(&embedded).Error; err != nil {
		t.Fatalf("read embedded inbound: %v", err)
	}
	if e := byEmail["embedded-user"]; e.Interface != fmt.Sprintf("awg%d", embedded.Id) || e.Online {
		t.Fatalf("embedded row = %+v, want the embedded label and offline", e)
	}
}

// stubKernelHost makes the kernel datapath answer from the given dump, as a
// host whose interface is up, for the length of the test.
func stubKernelHost(t *testing.T, stats []amneziawgkern.PeerStat) {
	t.Helper()
	prevDump, prevUp := kernelPeerDump, kernelInterfaceUp
	t.Cleanup(func() { kernelPeerDump, kernelInterfaceUp = prevDump, prevUp })
	kernelPeerDump = func(string) ([]amneziawgkern.PeerStat, error) { return stats, nil }
	kernelInterfaceUp = func(int) bool { return true }
}

const kernelModeAwgSettings = `{"server":{"privateKey":"priv","publicKey":"pub","subnetIp":"10.8.1.0","subnetCidr":24,"kernelMode":true},` +
	`"clients":[{"email":"kernel-user","enable":true,"publicKey":"pkA","allowedIPs":["10.8.1.2/32"]}]}`

// The modal reads the endpoint, not the helper: a kernel-mode inbound is not in
// the embedded manager, so only the kernel branch can report it running.
func TestGetAmneziaWGDiagnosticsReadsAKernelModeInbound(t *testing.T) {
	setupConflictDB(t)
	seedInboundConflict(t, "awg-kernel", "0.0.0.0", 51820, model.AmneziaWG, ``, kernelModeAwgSettings)
	handshake := time.Now().Add(-10 * time.Second).Unix()
	stubKernelHost(t, []amneziawgkern.PeerStat{
		{PublicKey: "pkA", Endpoint: "198.51.100.4:40000", LatestHandshake: handshake, RxBytes: 1000, TxBytes: 5000},
	})

	var ib model.Inbound
	if err := database.GetDB().Where("tag = ?", "awg-kernel").First(&ib).Error; err != nil {
		t.Fatalf("read seeded inbound: %v", err)
	}
	diag, err := (&InboundService{}).GetAmneziaWGDiagnostics(ib.Id)
	if err != nil {
		t.Fatalf("GetAmneziaWGDiagnostics: %v", err)
	}
	if !diag.Running || diag.ListenPort != 51820 {
		t.Fatalf("diagnostics = running %v on port %d, want running on 51820", diag.Running, diag.ListenPort)
	}
	if len(diag.Clients) != 1 || !diag.Clients[0].Connected() || diag.Clients[0].RxBytes != 1000 {
		t.Fatalf("clients = %+v, want kernel-user connected with its counters", diag.Clients)
	}
}

// The logs page goes through the real diagnose, not only through the seam: a
// kernel-mode inbound must show up there with the interface the kernel runs.
func TestAmneziawgLogActivityReadsAKernelModeInbound(t *testing.T) {
	setupConflictDB(t)
	seedInboundConflict(t, "awg-kernel", "0.0.0.0", 51820, model.AmneziaWG, ``, kernelModeAwgSettings)
	stubKernelHost(t, []amneziawgkern.PeerStat{
		{PublicKey: "pkA", Endpoint: "198.51.100.4:40000", LatestHandshake: time.Now().Unix(), RxBytes: 7, TxBytes: 9},
	})

	var ib model.Inbound
	if err := database.GetDB().Where("tag = ?", "awg-kernel").First(&ib).Error; err != nil {
		t.Fatalf("read seeded inbound: %v", err)
	}
	rows := amneziawgLogActivity()
	if len(rows) != 1 || rows[0].Email != "kernel-user" || rows[0].Interface != amneziawgkern.InterfaceName(ib.Id) || !rows[0].Online {
		t.Fatalf("rows = %+v, want kernel-user online on the kernel interface", rows)
	}
}

// A host that runs only kernel interfaces must not read as stopped on the logs
// page: its Running flag is the one the status card uses.
func TestGetAmneziaWGLogsRunningFollowsEitherDatapath(t *testing.T) {
	setupConflictDB(t)
	prev := amneziawgRunning
	t.Cleanup(func() { amneziawgRunning = prev })

	svc := &ServerService{}
	amneziawgRunning = func() bool { return true }
	if !svc.GetAmneziaWGLogs("10", "").Running {
		t.Fatal("logs report AmneziaWG stopped while a datapath is running")
	}
	amneziawgRunning = func() bool { return false }
	if svc.GetAmneziaWGLogs("10", "").Running {
		t.Fatal("logs report AmneziaWG running while no datapath is")
	}
}
