package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/crowl/ronin/config"
	"github.com/crowl/ronin/internal/agentrun"
	"github.com/crowl/ronin/internal/agenttools"
	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/runtime"
	"github.com/crowl/ronin/session"
	"github.com/crowl/ronin/session/sqlite"
	"github.com/crowl/ronin/workflow"
)

// cliOptions holds parsed command line flags. args carries the positional
// arguments that follow the flags.
type cliOptions struct {
	version        bool
	resume         bool
	prompt         string
	workingDir     string
	model          string
	reasoningLevel string
	contextFiles   []string
	skills         []string
	mcp            []string
	args           []string
}

// parseFlags parses args (without the program name). Usage and parse errors
// are written to usageOutput; flag.ErrHelp is returned for -h and -help.
func parseFlags(args []string, usageOutput io.Writer) (cliOptions, error) {
	var opts cliOptions
	var contextFiles, skills, mcp repeatedFlag

	fs := flag.NewFlagSet("ronin", flag.ContinueOnError)
	fs.SetOutput(usageOutput)
	fs.BoolVar(&opts.version, "version", false, "Print the Ronin version.")
	fs.BoolVar(&opts.resume, "resume", false, "Load the active session for the working directory instead of starting a fresh session.")
	fs.StringVar(&opts.prompt, "prompt", "", "Prompt to run without launching the TUI.")
	fs.StringVar(&opts.workingDir, "working_dir", ".", "Working directory. Defaults to the current directory.")
	fs.StringVar(&opts.model, "model", "", "Model to use as <provider>:<name>. Overrides the configured model.")
	fs.StringVar(&opts.reasoningLevel, "reasoning", "", "Reasoning level to use. Overrides the configured reasoning level.")
	fs.Var(&contextFiles, "context-file", "Context file to include in prompt mode. May be repeated.")
	fs.Var(&skills, "skill", "Skill name, skill directory, or SKILL.md path to include in prompt mode. May be repeated.")
	fs.Var(&mcp, "mcp", "Configured MCP server to activate. May be repeated; use all to activate every server.")

	if err := fs.Parse(args); err != nil {
		return cliOptions{}, err
	}
	opts.contextFiles = contextFiles
	opts.skills = skills
	opts.mcp = mcp
	opts.args = fs.Args()
	return opts, nil
}

// modelSelection is the resolved default model for a process, together with
// the settings it came from and whether command line flags overrode the
// configured model or reasoning level.
type modelSelection struct {
	settings            config.Settings
	model               llm.Model
	level               llm.ReasoningLevel
	modelOverridden     bool
	reasoningOverridden bool
}

// loadModelSelection loads configuration, registers providers when no models
// are registered yet, and resolves the model selected by configuration and
// the optional -model and -reasoning flags.
func loadModelSelection(modelFlag, reasoningFlag string) (modelSelection, error) {
	settings, err := config.Load()
	if err != nil {
		return modelSelection{}, fmt.Errorf("failed to load config: %w", err)
	}
	return selectModel(settings, modelFlag, reasoningFlag)
}

func selectModel(settings config.Settings, modelFlag, reasoningFlag string) (modelSelection, error) {
	if len(llm.Models()) == 0 {
		if err := setupProviders(settings); err != nil {
			return modelSelection{}, err
		}
	}
	if len(llm.Models()) == 0 {
		return modelSelection{}, errors.New("no models available; configure an enabled provider and set its API key environment variable")
	}

	sel := modelSelection{settings: settings}
	if modelFlag != "" {
		override, err := parseModelFlag(modelFlag)
		if err != nil {
			return modelSelection{}, err
		}
		sel.settings.Model = override
		sel.modelOverridden = true
	}
	if reasoningFlag != "" {
		sel.settings.ReasoningLevel = reasoningFlag
		sel.reasoningOverridden = true
	}

	var err error
	sel.model, sel.level, err = resolveModel(sel.settings)
	if err != nil {
		return modelSelection{}, err
	}
	return sel, nil
}

// startMCP validates the -mcp selections against configured servers and
// activates them. The caller owns the returned registry and must close it.
func startMCP(ctx context.Context, workingDir string, servers map[string]config.MCPServer, selections []string) (*mcpRegistry, error) {
	names, err := selectMCPServers(servers, selections)
	if err != nil {
		return nil, fmt.Errorf("invalid MCP selection: %w", err)
	}
	registry := newMCPRegistry(workingDir, servers)
	if err := activateMCPServers(ctx, registry, names); err != nil {
		return nil, errors.Join(fmt.Errorf("failed to initialize MCP servers: %w", err), registry.Close())
	}
	return registry, nil
}

// closeInto closes c and folds a close failure into *err so a caller whose
// primary work succeeded still reports the failure.
func closeInto(err *error, name string, c io.Closer) {
	if closeErr := c.Close(); closeErr != nil {
		*err = errors.Join(*err, fmt.Errorf("failed to close %s: %w", name, closeErr))
	}
}

func newWorkflowAgentFunc(sel modelSelection, workingDir string, tools *agenttools.Factory, mcp agentrun.MCPSource) workflow.AgentFunc {
	runner := agentrun.New(agentrun.Config{
		WorkingDir:     workingDir,
		Model:          sel.model,
		ReasoningLevel: sel.level,
		MaxTurns:       sel.settings.MaxTurns,
		MCP:            mcp,
		ResolveModel:   resolveWorkflowModel,
		Tools:          tools,
	})
	return runner.Run
}

// runWorkflowMode executes a Lua workflow script from the command line.
func runWorkflowMode(ctx context.Context, opts cliOptions, cmd workflowCommand, output io.Writer) (err error) {
	sel, err := loadModelSelection(opts.model, opts.reasoningLevel)
	if err != nil {
		return err
	}
	registry, err := startMCP(ctx, opts.workingDir, sel.settings.MCPServers, opts.mcp)
	if err != nil {
		return err
	}
	defer closeInto(&err, "MCP servers", registry)

	agent := newWorkflowAgentFunc(sel, opts.workingDir, agenttools.NewFactory(), registry)
	return runWorkflow(ctx, cmd.Script, opts.workingDir, cmd.Input, agent, output)
}

// promptContext is the instruction material included in the system prompt.
type promptContext struct {
	skills       []runtime.Skill
	contextFiles []runtime.ContextFile
}

// loadPromptContext loads skills and context files. Prompt mode includes only
// what the -skill and -context-file flags name; the TUI loads everything
// discoverable from the config and working directories.
func loadPromptContext(opts cliOptions, configDir, skillsDir string) (promptContext, error) {
	var pc promptContext
	var err error
	if opts.prompt != "" {
		pc.skills, err = loadExplicitSkills(skillsDir, opts.skills)
		if err != nil {
			return promptContext{}, fmt.Errorf("failed to load skills: %w", err)
		}
		pc.contextFiles, err = loadExplicitContextFiles(opts.contextFiles)
		if err != nil {
			return promptContext{}, fmt.Errorf("failed to load context files: %w", err)
		}
		return pc, nil
	}
	pc.skills, err = runtime.LoadSkills(skillsDir)
	if err != nil {
		return promptContext{}, fmt.Errorf("failed to load skills: %w", err)
	}
	pc.contextFiles, err = runtime.LoadContextFiles(configDir, opts.workingDir)
	if err != nil {
		return promptContext{}, fmt.Errorf("failed to load context files: %w", err)
	}
	return pc, nil
}

// runConversationMode serves a persistent conversation, either headless with
// -prompt or through the TUI.
func runConversationMode(ctx context.Context, opts cliOptions, output io.Writer) (err error) {
	if opts.prompt == "" && len(opts.mcp) != 0 {
		return errors.New("--mcp is only supported with --prompt or run; use /mcp:<name> in the TUI")
	}

	sel, err := loadModelSelection(opts.model, opts.reasoningLevel)
	if err != nil {
		return err
	}

	configDir, err := config.Dir()
	if err != nil {
		return fmt.Errorf("failed to resolve config directory: %w", err)
	}
	skillsDir, err := config.SkillsDir()
	if err != nil {
		return fmt.Errorf("failed to resolve skills directory: %w", err)
	}
	workflowsDir, err := config.WorkflowsDir()
	if err != nil {
		return fmt.Errorf("failed to resolve workflows directory: %w", err)
	}
	workflowCatalog, err := workflow.LoadCatalog(workflowsDir)
	if err != nil {
		return fmt.Errorf("failed to load workflows: %w", err)
	}
	pc, err := loadPromptContext(opts, configDir, skillsDir)
	if err != nil {
		return err
	}

	registry, err := startMCP(ctx, opts.workingDir, sel.settings.MCPServers, opts.mcp)
	if err != nil {
		return err
	}
	defer closeInto(&err, "MCP servers", registry)

	// One tool factory is shared by the conversation and workflow agents so
	// their file mutations are coordinated.
	toolFactory := agenttools.NewFactory()
	baseTools := toolFactory.New(opts.workingDir, false, false, nil)
	workflowAgent := newWorkflowAgentFunc(sel, opts.workingDir, toolFactory, registry)
	if len(workflowCatalog.Workflows()) > 0 {
		baseTools = append(baseTools, workflow.NewTool(workflowCatalog, opts.workingDir, workflowAgent))
	}
	tools := append(append([]runtime.Tool(nil), baseTools...), registry.Tools()...)

	systemPromptInput := runtime.SystemPromptInput{
		CWD:             opts.workingDir,
		Skills:          pc.skills,
		ContextFiles:    pc.contextFiles,
		MCPInstructions: registry.Instructions(),
	}
	systemPrompt, err := runtime.BuildSystemPrompt(systemPromptInput)
	if err != nil {
		return fmt.Errorf("failed to build system prompt: %w", err)
	}

	dataDir, err := config.EnsureDataDir()
	if err != nil {
		return fmt.Errorf("failed to initialize data directory: %w", err)
	}
	sessionStore, err := sqlite.Open(ctx, sqlite.StoreConfig{
		Path: filepath.Join(dataDir, "ronin.db"),
		Now:  time.Now,
	})
	if err != nil {
		return fmt.Errorf("failed to initialize session store: %w", err)
	}
	defer closeInto(&err, "session store", sessionStore)

	activeSession, messages, err := startupSession(ctx, sessionStore, opts.workingDir, session.Metadata{
		Model:          sel.settings.Model,
		ReasoningLevel: sel.settings.ReasoningLevel,
	}, opts.resume)
	if err != nil {
		return err
	}

	// Each conversation owns its own model client so per-session model and
	// reasoning switches stay isolated.
	sessionModel, sessionLevel, err := resolveSessionModel(activeSession, sel.model, sel.level, sel.modelOverridden, sel.reasoningOverridden)
	if err != nil {
		return fmt.Errorf("failed to initialize conversation: %w", err)
	}
	client, err := llm.LoadModelClient(sessionModel, sessionLevel)
	if err != nil {
		return fmt.Errorf("failed to initialize conversation: load model client: %w", err)
	}
	conv, err := runtime.NewConversation(runtime.ConversationConfig{
		CWD:          opts.workingDir,
		ModelClient:  client,
		Compactor:    &runtime.DefaultCompactor{},
		Tools:        tools,
		SystemPrompt: systemPrompt,
		MaxTurns:     sel.settings.MaxTurns,
		Now:          time.Now,
		SessionStore: sessionStore,
		Session:      activeSession,
		Messages:     messages,
		SessionCost:  activeSession.Cost,
	})
	if err != nil {
		return fmt.Errorf("failed to initialize conversation: %w", err)
	}

	if opts.prompt != "" {
		return runPrompt(ctx, conv, opts.prompt, output)
	}

	activator := &conversationMCPActivator{
		registry:          registry,
		conversation:      conv,
		baseTools:         baseTools,
		systemPromptInput: systemPromptInput,
	}
	return runTUI(ctx, conv, workflowCatalog, workflowAgent, activator, sel.settings.MCPServers)
}

func shutdownTelemetry(shutdown func(context.Context) error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := shutdown(ctx); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "telemetry shutdown incomplete")
	}
}
