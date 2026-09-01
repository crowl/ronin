package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/crowl/ronin/config"
)

func TestSelectMCPServers(t *testing.T) {
	configured := map[string]config.MCPServer{
		"gopls":  {Command: "gopls"},
		"github": {URL: "https://example.com/mcp"},
	}

	for _, test := range []struct {
		name       string
		selections []string
		want       []string
		wantErr    string
	}{
		{name: "none"},
		{name: "named", selections: []string{"gopls", "github", "gopls"}, want: []string{"github", "gopls"}},
		{name: "all", selections: []string{"all"}, want: []string{"github", "gopls"}},
		{name: "unknown", selections: []string{"missing"}, wantErr: "unknown MCP server"},
		{name: "all conflicts with named", selections: []string{"all", "gopls"}, wantErr: "cannot be combined"},
		{name: "empty", selections: []string{" "}, wantErr: "requires a server name"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := selectMCPServers(configured, test.selections)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("selectMCPServers() error = %v, want %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("selectMCPServers() error = %v", err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("selectMCPServers() = %#v, want %#v", got, test.want)
			}
		})
	}
}

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
