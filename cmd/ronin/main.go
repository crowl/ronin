package main

import (
	"context"
	"encoding/json"
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
	"sync"
	"time"

	"github.com/crowl/ronin/config"
	"github.com/crowl/ronin/jsonschema"
	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/llm/anthropic"
	"github.com/crowl/ronin/llm/google"
	"github.com/crowl/ronin/llm/openai"
	"github.com/crowl/ronin/mcp"
	"github.com/crowl/ronin/runtime"
	"github.com/crowl/ronin/session"
	"github.com/crowl/ronin/session/sqlite"
	"github.com/crowl/ronin/tool/editfile"
	"github.com/crowl/ronin/tool/fsutil"
	"github.com/crowl/ronin/tool/readfile"
	"github.com/crowl/ronin/tool/shell"
	"github.com/crowl/ronin/tool/writefile"
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

func run() (exitCode int) {
	var versionFlag bool
	var resume bool
	var prompt string
	var workingDir string
	var modelFlag string
	var reasoningLevelFlag string
	var contextFileFlags repeatedFlag
	var skillFlags repeatedFlag
	var mcpFlags repeatedFlag

	flag.BoolVar(&versionFlag, "version", false, "Print the Ronin version.")
	flag.BoolVar(&resume, "resume", false, "Load the active session for the working directory instead of starting a fresh session.")
	flag.StringVar(&prompt, "prompt", "", "Prompt to run without launching the TUI.")
	flag.StringVar(&workingDir, "working_dir", ".", "Working directory. Defaults to the current directory.")
	flag.StringVar(&modelFlag, "model", "", "Model to use as <provider>:<name>. Overrides the configured model.")
	flag.StringVar(&reasoningLevelFlag, "reasoning", "", "Reasoning level to use. Overrides the configured reasoning level.")
	flag.Var(&contextFileFlags, "context-file", "Context file to include in prompt mode. May be repeated.")
	flag.Var(&skillFlags, "skill", "Skill name, skill directory, or SKILL.md path to include in prompt mode. May be repeated.")
	flag.Var(&mcpFlags, "mcp", "Configured MCP server to activate. May be repeated; use all to activate every server.")

	flag.Parse()

	if versionFlag {
		if err := writeVersion(os.Stdout); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "failed to write version: %v\n", err)
			return 1
		}
		return 0
	}

	workflowCmd, workflowMode, err := parseWorkflowCommand(flag.Args(), os.Stdin)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		return 1
	}

	if workflowMode {
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, os.Kill)
		defer cancel()

		settings, err := config.Load()
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
			return 1
		}
		mcpNames, err := selectMCPServers(settings.MCPServers, mcpFlags)
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "invalid MCP selection: %v\n", err)
			return 1
		}
		mcpRegistry := newMCPRegistry(workingDir, settings.MCPServers)
		if err := activateMCPServers(ctx, mcpRegistry, mcpNames); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "failed to initialize MCP servers: %v\n", err)
			_ = mcpRegistry.Close()
			return 1
		}
		defer func() {
			if err := mcpRegistry.Close(); err != nil {
				_, _ = fmt.Fprintf(os.Stderr, "failed to close MCP servers: %v\n", err)
				if exitCode == 0 {
					exitCode = 1
				}
			}
		}()

		workflowAgent, closeWorkflowAgent := newWorkflowAgentFunc(workingDir, modelFlag, reasoningLevelFlag, mcpRegistry)
		defer func() { _ = closeWorkflowAgent() }()
		if err := runWorkflow(ctx, workflowCmd.Script, workingDir, workflowCmd.Input, workflowAgent, os.Stdout); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "%v\n", err)
			return 1
		}
		return 0
	}

	settings, err := config.Load()
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		return 1
	}

	if err := setupProviders(settings); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}

	if len(llm.Models()) == 0 {
		_, _ = fmt.Fprintf(os.Stderr, "no models available; configure an enabled provider and set its API key environment variable\n")
		return 1
	}

	if modelFlag != "" {
		override, err := parseModelFlag(modelFlag)
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "%v\n", err)
			return 1
		}
		settings.Model = override
	}
	if reasoningLevelFlag != "" {
		settings.ReasoningLevel = reasoningLevelFlag
	}

	model, level, err := resolveModel(settings)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}

	configDir, err := config.Dir()
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "failed to resolve config directory: %v\n", err)
		return 1
	}

	skillsDir, err := config.SkillsDir()
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "failed to resolve skills directory: %v\n", err)
		return 1
	}

	workflowsDir, err := config.WorkflowsDir()
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "failed to resolve workflows directory: %v\n", err)
		return 1
	}
	workflowCatalog, err := workflow.LoadCatalog(workflowsDir)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "failed to load workflows: %v\n", err)
		return 1
	}

	var skills []runtime.Skill
	var contextFiles []runtime.ContextFile

	if prompt != "" {
		skills, err = loadExplicitSkills(skillsDir, skillFlags)
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "failed to load skills: %v\n", err)
			return 1
		}
		contextFiles, err = loadExplicitContextFiles(contextFileFlags)
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "failed to load context files: %v\n", err)
			return 1
		}
	} else {
		skills, err = runtime.LoadSkills(skillsDir)
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "failed to load skills: %v\n", err)
			return 1
		}

		contextFiles, err = runtime.LoadContextFiles(configDir, workingDir)
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "failed to load context files: %v\n", err)
			return 1
		}
	}

	mcpNames, err := selectMCPServers(settings.MCPServers, mcpFlags)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "invalid MCP selection: %v\n", err)
		return 1
	}
	if prompt == "" && len(mcpFlags) != 0 {
		_, _ = fmt.Fprintln(os.Stderr, "--mcp is only supported with --prompt or run; use /mcp:<name> in the TUI")
		return 1
	}

	mcpRegistry := newMCPRegistry(workingDir, settings.MCPServers)
	if err := activateMCPServers(context.Background(), mcpRegistry, mcpNames); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "failed to initialize MCP servers: %v\n", err)
		_ = mcpRegistry.Close()
		return 1
	}
	defer func() {
		if err := mcpRegistry.Close(); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "failed to close MCP servers: %v\n", err)
			if exitCode == 0 {
				exitCode = 1
			}
		}
	}()

	readCache := fsutil.NewReadCache()
	mutationQueue := fsutil.NewMutationQueue()

	baseTools := []runtime.Tool{
		readfile.New(workingDir, readCache),
		editfile.New(workingDir, mutationQueue),
		writefile.New(workingDir, mutationQueue),
		shell.New(workingDir),
	}

	workflowAgent, _ := newWorkflowAgentFunc(workingDir, modelFlag, reasoningLevelFlag, mcpRegistry)
	if len(workflowCatalog.Workflows()) > 0 {
		baseTools = append(baseTools, workflow.NewTool(workflowCatalog, workingDir, workflowAgent))
	}
	tools := append([]runtime.Tool(nil), baseTools...)
	tools = append(tools, mcpRegistry.Tools()...)

	systemPromptInput := runtime.SystemPromptInput{
		CWD:             workingDir,
		Skills:          skills,
		ContextFiles:    contextFiles,
		MCPInstructions: mcpRegistry.Instructions(),
	}
	systemPrompt, err := runtime.BuildSystemPrompt(systemPromptInput)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "failed to build system prompt: %v\n", err)
		return 1
	}

	dataDir, err := config.EnsureDataDir()
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "failed to initialize data directory: %v\n", err)
		return 1
	}
	sessionStore, err := sqlite.Open(context.Background(), sqlite.StoreConfig{
		Path: filepath.Join(dataDir, "ronin.db"),
		Now:  time.Now,
	})
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "failed to initialize session store: %v\n", err)
		return 1
	}
	defer func() {
		if err := sessionStore.Close(); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "failed to close session store: %v\n", err)
			if exitCode == 0 {
				exitCode = 1
			}
		}
	}()

	// buildConversation creates a conversation for an existing session record.
	// Each conversation owns its own model client so per-session model and
	// reasoning switches stay isolated.
	buildConversation := func(activeSession session.Session, messages []llm.Message) (*runtime.Conversation, error) {
		sessionModel, sessionLevel, err := resolveSessionModel(
			activeSession,
			model,
			level,
			modelFlag != "",
			reasoningLevelFlag != "",
		)
		if err != nil {
			return nil, err
		}
		client, err := llm.LoadModelClient(sessionModel, sessionLevel)
		if err != nil {
			return nil, fmt.Errorf("load model client: %w", err)
		}
		compactor, err := runtime.NewDefaultCompactor(runtime.DefaultCompactorConfig{
			ModelClient: client,
			Now:         time.Now,
		})
		if err != nil {
			return nil, fmt.Errorf("initialize compactor: %w", err)
		}
		return runtime.NewConversation(runtime.ConversationConfig{
			CWD:          workingDir,
			ModelClient:  client,
			Compactor:    compactor,
			Tools:        tools,
			SystemPrompt: systemPrompt,
			MaxTurns:     settings.MaxTurns,
			Now:          func() time.Time { return time.Now() },
			SessionStore: sessionStore,
			Session:      activeSession,
			Messages:     messages,
			SessionCost:  activeSession.Cost,
		})
	}

	activeSession, messages, err := startupSession(context.Background(), sessionStore, workingDir, session.Metadata{
		Model:          settings.Model,
		ReasoningLevel: settings.ReasoningLevel,
	}, resume)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}

	conv, err := buildConversation(activeSession, messages)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "failed to initialize conversation: %v\n", err)
		return 1
	}

	if prompt != "" {
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, os.Kill)
		defer cancel()

		if err := runPrompt(ctx, conv, prompt, os.Stdout); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "%v\n", err)
			return 1
		}
		return 0
	}

	activator := &conversationMCPActivator{
		registry:          mcpRegistry,
		conversation:      conv,
		baseTools:         baseTools,
		systemPromptInput: systemPromptInput,
	}
	if err := runTUI(conv, workflowCatalog, workflowAgent, activator, settings.MCPServers); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}

	return 0
}

type mcpSource interface {
	Tools() []runtime.Tool
	Instructions() []runtime.MCPInstruction
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

func newWorkflowAgentFunc(workingDir, modelFlag, reasoningLevelFlag string, mcpTools mcpSource) (workflow.AgentFunc, func() error) {
	var initOnce sync.Once
	var initErr error
	var settings config.Settings
	var defaultModel llm.Model
	var defaultLevel llm.ReasoningLevel
	var defaultTools []runtime.Tool
	var compactor runtime.Compactor

	init := func() {
		settings, initErr = config.Load()
		if initErr != nil {
			initErr = fmt.Errorf("failed to load config: %w", initErr)
			return
		}
		if len(llm.Models()) == 0 {
			if initErr = setupProviders(settings); initErr != nil {
				return
			}
		}
		if len(llm.Models()) == 0 {
			initErr = fmt.Errorf("no models available; configure an enabled provider and set its API key environment variable")
			return
		}
		if modelFlag != "" {
			override, err := parseModelFlag(modelFlag)
			if err != nil {
				initErr = err
				return
			}
			settings.Model = override
		}
		if reasoningLevelFlag != "" {
			settings.ReasoningLevel = reasoningLevelFlag
		}

		defaultModel, defaultLevel, initErr = resolveModel(settings)
		if initErr != nil {
			return
		}
		defaultClient, err := llm.LoadModelClient(defaultModel, defaultLevel)
		if err != nil {
			initErr = fmt.Errorf("failed to load LLM model client: %w", err)
			return
		}

		readCache := fsutil.NewReadCache()
		mutationQueue := fsutil.NewMutationQueue()
		defaultTools = []runtime.Tool{
			readfile.New(workingDir, readCache),
			editfile.New(workingDir, mutationQueue),
			writefile.New(workingDir, mutationQueue),
			shell.New(workingDir),
		}

		compactor, err = runtime.NewDefaultCompactor(runtime.DefaultCompactorConfig{
			ModelClient: defaultClient,
			Now:         func() time.Time { return time.Now() },
		})
		if err != nil {
			initErr = fmt.Errorf("failed to initialize compactor: %w", err)
			return
		}
	}

	agent := func(ctx context.Context, req workflow.AgentRequest) (workflow.AgentResult, error) {
		initOnce.Do(init)
		if initErr != nil {
			return workflow.AgentResult{}, initErr
		}

		agentModel := defaultModel
		if req.Model.Provider != "" || req.Model.Name != "" {
			resolved, err := resolveWorkflowModel(req.Model)
			if err != nil {
				return workflow.AgentResult{}, err
			}
			agentModel = resolved
		}

		agentLevel := defaultLevel
		if req.ReasoningLevel != "" {
			agentLevel = req.ReasoningLevel
		}
		if !agentModel.SupportsReasoning(agentLevel) {
			return workflow.AgentResult{}, fmt.Errorf("reasoning level %q is not supported by model %s", agentLevel, agentModel)
		}

		agentWorkingDir := workingDir
		agentTools := workflowAgentTools(agentWorkingDir, req.ReadOnly, defaultTools, mcpTools.Tools())
		var agentMCPInstructions []runtime.MCPInstruction
		if !req.ReadOnly {
			agentMCPInstructions = mcpTools.Instructions()
		}
		agentSystemPrompt, err := runtime.BuildSystemPrompt(runtime.SystemPromptInput{
			CWD:             workingDir,
			MCPInstructions: agentMCPInstructions,
		})
		if err != nil {
			return workflow.AgentResult{}, fmt.Errorf("build MCP system prompt: %w", err)
		}
		if req.Workspace != "" {
			agentWorkingDir = req.Workspace
			if req.ReadOnly {
				agentTools = workflowAgentTools(agentWorkingDir, true, nil, nil)
			} else {
				readCache := fsutil.NewReadCache()
				mutationQueue := fsutil.NewMutationQueue()
				agentTools = []runtime.Tool{
					readfile.New(agentWorkingDir, readCache),
					editfile.New(agentWorkingDir, mutationQueue),
					writefile.New(agentWorkingDir, mutationQueue),
					shell.NewWithPolicy(agentWorkingDir, workflow.WorkspaceShellPolicy{}),
				}
			}
			var err error
			agentSystemPrompt, err = runtime.BuildSystemPrompt(runtime.SystemPromptInput{CWD: agentWorkingDir})
			if err != nil {
				return workflow.AgentResult{}, fmt.Errorf("build workspace system prompt: %w", err)
			}
		}
		if req.ReadOnly {
			agentSystemPrompt += "\n\nThis agent is read-only. Do not modify files, run commands, or otherwise change repository or external state."
		}
		if strings.TrimSpace(req.System) != "" {
			agentSystemPrompt += "\n\nWorkflow agent instructions:\n" + strings.TrimSpace(req.System)
		}

		client, err := llm.LoadModelClient(agentModel, agentLevel)
		if err != nil {
			return workflow.AgentResult{}, fmt.Errorf("load model client: %w", err)
		}
		if req.OutputSchema != nil {
			if err := validateWorkflowAgentOutputSchema(client, req.OutputSchema); err != nil {
				return workflow.AgentResult{}, fmt.Errorf("validate structured workflow agent output schema before running agent: %w", err)
			}
		}
		conv, err := runtime.NewConversation(runtime.ConversationConfig{
			CWD:          agentWorkingDir,
			ModelClient:  client,
			Compactor:    compactor,
			Tools:        agentTools,
			SystemPrompt: agentSystemPrompt,
			MaxTurns:     settings.MaxTurns,
			Now:          func() time.Time { return time.Now() },
			Session:      session.Session{WorkingDir: agentWorkingDir},
		})
		if err != nil {
			return workflow.AgentResult{}, err
		}
		text, err := runAgent(ctx, conv, req.Prompt, req.Progress)
		if err != nil {
			return workflow.AgentResult{}, err
		}
		result := workflow.AgentResult{Text: text}
		if req.OutputSchema != nil {
			raw, err := structureWorkflowAgentOutput(ctx, client, text, req.OutputSchema)
			if err != nil {
				return workflow.AgentResult{}, fmt.Errorf("generate structured workflow agent output: %w", err)
			}
			result.Output = raw
		}
		return result, nil
	}
	closeAgent := func() error { return nil }
	return agent, closeAgent
}

func workflowAgentTools(workingDir string, readOnly bool, defaultTools, mcpTools []runtime.Tool) []runtime.Tool {
	if readOnly {
		return []runtime.Tool{readfile.New(workingDir, fsutil.NewReadCache())}
	}
	tools := append([]runtime.Tool(nil), defaultTools...)
	return append(tools, mcpTools...)
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

func startupSession(ctx context.Context, store session.Store, workingDir string, metadata session.Metadata, resume bool) (session.Session, []llm.Message, error) {
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

func validateWorkflowAgentOutputSchema(client llm.ModelClient, schema *jsonschema.Schema) error {
	if validator, ok := client.(llm.StructuredOutputSchemaValidator); ok {
		return validator.ValidateStructuredOutputSchema(schema)
	}
	return nil
}

const maxStructuredOutputCorrectionAttempts = 2
const maxInvalidStructuredOutputBytes = 4096

func structureWorkflowAgentOutput(ctx context.Context, client llm.ModelClient, report string, schema *jsonschema.Schema) (json.RawMessage, error) {
	prompt := report
	var lastOutput json.RawMessage
	var lastValidationErr error

	for attempt := 0; attempt <= maxStructuredOutputCorrectionAttempts; attempt++ {
		raw, err := client.PredictNextStructured(ctx, llm.PredictNextStructuredRequest{
			SystemPrompt: "Convert the supplied agent report into JSON matching the requested schema. Preserve its decisions exactly and do not add new work. Return substantive values from the report, never schema examples or placeholders.",
			Messages: []llm.Message{llm.UserMessage{
				Timestamp: time.Now(),
				Text:      prompt,
			}},
			Schema: schema,
		})
		if err != nil {
			return nil, err
		}
		if err := jsonschema.Validate(schema, raw); err == nil {
			return raw, nil
		} else {
			lastOutput = append(lastOutput[:0], raw...)
			lastValidationErr = err
		}
		if attempt < maxStructuredOutputCorrectionAttempts {
			prompt = structuredOutputCorrectionPrompt(report, raw, lastValidationErr)
		}
	}

	return nil, fmt.Errorf(
		"structured conversion remained invalid after %d correction attempts: %v; invalid output: %s; original agent report (the agent was not rerun): %s",
		maxStructuredOutputCorrectionAttempts,
		lastValidationErr,
		boundedStructuredOutput(lastOutput),
		boundedStructuredOutput([]byte(report)),
	)
}

func structuredOutputCorrectionPrompt(report string, invalid json.RawMessage, validationErr error) string {
	return "Original agent report (authoritative; preserve its decisions):\n\n" + report +
		"\n\nThe prior JSON conversion was invalid:\n\n" + boundedStructuredOutput(invalid) +
		"\n\nValidation failures:\n\n" + validationErr.Error() +
		"\n\nCorrect only the JSON conversion. Use substantive values from the original report; do not use placeholder values such as `string`, and do not add new work."
}

func boundedStructuredOutput(raw []byte) string {
	if len(raw) <= maxInvalidStructuredOutputBytes {
		return string(raw)
	}
	return string(raw[:maxInvalidStructuredOutputBytes]) + fmt.Sprintf("... [truncated %d bytes]", len(raw)-maxInvalidStructuredOutputBytes)
}

type prompter interface {
	Prompt(context.Context, string) (<-chan runtime.Event, <-chan error)
}

func runAgent(ctx context.Context, conv prompter, prompt string, progress func(workflow.AgentEvent)) (string, error) {
	var output strings.Builder
	events, errs := conv.Prompt(ctx, prompt)
	for event := range events {
		switch event := event.(type) {
		case runtime.AssistantThinkingDeltaReceived:
			if progress != nil {
				progress(workflow.AgentThinkingDelta{Text: event.Text})
			}
		case runtime.AssistantMessageDeltaReceived:
			output.WriteString(event.Text)
			if progress != nil {
				progress(workflow.AgentTextDelta{Text: event.Text})
			}
		case runtime.ToolExecutionStarted:
			if progress != nil {
				progress(workflow.AgentToolStarted{ID: event.CallID, Title: event.CallTitle})
			}
		case runtime.ToolExecutionOutputDeltaReceived:
			if progress != nil {
				progress(workflow.AgentToolOutput{ID: event.CallID, Artifact: event.Artifact})
			}
		case runtime.ToolExecutionResultReceived:
			if progress != nil {
				for _, artifact := range event.Artifacts {
					progress(workflow.AgentToolOutput{ID: event.CallID, Artifact: artifact})
				}
			}
		case runtime.ToolExecutionFailed:
			if progress != nil {
				progress(workflow.AgentToolFailed{ID: event.CallID, Error: event.Error.Error()})
			}
		case runtime.ToolExecutionEnded:
			if progress != nil {
				progress(workflow.AgentToolEnded{ID: event.CallID})
			}
		}
	}
	if err, ok := <-errs; ok && err != nil {
		return "", err
	}
	return output.String(), nil
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

func runTUI(conv *runtime.Conversation, catalog *workflow.Catalog, agent workflow.AgentFunc, activator tui.MCPActivator, mcpServers map[string]config.MCPServer) error {
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

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, os.Kill)
	defer cancel()

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
