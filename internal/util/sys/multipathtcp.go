package sys

import (
	"os"
	"strings"
)

const noMultipathTCP = "multipathtcp=0"

// DisableMultipathTCP makes the programs this process starts listen over plain TCP, as the process itself
// does through the go:debug line of its main package. A GODEBUG that already sets multipathtcp is the
// operator's and stays. It returns the line to log.
func DisableMultipathTCP() string {
	current := os.Getenv("GODEBUG")
	next := withoutMultipathTCP(current)
	if next == current {
		return "MPTCP listeners: GODEBUG already sets multipathtcp, left as it is"
	}
	if err := os.Setenv("GODEBUG", next); err != nil {
		return "MPTCP listeners: could not set GODEBUG: " + err.Error()
	}
	return "MPTCP listeners are off for this process and the programs it starts"
}

// withoutMultipathTCP adds multipathtcp=0 to a GODEBUG value that does not set the key.
func withoutMultipathTCP(godebug string) string {
	for _, setting := range strings.Split(godebug, ",") {
		if key, _, _ := strings.Cut(strings.TrimSpace(setting), "="); key == "multipathtcp" {
			return godebug
		}
	}
	if strings.TrimSpace(godebug) == "" {
		return noMultipathTCP
	}
	return godebug + "," + noMultipathTCP
}
