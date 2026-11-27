package hostfw

import (
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"strconv"
	"strings"
)

// ufwRuleFiles are where ufw keeps the rules that were added to it: IPv4, then IPv6.
var ufwRuleFiles = []string{"/etc/ufw/user.rules", "/etc/ufw/user6.rules"}

// ufwMark is what the comment of the panel's own rule starts with. The unix time after it says when the rule is due to be taken
// out, so that a rule of a round that is still on, in this panel or another on the host, is not taken for a leftover.
const ufwMark = "x-ui acme"

// ufwRule is one rule as ufw writes it in its rule files, on a "### tuple ###" line: allow tcp 80 0.0.0.0/0 any 0.0.0.0/0 in
// comment=782d75692061636d65.
type ufwRule struct {
	action, proto, dport, dst, sport, src string
	// direction is in, out or fwd, with _ and the interface after it when the rule is for one interface.
	direction string
	comment   string
}

// parseUFWRules reads the rules of one of ufw's rule files.
func parseUFWRules(data string) []ufwRule {
	var rules []ufwRule
	for _, line := range strings.Split(data, "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "### tuple ### ")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		comment := ""
		if n := len(fields); n > 0 && strings.HasPrefix(fields[n-1], "comment=") {
			raw, _ := hex.DecodeString(strings.TrimPrefix(fields[n-1], "comment="))
			comment = string(raw)
			fields = fields[:n-1]
		}
		// A rule for an application profile has its two names between the source and the direction.
		if len(fields) < 7 {
			continue
		}
		rules = append(rules, ufwRule{
			action: fields[0], proto: fields[1], dport: fields[2], dst: fields[3], sport: fields[4], src: fields[5],
			direction: fields[len(fields)-1], comment: comment,
		})
	}
	return rules
}

// readUFWRules reads both rule files. A host that has no IPv6 file has no IPv6 rules; one that has no IPv4 file cannot be read.
func readUFWRules() ([]ufwRule, error) {
	var rules []ufwRule
	for i, path := range ufwRuleFiles {
		data, err := os.ReadFile(path)
		if err != nil {
			if i > 0 && errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, err
		}
		rules = append(rules, parseUFWRules(string(data))...)
	}
	return rules, nil
}

func isAnyAddress(addr string) bool { return addr == "0.0.0.0/0" || addr == "::/0" }

// generic is true for a rule about traffic from anywhere to anywhere, on any interface and any source port.
func (r ufwRule) generic() bool {
	return r.direction == "in" && r.sport == "any" && isAnyAddress(r.src) && isAnyAddress(r.dst)
}

// covers is true when the rule is about tcp port number: its protocol is tcp or any, and the port is in its list or range.
func (r ufwRule) covers(port int) bool {
	if r.proto != "tcp" && r.proto != "any" {
		return false
	}
	if r.dport == "any" {
		return true
	}
	for _, part := range strings.Split(r.dport, ",") {
		low, high, isRange := strings.Cut(part, ":")
		a, err := strconv.Atoi(low)
		if err != nil {
			continue
		}
		if !isRange {
			if a == port {
				return true
			}
			continue
		}
		if b, err := strconv.Atoi(high); err == nil && a <= port && port <= b {
			return true
		}
	}
	return false
}

// expiry is the time that the panel's mark names, and false for a rule that has no such mark.
func (r ufwRule) expiry() (int64, bool) {
	rest, ok := strings.CutPrefix(r.comment, ufwMark+" ")
	if !ok {
		return 0, false
	}
	at, err := strconv.ParseInt(rest, 10, 64)
	return at, err == nil
}

// ours is true for a rule that the panel added: allow tcp from anywhere, with its mark.
func (r ufwRule) ours() bool {
	_, marked := r.expiry()
	return marked && r.action == "allow" && r.proto == "tcp" && r.generic()
}

// ufwPort returns the due times of the panel's own rules for tcp port number, and whether a rule of the admin's speaks for it from
// anywhere, whatever it does: a conflict, since ufw takes a new rule for the same ports and addresses for it and rewrites it.
func ufwPort(rules []ufwRule, port int) (conflict bool, ours []int64) {
	for _, r := range rules {
		switch {
		case r.ours() && r.dport == strconv.Itoa(port):
			at, _ := r.expiry()
			ours = append(ours, at)
		case r.generic() && r.covers(port):
			conflict = true
		}
	}
	return conflict, ours
}

// ufwLeftover is a rule of the panel's, with the port it is for and the time it is due to be taken out.
type ufwLeftover struct {
	port   int
	expiry int64
}

// ufwOwn lists the panel's own rules.
func ufwOwn(rules []ufwRule) []ufwLeftover {
	var own []ufwLeftover
	for _, r := range rules {
		if !r.ours() {
			continue
		}
		at, _ := r.expiry()
		if port, err := strconv.Atoi(r.dport); err == nil {
			own = append(own, ufwLeftover{port: port, expiry: at})
		}
	}
	return own
}
