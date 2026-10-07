package sys

import "strings"

var virtualInterfacePrefixes = []string{
	"loopback", "docker", "br-", "veth", "virbr", "tun", "tap", "wg", "tailscale", "zt",
}

// IsVirtualInterface reports whether a network interface is loopback or a tunnel, bridge
// or container link, which carry bytes a physical interface counts as well.
func IsVirtualInterface(name string) bool {
	name = strings.ToLower(name)
	if name == "lo" || name == "lo0" {
		return true
	}
	for _, prefix := range virtualInterfacePrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}
