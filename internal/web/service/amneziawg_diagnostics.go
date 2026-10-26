package service

import (
	"strings"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/amneziawg"
	"github.com/mhsanaei/3x-ui/v3/internal/amneziawgkern"
	"github.com/mhsanaei/3x-ui/v3/internal/amneziawgnet"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
)

var (
	// kernelPeerDump reads the live peers of a kernel interface, and
	// kernelInterfaceUp says whether an inbound has one; variables so tests can
	// stand in for the awg tool and the host.
	kernelPeerDump    = amneziawgkern.Dump
	kernelInterfaceUp = func(id int) bool { return amneziawgkern.GetManager().Running(id) }
)

// diagnoseAmneziaWG is amneziawgnet.Diagnose for whichever datapath serves the
// inbound: an embedded one is looked up in its manager, a kernel-mode one is
// read from its interface. The diagnostics modal and the logs page therefore
// agree with the status card, which counts both.
func diagnoseAmneziaWG(inst amneziawg.Instance) amneziawgnet.Diagnostics {
	if !inst.KernelMode {
		return amneziawgnet.Diagnose(inst.Id, inst.Peers)
	}
	return diagnoseKernelAmneziaWG(inst, kernelInterfaceUp(inst.Id), kernelPeerDump)
}

// diagnoseKernelAmneziaWG builds the snapshot of a kernel-mode inbound from
// `awg show <iface> dump`. Like the embedded path, an interface that is not up
// comes back as an empty Diagnostics, and one whose dump cannot be read as
// running with no client rows; a configured peer the kernel has not seen still
// gets a row, with a zero handshake.
func diagnoseKernelAmneziaWG(inst amneziawg.Instance, running bool, dump func(string) ([]amneziawgkern.PeerStat, error)) amneziawgnet.Diagnostics {
	if !running {
		return amneziawgnet.Diagnostics{}
	}
	diag := amneziawgnet.Diagnostics{Running: true, ListenPort: inst.ListenPort}
	iface := amneziawgkern.InterfaceName(inst.Id)
	stats, err := dump(iface)
	if err != nil {
		logger.Debug("amneziawg diagnostics: reading", iface, "failed:", err)
		return diag
	}
	byKey := make(map[string]amneziawgkern.PeerStat, len(stats))
	for _, st := range stats {
		byKey[st.PublicKey] = st
	}
	diag.Clients = make([]amneziawgnet.ClientDiagnostic, 0, len(inst.Peers))
	for _, p := range inst.Peers {
		st := byKey[p.PublicKey]
		cd := amneziawgnet.ClientDiagnostic{
			Email:      p.Email,
			RxBytes:    uint64(max(st.RxBytes, 0)),
			TxBytes:    uint64(max(st.TxBytes, 0)),
			AllowedIPs: strings.Join(p.AllowedIPs, ", "),
		}
		if st.Endpoint != "" && st.Endpoint != "(none)" {
			cd.Endpoint = st.Endpoint
		}
		if st.LatestHandshake > 0 {
			cd.LastHandshake = time.Unix(st.LatestHandshake, 0)
		}
		diag.Clients = append(diag.Clients, cd)
	}
	return diag
}

// amneziawgInterfaceName is the interface a peer row names: the real kernel
// interface for a kernel-mode inbound, the embedded path's label otherwise.
func amneziawgInterfaceName(inst amneziawg.Instance) string {
	if inst.KernelMode {
		return amneziawgkern.InterfaceName(inst.Id)
	}
	return inst.InterfaceName
}
