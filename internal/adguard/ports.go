package adguard

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/parser"
)

// checkPorts refuses a port that is no port, for the two that AdGuard Home listens on.
func checkPorts(webPort, dnsPort int) error {
	if webPort <= 0 || webPort > 65535 {
		return fmt.Errorf("invalid AdGuard Home web port %d", webPort)
	}
	if dnsPort <= 0 || dnsPort > 65535 {
		return fmt.Errorf("invalid AdGuard Home DNS port %d", dnsPort)
	}
	return nil
}

// SetPorts moves the web page and the plain DNS of AdGuard Home to these loopback ports, for a config whose ports
// something else came to hold. As with the account, AdGuard Home must not be running: it keeps its whole config in
// memory and rewrites the file, so an edit made underneath it is liable to be thrown away.
func SetPorts(webPort, dnsPort int) error {
	if err := checkPorts(webPort, dnsPort); err != nil {
		return err
	}
	raw, err := os.ReadFile(ConfigPath())
	if err != nil {
		return fmt.Errorf("cannot read %s: %w", ConfigPath(), err)
	}
	updated, err := replacePorts(raw, webPort, dnsPort)
	if err != nil {
		return err
	}
	if err := os.WriteFile(ConfigPath(), updated, 0o600); err != nil {
		return fmt.Errorf("cannot write %s: %w", ConfigPath(), err)
	}
	return nil
}

// replacePorts swaps the two ports in the parsed document, so that every other setting comes back out byte for byte.
func replacePorts(raw []byte, webPort, dnsPort int) ([]byte, error) {
	file, err := parser.ParseBytes(raw, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("cannot parse %s: %w", ConfigPath(), err)
	}
	for _, edit := range []struct{ path, value string }{
		{"$.http.address", strconv.Quote(fmt.Sprintf("127.0.0.1:%d", webPort))},
		{"$.dns.port", strconv.Itoa(dnsPort)},
	} {
		path, err := yaml.PathString(edit.path)
		if err != nil {
			return nil, err
		}
		if err := path.ReplaceWithReader(file, strings.NewReader(edit.value)); err != nil {
			return nil, fmt.Errorf("cannot move %s: %w", edit.path, err)
		}
	}
	out := file.String()
	if !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	// A path that the config does not have is no error of the replacement, so what came out is read back.
	var moved configView
	if err := yaml.Unmarshal([]byte(out), &moved); err != nil {
		return nil, fmt.Errorf("cannot parse the moved %s: %w", ConfigPath(), err)
	}
	if moved.HTTP.Address != fmt.Sprintf("127.0.0.1:%d", webPort) {
		return nil, fmt.Errorf("%s has no http.address to move", ConfigPath())
	}
	if moved.DNS.Port != dnsPort {
		return nil, fmt.Errorf("%s has no dns.port to move", ConfigPath())
	}
	return []byte(out), nil
}
