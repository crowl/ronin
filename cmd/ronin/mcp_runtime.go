package main

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/crowl/ronin/config"
	"github.com/crowl/ronin/mcp"
	"github.com/crowl/ronin/runtime"
)

type mcpRegistry struct {
	workingDir string
	configured map[string]config.MCPServer
	clients    map[string]*mcp.Client
	order      []string
}

func newMCPRegistry(workingDir string, configured map[string]config.MCPServer) *mcpRegistry {
	return &mcpRegistry{
		workingDir: workingDir,
		configured: configured,
		clients:    make(map[string]*mcp.Client),
	}
}

func (r *mcpRegistry) Activate(ctx context.Context, name string) (bool, error) {
	client, active, err := r.connect(ctx, name)
	if err != nil || active {
		return false, err
	}
	r.add(name, client)
	return true, nil
}

func (r *mcpRegistry) connect(ctx context.Context, name string) (*mcp.Client, bool, error) {
	if _, ok := r.clients[name]; ok {
		return nil, true, nil
	}
	server, ok := r.configured[name]
	if !ok {
		return nil, false, fmt.Errorf("unknown MCP server %q", name)
	}
	client, err := connectMCPServers(ctx, r.workingDir, map[string]config.MCPServer{name: server})
	if err != nil {
		return nil, false, err
	}
	return client, false, nil
}

func (r *mcpRegistry) add(name string, client *mcp.Client) {
	r.clients[name] = client
	r.order = append(r.order, name)
}

func (r *mcpRegistry) Tools() []runtime.Tool {
	var tools []runtime.Tool
	for _, name := range r.order {
		tools = append(tools, r.clients[name].Tools()...)
	}
	return tools
}

func (r *mcpRegistry) Instructions() []runtime.MCPInstruction {
	var instructions []runtime.MCPInstruction
	for _, name := range r.order {
		instructions = append(instructions, r.clients[name].Instructions()...)
	}
	return instructions
}

func (r *mcpRegistry) Close() error {
	var errs []error
	for _, name := range slices.Backward(r.order) {
		if err := r.clients[name].Close(); err != nil {
			errs = append(errs, fmt.Errorf("close MCP server %q: %w", name, err))
		}
	}
	r.clients = make(map[string]*mcp.Client)
	r.order = nil
	return errors.Join(errs...)
}

type conversationMCPActivator struct {
	registry          *mcpRegistry
	conversation      *runtime.Conversation
	baseTools         []runtime.Tool
	systemPromptInput runtime.SystemPromptInput
}

func (a *conversationMCPActivator) ActivateMCP(ctx context.Context, name string) (bool, error) {
	client, active, err := a.registry.connect(ctx, name)
	if err != nil || active {
		return false, err
	}

	tools := append([]runtime.Tool(nil), a.baseTools...)
	tools = append(tools, a.registry.Tools()...)
	tools = append(tools, client.Tools()...)

	promptInput := a.systemPromptInput
	promptInput.MCPInstructions = append(a.registry.Instructions(), client.Instructions()...)
	systemPrompt, err := runtime.BuildSystemPrompt(promptInput)
	if err != nil {
		_ = client.Close()
		return false, fmt.Errorf("build system prompt: %w", err)
	}
	if err := a.conversation.SetToolsAndSystemPrompt(tools, systemPrompt); err != nil {
		_ = client.Close()
		return false, fmt.Errorf("update conversation tools: %w", err)
	}

	a.registry.add(name, client)
	return true, nil
}

func selectMCPServers(configured map[string]config.MCPServer, selections []string) ([]string, error) {
	if len(selections) == 0 {
		return nil, nil
	}

	selected := make(map[string]struct{}, len(selections))
	all := false
	for _, raw := range selections {
		name := strings.TrimSpace(raw)
		if name == "" {
			return nil, errors.New("--mcp requires a server name or all")
		}
		if name == "all" {
			all = true
			continue
		}
		if _, ok := configured[name]; !ok {
			return nil, fmt.Errorf("unknown MCP server %q", name)
		}
		selected[name] = struct{}{}
	}
	if all && len(selected) != 0 {
		return nil, errors.New("--mcp all cannot be combined with named MCP servers")
	}
	if all {
		for name := range configured {
			selected[name] = struct{}{}
		}
	}

	names := make([]string, 0, len(selected))
	for name := range selected {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

func activateMCPServers(ctx context.Context, registry *mcpRegistry, names []string) error {
	for _, name := range names {
		if _, err := registry.Activate(ctx, name); err != nil {
			return fmt.Errorf("activate MCP server %q: %w", name, err)
		}
	}
	return nil
}

func connectMCPServers(ctx context.Context, workingDir string, servers map[string]config.MCPServer) (*mcp.Client, error) {
	if len(servers) == 0 {
		return mcp.Connect(ctx, workingDir, nil)
	}

	logDir := ""
	for _, server := range servers {
		if strings.TrimSpace(server.Command) == "" {
			continue
		}
		dataDir, err := config.EnsureDataDir()
		if err != nil {
			return nil, fmt.Errorf("initialize MCP log directory: %w", err)
		}
		logDir = filepath.Join(dataDir, "logs", "mcp")
		if err := os.MkdirAll(logDir, 0o700); err != nil {
			return nil, fmt.Errorf("create MCP log directory %q: %w", logDir, err)
		}
		break
	}

	configs := make(map[string]mcp.ServerConfig, len(servers))
	for name, server := range servers {
		cfg := mcp.ServerConfig{
			Command: server.Command,
			Args:    append([]string(nil), server.Args...),
			Env:     cloneStrings(server.Env),
			URL:     server.URL,
		}
		if strings.TrimSpace(server.Command) != "" {
			cfg.LogPath = filepath.Join(logDir, name+".log")
		}
		configs[name] = cfg
	}
	return mcp.Connect(ctx, workingDir, configs)
}

func cloneStrings(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	clone := make(map[string]string, len(values))
	maps.Copy(clone, values)
	return clone
}
