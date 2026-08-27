package main

import (
	"testing"

	"github.com/crowl/ronin/config"
)

func TestConnectMCPServersWithoutConfiguration(t *testing.T) {
	client, err := connectMCPServers(t.Context(), t.TempDir(), map[string]config.MCPServer{})
	if err != nil {
		t.Fatalf("connectMCPServers() error = %v", err)
	}
	defer client.Close()
	if len(client.Tools()) != 0 {
		t.Fatalf("Tools() = %#v", client.Tools())
	}
	if len(client.Instructions()) != 0 {
		t.Fatalf("Instructions() = %#v", client.Instructions())
	}
}
