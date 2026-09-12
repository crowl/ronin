package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/crowl/ronin/config"
	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/llm/anthropic"
	"github.com/crowl/ronin/llm/google"
	"github.com/crowl/ronin/llm/openai"
	"github.com/crowl/ronin/mcp"
	"github.com/crowl/ronin/runtime"
	"github.com/crowl/ronin/session"
	"github.com/crowl/ronin/telemetry"
	"github.com/crowl/ronin/tui"
	"github.com/crowl/ronin/workflow"
)

var version = "dev"

func writeVersion(output io.Writer) error {
	_, err := fmt.Fprintf(output, "ronin %s\n", version)
	return err
}

func main() {
	os.Exit(run())
}

func run() int {
	opts, err := parseFlags(os.Args[1:], os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		return 2
	}

	if opts.version {
		if err := writeVersion(os.Stdout); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "failed to write version: %v\n", err)
			return 1
		}
		return 0
	}

	// Telemetry outlives the signal context so a final flush can complete
	// after cancellation.
	shutdown, telemetryErr := telemetry.Setup(context.Background())
	if telemetryErr != nil {
		_, _ = fmt.Fprintln(os.Stderr, "telemetry initialization failed; continuing without telemetry")
	} else {
		defer shutdownTelemetry(shutdown)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), shutdownSignals()...)
	defer cancel()

	workflowCmd, workflowMode, err := parseWorkflowCommand(opts.args, os.Stdin)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if workflowMode {
		err = runWorkflowMode(ctx, opts, workflowCmd, os.Stdout)
	} else {
		err = runConversationMode(ctx, opts, os.Stdout)
	}
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

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

func setupProviders(settings ...config.Settings) error {
	if len(settings) > 1 {
		return fmt.Errorf("setup providers accepts at most one settings value")
	}
	var providerSettings config.Settings
	if len(settings) > 0 {
		providerSettings = settings[0]
	} else {
		var err error
		providerSettings, err = config.Load()
		if err != nil {
			return err
		}
	}
	providerNames := make([]string, 0, len(providerSettings.Providers))
	for providerName := range providerSettings.Providers {
		providerNames = append(providerNames, providerName)
	}
	slices.Sort(providerNames)
	for _, providerName := range providerNames {
		provider := providerSettings.Providers[providerName]
		if provider.Enabled.Set && !provider.Enabled.Value {
			continue
		}
		baseURL := provider.BaseURL
		baseURLWasOverridden := false
		baseURLEnv := provider.BaseURLEnv
		if baseURLEnv == "" {
			baseURLEnv = "base_url"
		}
		if provider.BaseURLEnv != "" {
			if value, ok := os.LookupEnv(provider.BaseURLEnv); ok {
				baseURL = value
				baseURLWasOverridden = true
			}
		}
		apiKey := os.Getenv(provider.APIKeyEnv)
		if apiKey == "" {
			if baseURLWasOverridden {
				return fmt.Errorf("%s is required when %s is set", provider.APIKeyEnv, provider.BaseURLEnv)
			}
			continue
		}
		if err := validateRuntimeProviderURL(baseURL); err != nil {
			return fmt.Errorf("%s: invalid %s: %w", providerName, baseURLEnv, err)
		}

		models := configuredModels(providerName, provider.Models)
		if len(models) == 0 {
			continue
		}
		var err error
		switch provider.Adapter {
		case "openai":
			err = openai.SetupModels(apiKey, baseURL, models)
		case "anthropic":
			err = anthropic.SetupModels(apiKey, baseURL, models)
		case "google":
			err = google.SetupModels(apiKey, baseURL, models)
		default:
			return fmt.Errorf("provider %q uses unsupported adapter %q", providerName, provider.Adapter)
		}
		if err != nil {
			return fmt.Errorf("%s LLM provider setup failed: %w", providerName, err)
		}
	}
	return nil
}

func validateRuntimeProviderURL(value string) error {
	parsed, err := url.Parse(value)
	if err != nil {
		return err
	}
	if !parsed.IsAbs() || parsed.Hostname() == "" {
		return fmt.Errorf("URL must be absolute and include a host")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("URL scheme must be http or https")
	}
	return nil
}

func configuredModels(provider string, configured map[string]config.ProviderModel) []llm.Model {
	models := make([]llm.Model, 0, len(configured))
	for name, model := range configured {
		if model.Enabled.Set && !model.Enabled.Value {
			continue
		}
		levels := make([]llm.ReasoningLevel, 0, len(model.Reasoning.Levels))
		for _, level := range model.Reasoning.Levels {
			levels = append(levels, llm.ReasoningLevel(level))
		}
		models = append(models, llm.Model{
			Provider:           provider,
			Name:               name,
			ContextWindow:      model.ContextWindow.Value,
			ReasoningMode:      llm.ReasoningMode(model.Reasoning.Mode),
			SupportedReasoning: llm.NewReasoningSet(levels...),
			Pricing: llm.ModelPricing{
				Input:         model.Pricing.Input.Value,
				Output:        model.Pricing.Output.Value,
				CacheRead:     model.Pricing.CacheRead.Value,
				CacheWrite:    model.Pricing.CacheWrite.Value,
				HasInput:      model.Pricing.Input.Set,
				HasOutput:     model.Pricing.Output.Set,
				HasCacheRead:  model.Pricing.CacheRead.Set,
				HasCacheWrite: model.Pricing.CacheWrite.Set,
			},
		})
	}
	slices.SortFunc(models, func(a, b llm.Model) int { return strings.Compare(a.Name, b.Name) })
	return models
}

type workflowCommand struct {
	Script string
	Input  string
}

func parseWorkflowCommand(args []string, stdin io.Reader) (workflowCommand, bool, error) {
	if len(args) == 0 || args[0] != "run" {
		return workflowCommand{}, false, nil
	}

	command, err := parseWorkflowArgs(args[1:])
	if err != nil {
		return workflowCommand{}, true, err
	}
	if command.Script == "" {
		return workflowCommand{}, true, fmt.Errorf("usage: ronin run <script.lua> [<input> | --input <file> | -]")
	}
	if command.InputFile != "" {
		input, err := readWorkflowInputFile(command.InputFile)
		if err != nil {
			return workflowCommand{}, true, err
		}
		return workflowCommand{Script: command.Script, Input: input}, true, nil
	}
	if command.ReadStdin {
		if stdin == nil {
			return workflowCommand{}, true, fmt.Errorf("read workflow stdin: input is unavailable")
		}
		input, err := readWorkflowInput(stdin, "stdin")
		if err != nil {
			return workflowCommand{}, true, err
		}
		return workflowCommand{Script: command.Script, Input: input}, true, nil
	}
	return workflowCommand{Script: command.Script, Input: command.InlineInput}, true, nil
}

const maxWorkflowInputBytes int64 = 1 << 20

type workflowArgs struct {
	Script      string
	InlineInput string
	InputFile   string
	ReadStdin   bool
}

func parseWorkflowArgs(args []string) (workflowArgs, error) {
	var parsed workflowArgs
	var inlineInputSet bool
	var inputFileSet bool
	options := true

	if len(args) == 0 || args[0] == "" || strings.HasPrefix(args[0], "-") {
		return workflowArgs{}, fmt.Errorf("usage: ronin run <script.lua> [<input> | --input <file> | -]")
	}
	parsed.Script = args[0]

	for i := 1; i < len(args); i++ {
		arg := args[i]

		if options && arg == "--" {
			if inlineInputSet || inputFileSet || parsed.ReadStdin || i+1 >= len(args) {
				return workflowArgs{}, fmt.Errorf("unexpected workflow argument %q", arg)
			}
			options = false
			continue
		}
		if options && arg == "--input" {
			if inlineInputSet || inputFileSet || parsed.ReadStdin {
				return workflowArgs{}, fmt.Errorf("workflow input sources conflict: use one of inline text, --input <file>, or -")
			}
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
				return workflowArgs{}, fmt.Errorf("workflow --input requires a file path")
			}
			i++
			parsed.InputFile = args[i]
			if parsed.InputFile == "" {
				return workflowArgs{}, fmt.Errorf("workflow --input requires a file path")
			}
			inputFileSet = true
			continue
		}
		if options && strings.HasPrefix(arg, "--input=") {
			if inlineInputSet || inputFileSet || parsed.ReadStdin {
				return workflowArgs{}, fmt.Errorf("workflow input sources conflict: use one of inline text, --input <file>, or -")
			}
			parsed.InputFile = strings.TrimPrefix(arg, "--input=")
			if parsed.InputFile == "" {
				return workflowArgs{}, fmt.Errorf("workflow --input requires a file path")
			}
			inputFileSet = true
			continue
		}
		if options && strings.HasPrefix(arg, "-") && arg != "-" {
			return workflowArgs{}, fmt.Errorf("unknown workflow option %q", arg)
		}
		if inputFileSet || parsed.ReadStdin {
			return workflowArgs{}, fmt.Errorf("workflow input sources conflict: use one of inline text, --input <file>, or -")
		}
		if inlineInputSet {
			return workflowArgs{}, fmt.Errorf("unexpected workflow argument %q", arg)
		}
		if arg == "-" && options {
			parsed.ReadStdin = true
		} else {
			parsed.InlineInput = arg
			inlineInputSet = true
		}
	}

	return parsed, nil
}

func readWorkflowInputFile(path string) (input string, err error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("read workflow input file %q: %w", path, err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("read workflow input file %q: %w", path, closeErr)
		}
	}()

	info, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("read workflow input file %q: %w", path, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("workflow input file %q is a directory", path)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("workflow input file %q is not a regular file", path)
	}
	if info.Size() > maxWorkflowInputBytes {
		return "", fmt.Errorf("workflow input file %q exceeds the 1 MiB limit", path)
	}
	input, err = readWorkflowInput(file, fmt.Sprintf("input file %q", path))
	if err != nil {
		return "", err
	}
	return input, nil
}

func readWorkflowInput(reader io.Reader, source string) (string, error) {
	limited := io.LimitReader(reader, maxWorkflowInputBytes+1)
	content, err := io.ReadAll(limited)
	if err != nil {
		return "", fmt.Errorf("read workflow %s: %w", source, err)
	}
	if int64(len(content)) > maxWorkflowInputBytes {
		return "", fmt.Errorf("workflow %s exceeds the 1 MiB limit", source)
	}
	return string(content), nil
}

func runWorkflow(ctx context.Context, script, workingDir, input string, agent workflow.AgentFunc, output io.Writer) error {
	err := workflow.RunFileWithAgentInputInWorkingDir(ctx, script, workingDir, input, output, agent)
	if err == nil {
		return nil
	}

	if doneErr, ok := errors.AsType[*workflow.DoneError](err); ok {
		if doneErr.Message != "" {
			_, _ = fmt.Fprintln(output, doneErr.Message)
		}
		return nil
	}

	return err
}

func startupSession(ctx context.Context, store session.Store, workingDir string, metadata session.Metadata, resume bool) (session.Session, []session.Message, error) {
	if resume {
		activeSession, messages, ok, err := store.Latest(ctx, workingDir)
		if err != nil {
			return session.Session{}, nil, fmt.Errorf("failed to load session: %w", err)
		}
		if ok {
			return activeSession, messages, nil
		}
	}

	activeSession, err := store.Create(ctx, workingDir, metadata)
	if err != nil {
		return session.Session{}, nil, fmt.Errorf("failed to create session: %w", err)
	}
	activeSession.Cost.Available = true
	return activeSession, nil, nil
}

func parseModelFlag(value string) (config.Model, error) {
	provider, name, ok := strings.Cut(value, ":")
	if !ok {
		return config.Model{}, fmt.Errorf("invalid -model %q: want format <provider>:<name>", value)
	}
	if provider == "" || name == "" {
		return config.Model{}, fmt.Errorf("invalid -model %q: provider and name must not be empty", value)
	}
	return config.Model{Provider: provider, Name: name}, nil
}

func resolveSessionModel(activeSession session.Session, fallbackModel llm.Model, fallbackLevel llm.ReasoningLevel, modelOverridden, reasoningOverridden bool) (llm.Model, llm.ReasoningLevel, error) {
	settings := config.Settings{
		Model: config.Model{
			Provider: fallbackModel.Provider,
			Name:     fallbackModel.Name,
		},
		ReasoningLevel: string(fallbackLevel),
	}
	if !modelOverridden && activeSession.Model.Provider != "" && activeSession.Model.Name != "" {
		settings.Model = activeSession.Model
	}
	if !reasoningOverridden && activeSession.ReasoningLevel != "" {
		settings.ReasoningLevel = activeSession.ReasoningLevel
	}

	model, level, err := resolveModel(settings)
	if err != nil {
		return llm.Model{}, "", fmt.Errorf("resolve session model: %w", err)
	}
	return model, level, nil
}

func resolveModel(settings config.Settings) (llm.Model, llm.ReasoningLevel, error) {
	level := llm.ReasoningLevel(settings.ReasoningLevel)
	if !llm.IsValidReasoningLevel(level) {
		return llm.Model{}, "", fmt.Errorf("unknown reasoning level %q in config", settings.ReasoningLevel)
	}

	for _, model := range llm.Models() {
		if model.Provider == settings.Model.Provider && model.Name == settings.Model.Name {
			if !model.SupportsReasoning(level) {
				return llm.Model{}, "", fmt.Errorf("reasoning level %q is not supported by model %s", level, model)
			}
			return model, level, nil
		}
	}

	return llm.Model{}, "", fmt.Errorf("unknown model %q in config (is the provider's API key set?)", settings.Model.Provider+":"+settings.Model.Name)
}

func resolveWorkflowModel(requested llm.Model) (llm.Model, error) {
	for _, model := range llm.Models() {
		if model.Provider == requested.Provider && model.Name == requested.Name {
			return model, nil
		}
	}
	return llm.Model{}, fmt.Errorf("unknown ronin.run_agent model %q", requested.Provider+":"+requested.Name)
}

type repeatedFlag []string

func (f *repeatedFlag) String() string {
	if f == nil {
		return ""
	}
	return strings.Join(*f, ",")
}

func (f *repeatedFlag) Set(value string) error {
	*f = append(*f, value)
	return nil
}

func loadExplicitContextFiles(paths []string) ([]runtime.ContextFile, error) {
	files := make([]runtime.ContextFile, 0, len(paths))
	for _, path := range paths {
		clean, err := filepath.Abs(path)
		if err != nil {
			return nil, fmt.Errorf("resolve context file %q: %w", path, err)
		}
		clean = filepath.Clean(clean)

		info, err := os.Stat(clean)
		if err != nil {
			return nil, fmt.Errorf("stat context file %q: %w", clean, err)
		}
		if info.IsDir() {
			return nil, fmt.Errorf("context file %q is a directory", clean)
		}
		if info.Size() > runtime.MaxContextFileBytes {
			return nil, fmt.Errorf("context file %q exceeds %d bytes", clean, runtime.MaxContextFileBytes)
		}
		data, err := os.ReadFile(clean)
		if err != nil {
			return nil, fmt.Errorf("read context file %q: %w", clean, err)
		}
		files = append(files, runtime.ContextFile{Path: clean, Content: string(data)})
	}
	return files, nil
}

func loadExplicitSkills(skillsDir string, values []string) ([]runtime.Skill, error) {
	if len(values) == 0 {
		return nil, nil
	}

	available, err := runtime.LoadSkills(skillsDir)
	if err != nil {
		return nil, err
	}
	byName := make(map[string]runtime.Skill, len(available))
	for _, skill := range available {
		byName[skill.Name] = skill
	}

	skills := make([]runtime.Skill, 0, len(values))
	for _, value := range values {
		if skill, ok := byName[value]; ok {
			skills = append(skills, skill)
			continue
		}
		skill, err := loadExplicitSkillPath(value)
		if err != nil {
			return nil, err
		}
		skills = append(skills, skill)
	}
	return skills, nil
}

func loadExplicitSkillPath(value string) (runtime.Skill, error) {
	path := value
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		path = filepath.Join(path, "SKILL.md")
	} else if err != nil {
		return runtime.Skill{}, fmt.Errorf("load skill %q: not a configured skill name or readable path", value)
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		return runtime.Skill{}, fmt.Errorf("resolve skill %q: %w", value, err)
	}
	abs = filepath.Clean(abs)

	return runtime.LoadSkillFile(abs)
}

type prompter interface {
	Prompt(context.Context, string) (<-chan runtime.Event, <-chan error)
}

func runPrompt(ctx context.Context, conv prompter, prompt string, output io.Writer) error {
	events, errs := conv.Prompt(ctx, prompt)
	atLineStart := true

	writeText := func(text string) error {
		if text == "" {
			return nil
		}
		if _, err := io.WriteString(output, text); err != nil {
			return err
		}
		atLineStart = text[len(text)-1] == '\n'
		return nil
	}

	for event := range events {
		switch typedEvent := event.(type) {
		case runtime.AssistantMessageDeltaReceived:
			if err := writeText(typedEvent.Text); err != nil {
				return err
			}
		}
	}

	if !atLineStart {
		if _, err := io.WriteString(output, "\n"); err != nil {
			return err
		}
	}

	if err, ok := <-errs; ok && err != nil {
		return err
	}

	return nil
}

type workflowRunner struct {
	workingDir string
	agent      workflow.AgentFunc
}

func (r workflowRunner) Run(ctx context.Context, item workflow.Workflow, input string, emit func(workflow.Event)) workflow.Result {
	return workflow.Run(ctx, item, r.workingDir, input, r.agent, emit)
}

func runTUI(ctx context.Context, conv *runtime.Conversation, catalog *workflow.Catalog, agent workflow.AgentFunc, activator tui.MCPActivator, mcpServers map[string]config.MCPServer) error {
	models := llm.Models()

	cmds := []tui.Command{
		tui.StartNewConversation{},
		tui.RewindConversation{},
		tui.ForkConversation{},
		tui.CompactConversation{},
	}
	for _, item := range catalog.Workflows() {
		cmds = append(cmds, tui.InvokeWorkflow{Workflow: item})
	}
	mcpNames := make([]string, 0, len(mcpServers))
	for name := range mcpServers {
		mcpNames = append(mcpNames, name)
	}
	slices.Sort(mcpNames)
	for _, name := range mcpNames {
		cmds = append(cmds, tui.ActivateMCP{Name: name})
	}
	for _, model := range models {
		cmds = append(cmds, tui.SwitchModel{Model: model})
	}
	for _, level := range conv.Model().ReasoningLevels() {
		cmds = append(cmds, tui.SwitchReasoningLevel{Level: level})
	}
	cmds = append(cmds, tui.Exit{})

	if err := tui.Run(ctx, tui.Config{
		Conversation:   conv,
		WorkflowRunner: workflowRunner{workingDir: conv.CWD(), agent: agent},
		MCPActivator:   activator,
		Commands:       cmds,
		Input:          os.Stdin,
		Output:         os.Stdout,
	}); err != nil {
		return fmt.Errorf("run tui: %w", err)
	}

	return nil
}
