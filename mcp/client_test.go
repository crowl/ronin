package mcp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestConnectStdioServer(t *testing.T) {
	client, err := Connect(t.Context(), t.TempDir(), map[string]ServerConfig{
		"test": {
			Command: os.Args[0],
			Args:    []string{"-test.run=^TestMCPHelperProcess$"},
			Env:     map[string]string{"RONIN_MCP_HELPER": "1"},
		},
	})
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer client.Close()

	instructions := client.Instructions()
	if len(instructions) != 1 {
		t.Fatalf("Instructions() = %#v", instructions)
	}
	if instructions[0].Server != "test" || instructions[0].Content != "Use echo to repeat text." {
		t.Fatalf("Instructions()[0] = %#v", instructions[0])
	}
	if len(instructions[0].Tools) != 1 || instructions[0].Tools[0] != "echo" {
		t.Fatalf("Instructions()[0].Tools = %#v", instructions[0].Tools)
	}

	tools := client.Tools()
	if len(tools) != 1 || tools[0].Name() != "test__echo" {
		t.Fatalf("Tools() = %#v", tools)
	}
	if tools[0].Description() != "Echoes text" {
		t.Fatalf("Description() = %q", tools[0].Description())
	}

	schema, err := json.Marshal(tools[0].Parameters())
	if err != nil {
		t.Fatalf("marshal schema: %v", err)
	}
	var gotSchema map[string]any
	if err := json.Unmarshal(schema, &gotSchema); err != nil {
		t.Fatalf("decode schema: %v", err)
	}
	if gotSchema["type"] != "object" || gotSchema["additionalProperties"] != false {
		t.Fatalf("schema = %s", schema)
	}
	properties, ok := gotSchema["properties"].(map[string]any)
	if !ok || properties["text"] == nil {
		t.Fatalf("schema properties = %#v", gotSchema["properties"])
	}

	value, err := tools[0].Call(t.Context(), json.RawMessage(`{"text":"stdio"}`))
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	result := value.(Result)
	if len(result.Artifacts()) != 1 {
		t.Fatalf("artifacts = %#v", result.Artifacts())
	}
	data, err := json.Marshal(result)
	if err != nil || !containsJSONText(data, "stdio") {
		t.Fatalf("result = %s, error = %v", data, err)
	}
}

func TestBoundedWriter(t *testing.T) {
	var output bytes.Buffer
	writer := &boundedWriter{writer: &output, remaining: 5}

	for _, input := range []string{"abc", "def", "ignored"} {
		written, err := writer.Write([]byte(input))
		if err != nil {
			t.Fatalf("Write(%q) error = %v", input, err)
		}
		if written != len(input) {
			t.Fatalf("Write(%q) = %d, want %d", input, written, len(input))
		}
	}
	if got := output.String(); got != "abcde" {
		t.Fatalf("output = %q, want %q", got, "abcde")
	}
}

func TestStartSessionWritesBoundedServerLog(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "test.log")
	if err := os.WriteFile(logPath, []byte("old log contents"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	session, err := startSession(t.TempDir(), ServerConfig{
		Command: os.Args[0],
		Args:    []string{"-test.run=^TestMCPHelperProcess$"},
		Env: map[string]string{
			"RONIN_MCP_HELPER":        "1",
			"RONIN_MCP_HELPER_STDERR": "server log",
		},
		LogPath: logPath,
	})
	if err != nil {
		t.Fatalf("startSession() error = %v", err)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if got := string(log); got != "server log" {
		t.Fatalf("log = %q, want %q", got, "server log")
	}
	info, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("log permissions = %o, want 600", info.Mode().Perm())
	}
}

func TestMCPHelperProcess(t *testing.T) {
	if os.Getenv("RONIN_MCP_HELPER") != "1" {
		return
	}

	if stderr := os.Getenv("RONIN_MCP_HELPER_STDERR"); stderr != "" {
		_, _ = os.Stderr.WriteString(stderr)
	}

	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var request struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id,omitempty"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params,omitempty"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			os.Exit(2)
		}
		if len(request.ID) == 0 {
			continue
		}

		response := map[string]any{"jsonrpc": "2.0", "id": request.ID}
		switch request.Method {
		case "initialize":
			response["result"] = map[string]any{
				"protocolVersion": protocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]string{"name": "test", "version": "1"},
				"instructions":    "Use echo to repeat text.",
			}
		case "tools/list":
			response["result"] = map[string]any{"tools": []any{map[string]any{
				"name":        "echo",
				"description": "Echoes text",
				"inputSchema": json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"],"additionalProperties":false}`),
			}}}
		case "tools/call":
			var params struct {
				Arguments struct {
					Text string `json:"text"`
				} `json:"arguments"`
			}
			if err := json.Unmarshal(request.Params, &params); err != nil {
				os.Exit(2)
			}
			response["result"] = map[string]any{
				"content":           []any{map[string]string{"type": "text", "text": params.Arguments.Text}},
				"structuredContent": map[string]string{"echo": params.Arguments.Text},
			}
		default:
			response["error"] = map[string]any{"code": -32601, "message": "method not found"}
		}
		if err := encoder.Encode(response); err != nil {
			os.Exit(2)
		}
	}
	if scanner.Err() != nil {
		os.Exit(2)
	}
}

func TestValidateServer(t *testing.T) {
	for _, test := range []struct {
		name       string
		serverName string
		config     ServerConfig
		wantErr    bool
	}{
		{name: "valid", serverName: "go-tools", config: ServerConfig{Command: "gopls"}},
		{name: "invalid name", serverName: "go tools", config: ServerConfig{Command: "gopls"}, wantErr: true},
		{name: "empty command", serverName: "go", config: ServerConfig{}, wantErr: true},
		{name: "invalid env", serverName: "go", config: ServerConfig{Command: "gopls", Env: map[string]string{"BAD=KEY": "value"}}, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateServer(test.serverName, test.config)
			if (err != nil) != test.wantErr {
				t.Fatalf("validateServer() error = %v", err)
			}
		})
	}
}

func containsJSONText(data []byte, text string) bool {
	var value any
	if json.Unmarshal(data, &value) != nil {
		return false
	}
	return containsText(value, text)
}

func containsText(value any, text string) bool {
	switch value := value.(type) {
	case string:
		return value == text
	case []any:
		for _, item := range value {
			if containsText(item, text) {
				return true
			}
		}
	case map[string]any:
		for _, item := range value {
			if containsText(item, text) {
				return true
			}
		}
	}
	return false
}
