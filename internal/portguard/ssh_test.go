package portguard

import (
	"net"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func writeSSHConfig(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func tcpPorts(numbers ...int) []Port {
	out := make([]Port, len(numbers))
	for i, n := range numbers {
		out[i] = Port{Number: n, Proto: TCP}
	}
	return out
}

func TestSSHPortsAlwaysHoldPort22(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name string
		body string // empty: the file does not exist
		want []Port
	}{
		{"no sshd_config", "", tcpPorts(22)},
		{"no Port line", "PermitRootLogin no\n", tcpPorts(22)},
		{"one Port line", "Port 2222\n", tcpPorts(22, 2222)},
		{"several Port lines", "Port 2222\nPort 2200\nPort 2222\n", tcpPorts(22, 2200, 2222)},
		{"equal sign forms", "Port=2201\nPort = 2202\n", tcpPorts(22, 2201, 2202)},
		{"keyword in any case", "PORT 2203\nport 2204\n", tcpPorts(22, 2203, 2204)},
		{"a commented line", "#Port 2205\n  # Port 2206\nPort 2207\n", tcpPorts(22, 2207)},
		{"ListenAddress with a port", "ListenAddress 0.0.0.0:2300\nListenAddress [::]:2301\n", tcpPorts(22, 2300, 2301)},
		{"ListenAddress without a port", "ListenAddress 0.0.0.0\nListenAddress ::\n", tcpPorts(22)},
		{"ports that are no ports", "Port 0\nPort 70000\nPort abc\nPort\nPort 65536\n", tcpPorts(22)},
		{"the lowest and the highest port", "Port 1\nPort 65535\n", tcpPorts(1, 22, 65535)},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(dir, "case", string(rune('a'+i)), "sshd_config")
			if c.body != "" {
				writeSSHConfig(t, filepath.Dir(path), "sshd_config", c.body)
			}
			if got := sshPortsOf(path); !slices.Equal(got, c.want) {
				t.Errorf("sshPortsOf = %v, want %v", got, c.want)
			}
		})
	}
}

func TestSSHPortsFollowIncludes(t *testing.T) {
	dir := t.TempDir()
	main := writeSSHConfig(t, dir, "sshd_config", "Include sshd_config.d/*.conf\nInclude "+filepath.Join(dir, "other", "extra.conf")+"\nPort 2222\n")
	writeSSHConfig(t, dir, "sshd_config.d/10-a.conf", "Port 2301\n")
	writeSSHConfig(t, dir, "sshd_config.d/20-b.conf", "Port 2302\n")
	writeSSHConfig(t, dir, "sshd_config.d/30-skipped.txt", "Port 2399\n")
	writeSSHConfig(t, dir, "other/extra.conf", "Port 2303\n")
	if got, want := sshPortsOf(main), tcpPorts(22, 2222, 2301, 2302, 2303); !slices.Equal(got, want) {
		t.Errorf("sshPortsOf = %v, want %v", got, want)
	}
}

func TestSSHPortsSurviveAnIncludeLoop(t *testing.T) {
	dir := t.TempDir()
	main := writeSSHConfig(t, dir, "sshd_config", "Include sshd_config\nInclude loop.conf\nPort 2222\n")
	writeSSHConfig(t, dir, "loop.conf", "Include sshd_config\nPort 2301\n")
	if got, want := sshPortsOf(main), tcpPorts(22, 2222, 2301); !slices.Equal(got, want) {
		t.Errorf("sshPortsOf = %v, want %v", got, want)
	}
}

// greeter listens on a loopback port and says greeting to whoever connects; a silent one says nothing and keeps the connection.
func greeter(t *testing.T, greeting string) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				if greeting != "" {
					_, _ = c.Write([]byte(greeting))
				}
				time.Sleep(3 * time.Second)
			}()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

func TestAnswersAsSSHRecognisesASSHBanner(t *testing.T) {
	if port := greeter(t, "SSH-2.0-OpenSSH_9.6\r\n"); !answersAsSSH(port) {
		t.Error("answersAsSSH = false for a port that greets with an SSH banner")
	}
}

func TestAnswersAsSSHLeavesOtherServicesAlone(t *testing.T) {
	cases := map[string]string{
		"an HTTP server":     "HTTP/1.1 400 Bad Request\r\n",
		"a banner too short": "SSH",
		"a lowercase banner": "ssh-2.0-x\r\n",
	}
	for name, greeting := range cases {
		if port := greeter(t, greeting); answersAsSSH(port) {
			t.Errorf("answersAsSSH = true for %s", name)
		}
	}
}

// A service that waits for the client to speak first, like an Xray inbound, must not hold the probe for long.
func TestAnswersAsSSHGivesUpOnASilentService(t *testing.T) {
	port := greeter(t, "")
	start := time.Now()
	if answersAsSSH(port) {
		t.Error("answersAsSSH = true for a service that says nothing")
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("the probe took %v, want it bounded by the timeout %v", took, sshProbeTimeout)
	}
}

func TestAnswersAsSSHIsFalseWhereNothingListens(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	if answersAsSSH(port) {
		t.Error("answersAsSSH = true for a port nothing listens on")
	}
}

// sshd may listen on IPv6 only.
func TestAnswersAsSSHAlsoLooksOnIPv6(t *testing.T) {
	ln, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		t.Skipf("no IPv6 loopback on this host: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_, _ = c.Write([]byte("SSH-2.0-OpenSSH_9.6\r\n"))
			_ = c.Close()
		}
	}()
	if !answersAsSSH(ln.Addr().(*net.TCPAddr).Port) {
		t.Error("answersAsSSH = false for an sshd that listens on ::1 only")
	}
}
