// Package hostfw reads, and never changes, what the firewall of the host looks like: which front end is in charge, and whether an
// incoming packet that no rule names is dropped. The panel's own port guard table is not counted, since it only closes ports.
package hostfw

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
)

// Policy is what the rules of the host do with an incoming packet that no rule lets in.
type Policy string

const (
	// PolicyUnknown means the rules of that IP version could not be read.
	PolicyUnknown Policy = ""
	// PolicyAllow means nothing drops such a packet, so every port a service listens on is reachable.
	PolicyAllow Policy = "allow"
	// PolicyDeny means such a packet is dropped or rejected, so a port is reachable only if a rule opens it.
	PolicyDeny Policy = "deny"
)

// The front ends of a firewall that the panel tells apart.
const (
	UFW       = "ufw"
	Firewalld = "firewalld"
)

// Status is the firewall of the host as far as it could be read.
type Status struct {
	// Readable is true when at least one tool that lists the rules answered.
	Readable bool `json:"readable"`
	// Managers are the front ends whose rules are loaded now.
	Managers []string `json:"managers"`
	// Incoming4 and Incoming6 are the policy for incoming IPv4 and IPv6 packets.
	Incoming4 Policy `json:"incoming4"`
	Incoming6 Policy `json:"incoming6"`
}

// Runner runs a command and returns what it printed; a command that is missing or fails is an error.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// findings is what the tools said, before it is put into a Status.
type findings struct {
	read4, read6 bool
	deny4, deny6 bool
	ufw          bool
	firewalld    bool
}

// Detect asks nft, iptables, ip6tables and firewall-cmd side by side, and sums up their answers; a table named in ignoreTables is
// left out of what counts as the host's firewall.
func Detect(ctx context.Context, run Runner, ignoreTables ...string) Status {
	var nft, ipt4, ipt6, fwd []byte
	var nftErr, ipt4Err, ipt6Err, fwdErr error
	var wg sync.WaitGroup
	wg.Go(func() { nft, nftErr = run(ctx, "nft", "-j", "list", "ruleset") })
	wg.Go(func() { ipt4, ipt4Err = run(ctx, "iptables", "-w", "5", "-S") })
	wg.Go(func() { ipt6, ipt6Err = run(ctx, "ip6tables", "-w", "5", "-S") })
	wg.Go(func() { fwd, fwdErr = run(ctx, "firewall-cmd", "--state") })
	wg.Wait()

	var f findings
	if nftErr == nil {
		f.addNft(nft, ignoreTables)
	}
	if ipt4Err == nil {
		f.addIptables(ipt4, false)
	}
	if ipt6Err == nil {
		f.addIptables(ipt6, true)
	}
	if fwdErr == nil && strings.TrimSpace(string(fwd)) == "running" {
		f.firewalld = true
	}
	return f.status()
}

func (f findings) status() Status {
	st := Status{Readable: f.read4 || f.read6 || f.firewalld, Managers: []string{}}
	if f.firewalld {
		st.Managers = append(st.Managers, Firewalld)
	}
	if f.ufw {
		st.Managers = append(st.Managers, UFW)
	}
	st.Incoming4 = policyOf(f.read4, f.deny4)
	st.Incoming6 = policyOf(f.read6, f.deny6)
	return st
}

func policyOf(read, deny bool) Policy {
	switch {
	case deny:
		return PolicyDeny
	case read:
		return PolicyAllow
	}
	return PolicyUnknown
}

// nftItem is one object of the list that nft -j prints; only the parts that are read here are declared.
type nftItem struct {
	Table *struct {
		Name string `json:"name"`
	} `json:"table"`
	Chain *struct {
		Family string `json:"family"`
		Table  string `json:"table"`
		Name   string `json:"name"`
		Type   string `json:"type"`
		Hook   string `json:"hook"`
		Policy string `json:"policy"`
	} `json:"chain"`
	Rule *struct {
		Family string                       `json:"family"`
		Table  string                       `json:"table"`
		Chain  string                       `json:"chain"`
		Expr   []map[string]json.RawMessage `json:"expr"`
	} `json:"rule"`
}

// addNft reads the output of nft -j list ruleset; output that is not that list is not read at all.
func (f *findings) addNft(data []byte, ignoreTables []string) {
	var dump struct {
		Nftables []nftItem `json:"nftables"`
	}
	if json.Unmarshal(data, &dump) != nil || dump.Nftables == nil {
		return
	}
	f.read4, f.read6 = true, true
	type chainKey struct{ family, table, chain string }
	// The rules of a chain come in order, so what is left in the map is the verdict of its last rule.
	lastDropsAll := map[chainKey]bool{}
	var filters []chainKey
	policies := map[chainKey]string{}
	for _, item := range dump.Nftables {
		switch {
		case item.Table != nil:
			if item.Table.Name == Firewalld {
				f.firewalld = true
			}
		case item.Chain != nil:
			c := item.Chain
			if strings.HasPrefix(c.Name, "ufw-") {
				f.ufw = true
			}
			if c.Type == "filter" && c.Hook == "input" && !slices.Contains(ignoreTables, c.Table) {
				key := chainKey{c.Family, c.Table, c.Name}
				filters = append(filters, key)
				policies[key] = c.Policy
			}
		case item.Rule != nil:
			lastDropsAll[chainKey{item.Rule.Family, item.Rule.Table, item.Rule.Chain}] = dropsEverything(item.Rule.Expr)
		}
	}
	for _, key := range filters {
		if policies[key] != "drop" && !lastDropsAll[key] {
			continue
		}
		f.deny4 = f.deny4 || key.family == "ip" || key.family == "inet"
		f.deny6 = f.deny6 || key.family == "ip6" || key.family == "inet"
	}
}

// dropsEverything is true for a rule that drops or rejects without looking at the packet.
func dropsEverything(expr []map[string]json.RawMessage) bool {
	verdict := false
	for _, e := range expr {
		for name := range e {
			switch name {
			case "drop", "reject":
				verdict = true
			case "counter", "log":
			default:
				return false
			}
		}
	}
	return verdict
}

// addIptables reads the output of iptables -S, or of ip6tables -S.
func (f *findings) addIptables(data []byte, v6 bool) {
	deny := false
	lastInput := ""
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "-P INPUT DROP" || line == "-P INPUT REJECT":
			deny = true
		case strings.HasPrefix(line, "-N ufw-"):
			f.ufw = true
		case strings.HasPrefix(line, "-A INPUT "):
			lastInput = line
		}
	}
	if !deny && isUnconditionalDrop(lastInput) {
		deny = true
	}
	if v6 {
		f.read6, f.deny6 = true, f.deny6 || deny
	} else {
		f.read4, f.deny4 = true, f.deny4 || deny
	}
}

// isUnconditionalDrop is true for a rule like "-A INPUT -j REJECT --reject-with ...", with no match before the target, which
// closes a chain whose policy is ACCEPT; a jump to a chain of the admin's own, even one named DROP_LIST, is not one.
func isUnconditionalDrop(rule string) bool {
	rest, ok := strings.CutPrefix(rule, "-A INPUT -j ")
	target, _, _ := strings.Cut(rest, " ")
	return ok && (target == "DROP" || target == "REJECT")
}
