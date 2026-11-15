package main

import (
	"net"
	"strings"
	"testing"
)

func TestCommandHelpListsEncryptTokens(t *testing.T) {
	if help := commandHelp(); !strings.Contains(help, "encrypt-tokens") {
		t.Fatalf("command help omits encrypt-tokens:\n%s", help)
	}
}

func TestPanelListensOverPlainTCP(t *testing.T) {
	var lc net.ListenConfig
	if lc.MultipathTCP() {
		t.Fatal("a listener of the panel uses MPTCP: main.go lost its //go:debug multipathtcp=0 line")
	}
}
