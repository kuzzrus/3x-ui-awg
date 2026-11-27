package hostfw

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Lines of ufw's rule files as ufw 0.36.2 wrote them: each rule of the admin's was added to an empty ufw and the files were read.
const (
	v4 = "0.0.0.0/0"
	v6 = "::/0"
)

func tuple(action, proto, dport, dst, sport, src, tail string) string {
	return "### tuple ### " + action + " " + proto + " " + dport + " " + dst + " " + sport + " " + src + " " + tail
}

// both is the pair of lines that a rule for IPv4 and IPv6 leaves, and the one that either leaves alone.
func both(action, proto, dport, sport, tail string) []string {
	return []string{tuple(action, proto, dport, v4, sport, v4, tail), tuple(action, proto, dport, v6, sport, v6, tail)}
}

// markTail is the end of the line of a rule that the panel added, due at unix time at.
func markTail(direction string, at int64) string {
	return direction + " comment=" + hex.EncodeToString([]byte(fmt.Sprintf("%s %d", ufwMark, at)))
}

func TestParseUFWRules(t *testing.T) {
	data := strings.Join([]string{
		"*filter",
		"### RULES ###",
		"",
		tuple("allow", "tcp", "80", v4, "any", v4, "in comment=782d75692061636d65"),
		"-A ufw-user-input -p tcp --dport 80 -j ACCEPT",
		tuple("deny", "udp", "53", v4, "any", "1.2.3.4", "in"),
		tuple("allow", "tcp", "80", v4, "any", v4, "Test%20HTTP - in"),
		tuple("allow", "tcp", "80", v4, "any", v4, "in_lo comment=61646d696e206e6f7465"),
		"### tuple ### allow tcp 80",
		"### tuple ### allow tcp 80 0.0.0.0/0 any 0.0.0.0/0",
		"### END RULES ###",
	}, "\n")
	got := parseUFWRules(data)
	want := []ufwRule{
		{action: "allow", proto: "tcp", dport: "80", dst: v4, sport: "any", src: v4, direction: "in", comment: "x-ui acme"},
		{action: "deny", proto: "udp", dport: "53", dst: v4, sport: "any", src: "1.2.3.4", direction: "in"},
		{action: "allow", proto: "tcp", dport: "80", dst: v4, sport: "any", src: v4, direction: "in"},
		{action: "allow", proto: "tcp", dport: "80", dst: v4, sport: "any", src: v4, direction: "in_lo", comment: "admin note"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("parseUFWRules =\n%+v\nwant\n%+v", got, want)
	}
}

func TestParseUFWRulesKeepsAnUndecodableCommentOutOfTheOwnersMark(t *testing.T) {
	got := parseUFWRules(tuple("allow", "tcp", "80", v4, "any", v4, "in comment=zz"))
	if len(got) != 1 || got[0].comment != "" {
		t.Fatalf("parseUFWRules = %+v, want one rule with no comment", got)
	}
}

func TestTheMarkNamesTheTimeTheRuleIsDueAndNothingElseDoes(t *testing.T) {
	cases := []struct {
		comment string
		due     int64
		marked  bool
	}{
		{"x-ui acme 1800000300", 1800000300, true},
		{"x-ui acme 0", 0, true},
		{"x-ui acme", 0, false},
		{"x-ui acme ", 0, false},
		{"x-ui acme soon", 0, false},
		{"x-ui acme 1 2", 0, false},
		{"x-ui acmes 1800000300", 0, false},
		{"my x-ui acme 1800000300", 0, false},
		{"admin note", 0, false},
		{"", 0, false},
	}
	for _, tc := range cases {
		due, marked := (ufwRule{comment: tc.comment}).expiry()
		if due != tc.due || marked != tc.marked {
			t.Errorf("expiry of %q = %d, %v; want %d, %v", tc.comment, due, marked, tc.due, tc.marked)
		}
	}
}

// Each case is a rule of the admin's, the lines ufw wrote for it, and whether the panel may add its own rule for 80/tcp. Where ufw
// was asked, it took the panel's rule for the admin's one, and rewrote it, in every case that is a conflict here.
func TestUFWPortConflicts(t *testing.T) {
	cases := []struct {
		admin    string
		lines    []string
		conflict bool
		// ufwSaid is what ufw said to the panel's rule when this was there: "updated" for "Rule updated", "added" for "Rule added",
		// "" when it was not asked.
		ufwSaid string
	}{
		{"allow 80/tcp", both("allow", "tcp", "80", "any", "in"), true, "updated"},
		{"deny 80/tcp", both("deny", "tcp", "80", "any", "in"), true, "updated"},
		{"limit 80/tcp", both("limit", "tcp", "80", "any", "in"), true, "updated"},
		{"reject 80/tcp", both("reject", "tcp", "80", "any", "in"), true, "updated"},
		{"allow http", both("allow", "tcp", "80", "any", "in"), true, "updated"},
		{"allow from 0.0.0.0/0 to any port 80 proto tcp", []string{tuple("allow", "tcp", "80", v4, "any", v4, "in")}, true, "updated for IPv4 only"},
		{"allow from ::/0 to any port 80 proto tcp", []string{tuple("allow", "tcp", "80", v6, "any", v6, "in")}, true, "updated for IPv6 only"},
		{"allow 80/tcp comment 'admin note'", both("allow", "tcp", "80", "any", "in comment=61646d696e206e6f7465"), true, "updated"},
		{"allow 80", both("allow", "any", "80", "any", "in"), true, "added"},
		{"allow 80:90/tcp", both("allow", "tcp", "80:90", "any", "in"), true, "added"},
		{"allow 79:81/tcp", both("allow", "tcp", "79:81", "any", "in"), true, "added"},
		{"allow 80,443/tcp", both("allow", "tcp", "80,443", "any", "in"), true, "added"},
		{"allow 'Test HTTP'", both("allow", "tcp", "80", "any", "Test%20HTTP - in"), true, "added"},
		{"route allow proto tcp from any to any port 80", both("route:allow", "tcp", "80", "any", "in"), true, "added"},
		{"allow proto tcp from any to any", both("allow", "tcp", "any", "any", "in"), true, "added"},
		{"allow from 1.2.3.4 to any port 80 proto tcp", []string{tuple("allow", "tcp", "80", v4, "any", "1.2.3.4", "in")}, false, "added"},
		{"allow from 1.2.3.0/24 to any port 80 proto tcp", []string{tuple("allow", "tcp", "80", v4, "any", "1.2.3.0/24", "in")}, false, "added"},
		{"allow 80/udp", both("allow", "udp", "80", "any", "in"), false, "added"},
		{"allow 8080/tcp", both("allow", "tcp", "8080", "any", "in"), false, "added"},
		{"allow 70:79/tcp", both("allow", "tcp", "70:79", "any", "in"), false, "added"},
		{"allow 81:90/tcp", both("allow", "tcp", "81:90", "any", "in"), false, ""},
		{"allow in on lo to any port 80 proto tcp", both("allow", "tcp", "80", "any", "in_lo"), false, "added"},
		{"allow to 10.0.0.5 port 80 proto tcp", []string{tuple("allow", "tcp", "80", "10.0.0.5", "any", v4, "in")}, false, "added"},
		{"allow out 80/tcp", both("allow", "tcp", "80", "any", "out"), false, "added"},
		{"allow from any port 80 to any proto tcp", []string{tuple("allow", "tcp", "any", v4, "80", v4, "in")}, false, "added"},
		{"nothing", nil, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.admin, func(t *testing.T) {
			conflict, ours := ufwPort(parseUFWRules(strings.Join(tc.lines, "\n")), 80)
			if conflict != tc.conflict {
				t.Errorf("conflict = %v, want %v", conflict, tc.conflict)
			}
			if len(ours) != 0 {
				t.Errorf("ours = %v, want the admin's rule not to be taken for the panel's", ours)
			}
			// What ufw rewrote must never be a case that is let through: the panel would add its rule over the admin's.
			if strings.HasPrefix(tc.ufwSaid, "updated") && !conflict {
				t.Errorf("ufw rewrites the admin's rule when the panel adds its own, and ufwPort does not call that a conflict")
			}
		})
	}
}

func TestUFWPortRecognisesItsOwnRuleAndOnlyThat(t *testing.T) {
	const due = 1800000300
	cases := []struct {
		name     string
		lines    []string
		conflict bool
		ours     []int64
	}{
		{"both families", both("allow", "tcp", "80", "any", markTail("in", due)), false, []int64{due, due}},
		{"one family", []string{tuple("allow", "tcp", "80", v4, "any", v4, markTail("in", due))}, false, []int64{due}},
		{"the admin's rule next to it", append(both("allow", "tcp", "80", "any", markTail("in", due)), tuple("deny", "tcp", "80", v4, "any", v4, "in")), true, []int64{due, due}},
		{"another comment", both("allow", "tcp", "80", "any", "in comment=61646d696e206e6f7465"), true, nil},
		{"no comment", both("allow", "tcp", "80", "any", "in"), true, nil},
		{"the mark without a time", both("allow", "tcp", "80", "any", "in comment=782d75692061636d65"), true, nil},
		{"the mark on a deny", both("deny", "tcp", "80", "any", markTail("in", due)), true, nil},
		{"the mark on udp", both("allow", "udp", "80", "any", markTail("in", due)), false, nil},
		{"the mark on another port", both("allow", "tcp", "8080", "any", markTail("in", due)), false, nil},
		{"the mark on a rule for one address", []string{tuple("allow", "tcp", "80", v4, "any", "1.2.3.4", markTail("in", due))}, false, nil},
		{"the mark on a rule for one interface", both("allow", "tcp", "80", "any", markTail("in_eth0", due)), false, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conflict, ours := ufwPort(parseUFWRules(strings.Join(tc.lines, "\n")), 80)
			if conflict != tc.conflict || !slices.Equal(ours, tc.ours) {
				t.Errorf("ufwPort = conflict %v, ours %v; want conflict %v, ours %v", conflict, ours, tc.conflict, tc.ours)
			}
		})
	}
}

func TestUFWOwnListsThePanelsOwnRulesOnly(t *testing.T) {
	lines := slices.Concat(
		both("allow", "tcp", "80", "any", markTail("in", 10)),
		both("allow", "tcp", "8080", "any", markTail("in", 20)),
		both("allow", "tcp", "22", "any", "in"),
		both("deny", "tcp", "81", "any", markTail("in", 30)),
		[]string{tuple("allow", "tcp", "82", v4, "any", "1.2.3.4", markTail("in", 40))},
	)
	got := ufwOwn(parseUFWRules(strings.Join(lines, "\n")))
	want := []ufwLeftover{{80, 10}, {80, 10}, {8080, 20}, {8080, 20}}
	if !slices.Equal(got, want) {
		t.Fatalf("ufwOwn = %v, want %v", got, want)
	}
}

func TestPortListHasAPortInAListOrARange(t *testing.T) {
	cases := []struct {
		list string
		want bool
	}{
		{"80", true},
		{"79", false},
		{"any", true},
		{"80,443", true},
		{"22,443", false},
		{"22,80:90", true},
		{"1:79", false},
		{"1:80", true},
		{"80:80", true},
		{"81:90", false},
		{"x", false},
		{"80:x", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := (ufwRule{proto: "tcp", dport: tc.list}).covers(80); got != tc.want {
			t.Errorf("covers(80) of %q = %v, want %v", tc.list, got, tc.want)
		}
	}
}

func TestACoveringRuleNeedsTcpOrAnyProtocol(t *testing.T) {
	for proto, want := range map[string]bool{"tcp": true, "any": true, "udp": false, "": false} {
		if got := (ufwRule{proto: proto, dport: "80"}).covers(80); got != want {
			t.Errorf("covers(80) of protocol %q = %v, want %v", proto, got, want)
		}
	}
}

func TestReadUFWRulesReadsBothFilesAndDoesWithoutTheIPv6One(t *testing.T) {
	dir := t.TempDir()
	v4File, v6File := filepath.Join(dir, "user.rules"), filepath.Join(dir, "user6.rules")
	before := ufwRuleFiles
	ufwRuleFiles = []string{v4File, v6File}
	t.Cleanup(func() { ufwRuleFiles = before })
	write := func(path string, lines ...string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	write(v4File, tuple("allow", "tcp", "22", v4, "any", v4, "in"))
	rules, err := readUFWRules()
	if err != nil || len(rules) != 1 {
		t.Fatalf("with no IPv6 file: %d rules, error %v, want the one rule and no error", len(rules), err)
	}

	write(v6File, tuple("allow", "tcp", "22", v6, "any", v6, "in"))
	if rules, err = readUFWRules(); err != nil || len(rules) != 2 {
		t.Fatalf("with both files: %d rules, error %v, want 2 and no error", len(rules), err)
	}

	if err := os.Remove(v4File); err != nil {
		t.Fatal(err)
	}
	if _, err = readUFWRules(); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("with no IPv4 file: error %v, want it not to be read at all, which is not 'no rules'", err)
	}

	write(v4File, tuple("allow", "tcp", "22", v4, "any", v4, "in"))
	if err := os.Remove(v6File); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(v6File, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err = readUFWRules(); err == nil {
		t.Fatal("an IPv6 file that cannot be read was taken for one that is not there")
	}
}
