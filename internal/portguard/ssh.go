package portguard

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// sshdConfig is where sshd reads its settings from.
const sshdConfig = "/etc/ssh/sshd_config"

// SSHPorts is the TCP ports sshd listens on, read from its settings and the files they include; 22 is always one of them, as
// the port a host answers on until it is moved, and a mistake here would lock the admin out.
func SSHPorts() []Port {
	return sshPortsOf(sshdConfig)
}

func sshPortsOf(config string) []Port {
	numbers := []int{22}
	seen := map[string]bool{}
	collectSSHPorts(config, filepath.Dir(config), seen, &numbers)
	slices.Sort(numbers)
	numbers = slices.Compact(numbers)
	out := make([]Port, len(numbers))
	for i, n := range numbers {
		out[i] = Port{Number: n, Proto: TCP}
	}
	return out
}

// collectSSHPorts reads the Port and ListenAddress lines of path and of the files its Include lines name, which are searched for
// relative to base; a file that cannot be read has no ports to add.
func collectSSHPorts(path, base string, seen map[string]bool, numbers *[]int) {
	if seen[path] {
		return
	}
	seen[path] = true
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		// sshd takes "Port=2222" and "Port = 2222" for "Port 2222".
		fields := strings.Fields(strings.Replace(line, "=", " ", 1))
		if len(fields) < 2 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		switch strings.ToLower(fields[0]) {
		case "port":
			if n, ok := sshPortNumber(fields[1]); ok {
				*numbers = append(*numbers, n)
			}
		case "listenaddress":
			if _, port, err := net.SplitHostPort(fields[1]); err == nil {
				if n, ok := sshPortNumber(port); ok {
					*numbers = append(*numbers, n)
				}
			}
		case "include":
			for _, pattern := range fields[1:] {
				if !filepath.IsAbs(pattern) {
					pattern = filepath.Join(base, pattern)
				}
				matches, _ := filepath.Glob(pattern)
				for _, m := range matches {
					collectSSHPorts(m, base, seen, numbers)
				}
			}
		}
	}
}

func sshPortNumber(s string) (int, bool) {
	n, err := strconv.Atoi(s)
	return n, err == nil && n >= 1 && n <= 65535
}

// sshProbeTimeout is how long a port is given to answer, and then to show a banner.
const sshProbeTimeout = 400 * time.Millisecond

// answersAsSSH reports whether something on this host greets a connection to port with an SSH banner, which is how sshd shows
// itself wherever it was told to listen: a socket unit, its command line, or a file that is not read here.
func answersAsSSH(port int) bool {
	dialer := net.Dialer{Timeout: sshProbeTimeout}
	for _, host := range []string{"127.0.0.1", "::1"} {
		conn, err := dialer.DialContext(context.Background(), "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
		if err != nil {
			continue
		}
		_ = conn.SetReadDeadline(time.Now().Add(sshProbeTimeout))
		banner := make([]byte, 4)
		n, _ := io.ReadFull(conn, banner)
		_ = conn.Close()
		if string(banner[:n]) == "SSH-" {
			return true
		}
	}
	return false
}
