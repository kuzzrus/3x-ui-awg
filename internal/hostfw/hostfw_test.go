package hostfw

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
)

// answers maps a command to what it prints; a command that is not there is missing from the host.
type answers map[string]string

func (a answers) run(_ context.Context, name string, _ ...string) ([]byte, error) {
	out, ok := a[name]
	if !ok {
		return nil, errors.New("not found")
	}
	return []byte(out), nil
}

// The pieces of what nft -j list ruleset prints.
func nftDump(items ...string) string {
	head := `{"metainfo":{"version":"1.0.9","json_schema_version":1}}`
	return `{"nftables":[` + strings.Join(append([]string{head}, items...), ",") + `]}`
}

func nftTable(family, name string) string {
	return `{"table":{"family":"` + family + `","name":"` + name + `","handle":1}}`
}

func nftChain(family, table, name, extra string) string {
	return `{"chain":{"family":"` + family + `","table":"` + table + `","name":"` + name + `","handle":2` + extra + `}}`
}

func nftRule(family, table, chain, expr string) string {
	return `{"rule":{"family":"` + family + `","table":"` + table + `","chain":"` + chain + `","handle":9,"expr":` + expr + `}}`
}

// A base chain of the filter type on the input hook, then its rules.
func nftInput(family, table, chain, policy string, rules ...string) string {
	items := []string{
		nftTable(family, table),
		nftChain(family, table, chain, `,"type":"filter","hook":"input","prio":0,"policy":"`+policy+`"`),
	}
	for _, expr := range rules {
		items = append(items, nftRule(family, table, chain, expr))
	}
	return nftDump(items...)
}

const (
	acceptEstablished = `[{"match":{"op":"in","left":{"ct":{"key":"state"}},"right":["established","related"]}},{"accept":null}]`
	rejectAll         = `[{"counter":{"packets":0,"bytes":0}},{"reject":{"type":"icmpx","expr":"admin-prohibited"}}]`
	dropPort22        = `[{"match":{"op":"==","left":{"payload":{"protocol":"tcp","field":"dport"}},"right":22}},{"drop":null}]`
)

const ufwActive = "-P INPUT DROP\n-P FORWARD DROP\n-P OUTPUT ACCEPT\n-N ufw-before-input\n-N ufw-user-input\n-A INPUT -j ufw-before-logging-input\n"

func TestDetect(t *testing.T) {
	cases := []struct {
		name   string
		host   answers
		ignore []string
		want   Status
	}{
		{
			name: "a host with no firewall tool at all cannot be read",
			host: answers{},
			want: Status{Managers: []string{}},
		},
		{
			name: "ufw is told by its chains, and its policy is what drops",
			host: answers{"iptables": ufwActive, "ip6tables": ufwActive},
			want: Status{Readable: true, Managers: []string{UFW}, Incoming4: PolicyDeny, Incoming6: PolicyDeny},
		},
		{
			name: "ufw on nftables is told by its chains in the nft listing",
			host: answers{
				"nft": nftDump(
					nftTable("ip", "filter"),
					nftChain("ip", "filter", "INPUT", `,"type":"filter","hook":"input","prio":0,"policy":"drop"`),
					nftChain("ip", "filter", "ufw-before-input", ""),
				),
				"ip6tables": "-P INPUT ACCEPT\n",
			},
			want: Status{Readable: true, Managers: []string{UFW}, Incoming4: PolicyDeny, Incoming6: PolicyAllow},
		},
		{
			name: "firewalld is told by its table, and its closing reject makes the default a deny",
			host: answers{"nft": nftInput("inet", "firewalld", "filter_INPUT", "accept", acceptEstablished, rejectAll)},
			want: Status{Readable: true, Managers: []string{Firewalld}, Incoming4: PolicyDeny, Incoming6: PolicyDeny},
		},
		{
			name: "firewalld is told by firewall-cmd when its rules are not in nftables",
			host: answers{"iptables": "-P INPUT ACCEPT\n", "firewall-cmd": "running\n"},
			want: Status{Readable: true, Managers: []string{Firewalld}, Incoming4: PolicyAllow},
		},
		{
			name: "firewall-cmd alone is enough to read the host, though not its policy",
			host: answers{"firewall-cmd": "running\n"},
			want: Status{Readable: true, Managers: []string{Firewalld}},
		},
		{
			name: "firewall-cmd that does not say running is not firewalld",
			host: answers{"firewall-cmd": "not running\n"},
			want: Status{Managers: []string{}},
		},
		{
			name: "rules of its own with a drop policy are a firewall without a front end",
			host: answers{"nft": nftInput("inet", "filter", "input", "drop", acceptEstablished)},
			want: Status{Readable: true, Managers: []string{}, Incoming4: PolicyDeny, Incoming6: PolicyDeny},
		},
		{
			name: "an inet chain covers both versions, an ip chain only IPv4",
			host: answers{"nft": nftInput("ip", "filter", "input", "drop")},
			want: Status{Readable: true, Managers: []string{}, Incoming4: PolicyDeny, Incoming6: PolicyAllow},
		},
		{
			name: "an ip6 chain covers IPv6 only",
			host: answers{"nft": nftInput("ip6", "filter", "input", "drop")},
			want: Status{Readable: true, Managers: []string{}, Incoming4: PolicyAllow, Incoming6: PolicyDeny},
		},
		{
			name: "a chain that accepts, and drops only what it names, is no default deny",
			host: answers{"nft": nftInput("inet", "filter", "input", "accept", acceptEstablished, dropPort22)},
			want: Status{Readable: true, Managers: []string{}, Incoming4: PolicyAllow, Incoming6: PolicyAllow},
		},
		{
			name: "a closing drop of everything makes an accepting chain a deny",
			host: answers{"nft": nftInput("inet", "filter", "input", "accept", acceptEstablished, `[{"drop":null}]`)},
			want: Status{Readable: true, Managers: []string{}, Incoming4: PolicyDeny, Incoming6: PolicyDeny},
		},
		{
			name: "only the last rule of a chain is the closing one",
			host: answers{"nft": nftInput("inet", "filter", "input", "accept", rejectAll, acceptEstablished)},
			want: Status{Readable: true, Managers: []string{}, Incoming4: PolicyAllow, Incoming6: PolicyAllow},
		},
		{
			name: "a chain that is not a filter on the input hook decides nothing",
			host: answers{"nft": nftDump(
				nftTable("ip", "nat"),
				nftChain("ip", "nat", "INPUT", `,"type":"nat","hook":"input","prio":100,"policy":"drop"`),
				nftChain("ip", "filter", "FORWARD", `,"type":"filter","hook":"forward","prio":0,"policy":"drop"`),
			)},
			want: Status{Readable: true, Managers: []string{}, Incoming4: PolicyAllow, Incoming6: PolicyAllow},
		},
		{
			name:   "the table of the port guard is not the firewall of the host",
			host:   answers{"nft": nftInput("inet", "x_ui_portguard", "input", "drop")},
			ignore: []string{"x_ui_portguard"},
			want:   Status{Readable: true, Managers: []string{}, Incoming4: PolicyAllow, Incoming6: PolicyAllow},
		},
		{
			name: "the same table is a firewall when it is not ignored",
			host: answers{"nft": nftInput("inet", "x_ui_portguard", "input", "drop")},
			want: Status{Readable: true, Managers: []string{}, Incoming4: PolicyDeny, Incoming6: PolicyDeny},
		},
		{
			name: "an accepting policy that ends in a reject of everything is a deny, as on a cloud image",
			host: answers{"iptables": "-P INPUT ACCEPT\n-A INPUT -m state --state RELATED,ESTABLISHED -j ACCEPT\n-A INPUT -j REJECT --reject-with icmp-host-prohibited\n"},
			want: Status{Readable: true, Managers: []string{}, Incoming4: PolicyDeny},
		},
		{
			name: "a closing DROP of everything is a deny too",
			host: answers{"iptables": "-P INPUT ACCEPT\n-A INPUT -j DROP\n"},
			want: Status{Readable: true, Managers: []string{}, Incoming4: PolicyDeny},
		},
		{
			name: "a reject that looks at the packet is not a deny of everything",
			host: answers{"iptables": "-P INPUT ACCEPT\n-A INPUT -p tcp -j REJECT --reject-with tcp-reset\n"},
			want: Status{Readable: true, Managers: []string{}, Incoming4: PolicyAllow},
		},
		{
			name: "a jump to a chain of the admin's, even one named like a target, is not a drop",
			host: answers{"iptables": "-P INPUT ACCEPT\n-N DROP_LIST\n-A INPUT -j DROP_LIST\n"},
			want: Status{Readable: true, Managers: []string{}, Incoming4: PolicyAllow},
		},
		{
			name: "a drop that is not the last rule is not a deny of everything",
			host: answers{"iptables": "-P INPUT ACCEPT\n-A INPUT -j DROP\n-A INPUT -p tcp --dport 22 -j ACCEPT\n"},
			want: Status{Readable: true, Managers: []string{}, Incoming4: PolicyAllow},
		},
		{
			name: "output that is not the nft listing is not read",
			host: answers{"nft": "Error: Operation not permitted"},
			want: Status{Managers: []string{}},
		},
		{
			name: "JSON that is not a listing of rules is not read either",
			host: answers{"nft": "{}"},
			want: Status{Managers: []string{}},
		},
		{
			name: "a firewall with no rules at all lets everything in",
			host: answers{"nft": `{"nftables":[{"metainfo":{"version":"1.0.9"}}]}`},
			want: Status{Readable: true, Managers: []string{}, Incoming4: PolicyAllow, Incoming6: PolicyAllow},
		},
		{
			name: "a deny of either tool is a deny: a packet has to pass them all",
			host: answers{"nft": nftInput("ip", "filter", "input", "accept"), "iptables": "-P INPUT DROP\n"},
			want: Status{Readable: true, Managers: []string{}, Incoming4: PolicyDeny, Incoming6: PolicyAllow},
		},
		{
			name: "both front ends are named, in order",
			host: answers{"nft": nftInput("inet", "firewalld", "filter_INPUT", "accept", rejectAll), "iptables": "-N ufw-user-input\n"},
			want: Status{Readable: true, Managers: []string{Firewalld, UFW}, Incoming4: PolicyDeny, Incoming6: PolicyDeny},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Detect(context.Background(), tc.host.run, tc.ignore...)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Detect = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestDetectAsksEachToolItsOwnQuestion(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, name+" "+strings.Join(args, " "))
		return nil, errors.New("not found")
	}
	Detect(context.Background(), run)
	slices.Sort(calls)
	want := []string{"firewall-cmd --state", "ip6tables -w 5 -S", "iptables -w 5 -S", "nft -j list ruleset"}
	if !slices.Equal(calls, want) {
		t.Fatalf("commands = %q, want %q", calls, want)
	}
}

func TestBoundedBufferRefusesWhatIsTooLarge(t *testing.T) {
	var b boundedBuffer
	if _, err := b.Write(make([]byte, maxOutput)); err != nil {
		t.Fatalf("the most that is allowed was refused: %v", err)
	}
	if _, err := b.Write([]byte{0}); !errors.Is(err, errTooLarge) {
		t.Fatalf("one byte more: error = %v, want errTooLarge", err)
	}
	if b.Len() != maxOutput {
		t.Fatalf("the buffer holds %d bytes, want %d", b.Len(), maxOutput)
	}
}

func TestStatusListsAreNeverNull(t *testing.T) {
	st := Detect(context.Background(), answers{}.run)
	if st.Managers == nil {
		t.Fatal("Managers is nil, so the page would read null for a list")
	}
}
