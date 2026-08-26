package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/crowl/ronin/acp"
	"github.com/crowl/ronin/config"
	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/llm/anthropic"
	"github.com/crowl/ronin/llm/google"
	"github.com/crowl/ronin/llm/openai"
	"github.com/crowl/ronin/runtime"
	"github.com/crowl/ronin/session"
	"github.com/crowl/ronin/session/fs"
	"github.com/crowl/ronin/tool/editfile"
	"github.com/crowl/ronin/tool/fsutil"
	"github.com/crowl/ronin/tool/gosemantic"
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
	var versionFlag bool
	var acpMode bool
	var resume bool
	var prompt string
	var workingDir string
	var modelFlag string
	var reasoningLevelFlag string
	var contextFileFlags repeatedFlag
	var skillFlags repeatedFlag

	flag.BoolVar(&versionFlag, "version", false, "Print the Ronin version.")
	flag.BoolVar(&acpMode, "acp", false, "Run an ACP server over stdin/stdout.")
	flag.BoolVar(&resume, "resume", false, "Load the active session for the working directory instead of starting a fresh session.")
	flag.StringVar(&prompt, "prompt", "", "Prompt to run without launching the TUI.")
	flag.StringVar(&workingDir, "working_dir", ".", "Working directory. Defaults to the current directory.")
	flag.StringVar(&modelFlag, "model", "", "Model to use as <provider>:<name>. Overrides the configured model.")
	flag.StringVar(&reasoningLevelFlag, "reasoning", "", "Reasoning level to use. Overrides the configured reasoning level.")
	flag.Var(&contextFileFlags, "context-file", "Context file to include in prompt mode. May be repeated.")
	flag.Var(&skillFlags, "skill", "Skill name, skill directory, or SKILL.md path to include in prompt mode. May be repeated.")

	flag.Parse()

	if versionFlag {
		if err := writeVersion(os.Stdout); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "failed to write version: %v\n", err)
			os.Exit(1)
		}
		return
	}

	workflowCmd, workflowMode, err := parseWorkflowCommand(flag.Args(), os.Stdin)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if workflowMode {
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, os.Kill)
		defer cancel()

		if err := runWorkflow(ctx, workflowCmd.Script, workingDir, workflowCmd.Input, newWorkflowAgentFunc(workingDir, modelFlag, reasoningLevelFlag), os.Stdout); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "%v\n", err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	if err := setupProviders(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}

	if len(llm.Models()) == 0 {
		_, _ = fmt.Fprintf(os.Stderr, "no models available, please define at least one of OPENAI_API_KEY, GEMINI_API_KEY or ANTHROPIC_API_KEY\n")
		os.Exit(1)
	}

	settings, err := config.Load()
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}

	if modelFlag != "" {
		override, err := parseModelFlag(modelFlag)
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "%v\n", err)
			os.Exit(1)
		}
		settings.Model = override
	}
	if reasoningLevelFlag != "" {
		settings.ReasoningLevel = reasoningLevelFlag
	}

	model, level, err := resolveModel(settings)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}

	modelClient, err := llm.LoadModelClient(model, level)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "failed to load LLM model client: %v\n", err)
		os.Exit(1)
	}

	configDir, err := config.Dir()
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "failed to resolve config directory: %v\n", err)
		os.Exit(1)
	}

	skillsDir, err := config.SkillsDir()
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "failed to resolve skills directory: %v\n", err)
		os.Exit(1)
	}

	workflowsDir, err := config.WorkflowsDir()
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "failed to resolve workflows directory: %v\n", err)
		os.Exit(1)
	}
	workflowCatalog, err := workflow.LoadCatalog(workflowsDir)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "failed to load workflows: %v\n", err)
		os.Exit(1)
	}

	var skills []runtime.Skill
	var contextFiles []runtime.ContextFile

	if prompt != "" {
		skills, err = loadExplicitSkills(skillsDir, skillFlags)
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "failed to load skills: %v\n", err)
			os.Exit(1)
		}
		contextFiles, err = loadExplicitContextFiles(contextFileFlags)
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "failed to load context files: %v\n", err)
			os.Exit(1)
		}
	} else {
		skills, err = runtime.LoadSkills(skillsDir)
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "failed to load skills: %v\n", err)
			os.Exit(1)
		}

		contextFiles, err = runtime.LoadContextFiles(configDir, workingDir)
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "failed to load context files: %v\n", err)
			os.Exit(1)
		}
	}

	readCache := fsutil.NewReadCache()
	mutationQueue := fsutil.NewMutationQueue()

	tools := []runtime.Tool{
		readfile.New(workingDir, readCache),
		gosemantic.NewOutlinePackage(workingDir),
		gosemantic.NewFindSymbol(workingDir),
		gosemantic.NewNavigation(workingDir),
		editfile.New(workingDir, mutationQueue),
		writefile.New(workingDir, mutationQueue),
		shell.New(workingDir),
	}

	workflowAgent := newWorkflowAgentFunc(workingDir, modelFlag, reasoningLevelFlag)
	if len(workflowCatalog.Workflows()) > 0 {
		tools = append(tools, workflow.NewTool(workflowCatalog, workingDir, workflowAgent))
	}

	systemPrompt, err := runtime.BuildSystemPrompt(runtime.SystemPromptInput{
		CWD:          workingDir,
		Skills:       skills,
		ContextFiles: contextFiles,
	})
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "failed to build system prompt: %v\n", err)
		os.Exit(1)
	}

	compactor, err := runtime.NewDefaultCompactor(runtime.DefaultCompactorConfig{
		ModelClient: modelClient,
		Now:         func() time.Time { return time.Now() },
	})
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "failed to initialize compactor: %v\n", err)
		os.Exit(1)
	}

	sessionStore := fs.NewStore(fs.StoreConfig{
		Dir: configDir,
		Now: func() time.Time { return time.Now() },
	})

	summarizer, summarizationPolicy, err := toolOutputSummarization(settings)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "failed to initialize tool output summarization: %v\n", err)
		os.Exit(1)
	}

	// buildConversation creates a conversation for an existing session record.
	// Each conversation owns its own model client so per-session model and
	// reasoning switches stay isolated.
	buildConversation := func(activeSession session.Session) (*runtime.Conversation, error) {
		client, err := llm.LoadModelClient(model, level)
		if err != nil {
			return nil, fmt.Errorf("load model client: %w", err)
		}
		activeSession.Model = config.Model{Provider: model.Provider, Name: model.Name}
		activeSession.ReasoningLevel = string(level)
		return runtime.NewConversation(runtime.ConversationConfig{
			CWD:                           workingDir,
			ModelClient:                   client,
			Compactor:                     compactor,
			ToolOutputSummarizer:          summarizer,
			ToolOutputSummarizationPolicy: summarizationPolicy,
			Tools:                         tools,
			SystemPrompt:                  systemPrompt,
			MaxTurns:                      settings.MaxTurns,
			Now:                           func() time.Time { return time.Now() },
			SessionStore:                  sessionStore,
			Session:                       activeSession,
		})
	}

	if acpMode {
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, os.Kill)
		defer cancel()

		newConversation := func() (acp.Conversation, error) {
			activeSession, err := sessionStore.Create(workingDir, session.Metadata{
				Model:          settings.Model,
				ReasoningLevel: settings.ReasoningLevel,
			})
			if err != nil {
				return nil, fmt.Errorf("create session: %w", err)
			}
			conv, err := buildConversation(activeSession)
			if err != nil {
				return nil, err
			}
			return conv, nil
		}

		loadConversation := func(sessionID string) (acp.Conversation, bool, error) {
			activeSession, ok, err := sessionStore.Load(sessionID)
			if err != nil {
				return nil, false, fmt.Errorf("load session: %w", err)
			}
			if !ok {
				return nil, false, nil
			}
			if !sameWorkingDir(activeSession.WorkingDir, workingDir) {
				return nil, false, nil
			}
			conv, err := buildConversation(activeSession)
			if err != nil {
				return nil, false, err
			}
			return conv, true, nil
		}

		absWorkingDir, err := filepath.Abs(workingDir)
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "resolve working directory: %v\n", err)
			os.Exit(1)
		}

		listSessions := func(cwd string) ([]acp.SessionSummary, error) {
			if cwd != "" && !sameWorkingDir(cwd, absWorkingDir) {
				return nil, nil
			}
			refs, err := sessionStore.List(absWorkingDir)
			if err != nil {
				return nil, err
			}
			summaries := make([]acp.SessionSummary, 0, len(refs))
			for _, ref := range refs {
				summaries = append(summaries, acp.SessionSummary{
					SessionID: ref.ID,
					CWD:       absWorkingDir,
					Title:     ref.Title,
					UpdatedAt: ref.UpdatedAt,
				})
			}
			return summaries, nil
		}

		if err := acp.Serve(ctx, acp.Config{
			NewConversation:  newConversation,
			LoadConversation: loadConversation,
			DeleteSession:    sessionStore.Delete,
			ListSessions:     listSessions,
			CWD:              workingDir,
			Input:            os.Stdin,
			Output:           os.Stdout,
			Log:              os.Stderr,
		}); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "%v\n", err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	activeSession, err := startupSession(sessionStore, workingDir, session.Metadata{
		Model:          settings.Model,
		ReasoningLevel: settings.ReasoningLevel,
	}, resume)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}

	conv, err := buildConversation(activeSession)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "failed to initialize conversation: %v\n", err)
		os.Exit(1)
	}

	if prompt != "" {
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, os.Kill)
		defer cancel()

		if err := runPrompt(ctx, conv, prompt, os.Stdout); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "%v\n", err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	if err := runTUI(conv, workflowCatalog, workflowAgent); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}

	os.Exit(0)
}

func setupProviders() error {
	if baseURL, ok := os.LookupEnv("OPENAI_BASE_URL"); ok {
		apiKey := os.Getenv("OPENAI_API_KEY")
		if apiKey == "" {
			return fmt.Errorf("OPENAI_API_KEY is required when OPENAI_BASE_URL is set")
		}
		if err := openai.SetupWithBaseURL(apiKey, baseURL); err != nil {
			return fmt.Errorf("openai LLM provider setup failed: %w", err)
		}
	} else if apiKey := os.Getenv("OPENAI_API_KEY"); apiKey != "" {
		if err := openai.Setup(apiKey); err != nil {
			return fmt.Errorf("openai LLM provider setup failed: %w", err)
		}
	}
	if baseURL, ok := os.LookupEnv("GEMINI_BASE_URL"); ok {
		apiKey := os.Getenv("GEMINI_API_KEY")
		if apiKey == "" {
			return fmt.Errorf("GEMINI_API_KEY is required when GEMINI_BASE_URL is set")
		}
		if err := google.SetupWithBaseURL(apiKey, baseURL); err != nil {
			return fmt.Errorf("google LLM provider setup failed: %w", err)
		}
	} else if apiKey := os.Getenv("GEMINI_API_KEY"); apiKey != "" {
		if err := google.Setup(apiKey); err != nil {
			return fmt.Errorf("google LLM provider setup failed: %w", err)
		}
	}
	if baseURL, ok := os.LookupEnv("ANTHROPIC_BASE_URL"); ok {
		apiKey := os.Getenv("ANTHROPIC_API_KEY")
		if apiKey == "" {
			return fmt.Errorf("ANTHROPIC_API_KEY is required when ANTHROPIC_BASE_URL is set")
		}
		if err := anthropic.SetupWithBaseURL(apiKey, baseURL); err != nil {
			return fmt.Errorf("anthropic LLM provider setup failed: %w", err)
		}
	} else if apiKey := os.Getenv("ANTHROPIC_API_KEY"); apiKey != "" {
		if err := anthropic.Setup(apiKey); err != nil {
			return fmt.Errorf("anthropic LLM provider setup failed: %w", err)
		}
	}
	return nil
}

func newWorkflowAgentFunc(workingDir, modelFlag, reasoningLevelFlag string) workflow.AgentFunc {
	var initOnce sync.Once
	var initErr error
	var settings config.Settings
	var defaultModel llm.Model
	var defaultLevel llm.ReasoningLevel
	var tools []runtime.Tool
	var systemPrompt string
	var compactor runtime.Compactor
	var summarizer runtime.ToolOutputSummarizer
	var summarizationPolicy runtime.ToolOutputSummarizationPolicy

	init := func() {
		if len(llm.Models()) == 0 {
			initErr = fmt.Errorf("no models available, please define at least one of OPENAI_API_KEY, GEMINI_API_KEY or ANTHROPIC_API_KEY")
			return
		}

		settings, initErr = config.Load()
		if initErr != nil {
			initErr = fmt.Errorf("failed to load config: %w", initErr)
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
		tools = []runtime.Tool{
			readfile.New(workingDir, readCache),
			gosemantic.NewOutlinePackage(workingDir),
			gosemantic.NewFindSymbol(workingDir),
			gosemantic.NewNavigation(workingDir),
			editfile.New(workingDir, mutationQueue),
			writefile.New(workingDir, mutationQueue),
			shell.New(workingDir),
		}

		systemPrompt, err = runtime.BuildSystemPrompt(runtime.SystemPromptInput{CWD: workingDir})
		if err != nil {
			initErr = fmt.Errorf("failed to build system prompt: %w", err)
			return
		}

		compactor, err = runtime.NewDefaultCompactor(runtime.DefaultCompactorConfig{
			ModelClient: defaultClient,
			Now:         func() time.Time { return time.Now() },
		})
		if err != nil {
			initErr = fmt.Errorf("failed to initialize compactor: %w", err)
			return
		}

		summarizer, summarizationPolicy, err = toolOutputSummarization(settings)
		if err != nil {
			initErr = fmt.Errorf("failed to initialize tool output summarization: %w", err)
			return
		}
	}

	return func(ctx context.Context, req workflow.AgentRequest) (workflow.AgentResult, error) {
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

		agentSystemPrompt := systemPrompt
		if strings.TrimSpace(req.System) != "" {
			agentSystemPrompt += "\n\nWorkflow agent instructions:\n" + strings.TrimSpace(req.System)
		}

		client, err := llm.LoadModelClient(agentModel, agentLevel)
		if err != nil {
			return workflow.AgentResult{}, fmt.Errorf("load model client: %w", err)
		}
		conv, err := runtime.NewConversation(runtime.ConversationConfig{
			CWD:                           workingDir,
			ModelClient:                   client,
			Compactor:                     compactor,
			ToolOutputSummarizer:          summarizer,
			ToolOutputSummarizationPolicy: summarizationPolicy,
			Tools:                         tools,
			SystemPrompt:                  agentSystemPrompt,
			MaxTurns:                      settings.MaxTurns,
			Now:                           func() time.Time { return time.Now() },
			Session:                       session.Session{WorkingDir: workingDir},
		})
		if err != nil {
			return workflow.AgentResult{}, err
		}
		text, err := runAgent(ctx, conv, req.Prompt, req.Progress)
		if err != nil {
			return workflow.AgentResult{}, err
		}
		return workflow.AgentResult{Text: text}, nil
	}
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

	var doneErr *workflow.DoneError
	if errors.As(err, &doneErr) {
		if doneErr.Message != "" {
			_, _ = fmt.Fprintln(output, doneErr.Message)
		}
		return nil
	}

	return err
}

func toolOutputSummarization(settings config.Settings) (runtime.ToolOutputSummarizer, runtime.ToolOutputSummarizationPolicy, error) {
	policy := runtime.DefaultToolOutputSummarizationPolicy()
	cfg := settings.ToolOutputSummarization
	policy.Enabled = cfg.Enabled
	if cfg.MinBytes > 0 {
		policy.MinBytes = cfg.MinBytes
	}
	if cfg.MaxSummaryTokens > 0 {
		policy.MaxSummaryTokens = cfg.MaxSummaryTokens
	}
	policy.SummarizeErrors = cfg.SummarizeErrors
	if cfg.ExcludedTools != nil {
		policy.ExcludedTools = make(map[string]bool, len(cfg.ExcludedTools))
		for _, name := range cfg.ExcludedTools {
			if name != "" {
				policy.ExcludedTools[name] = true
			}
		}
	}

	if !policy.Enabled {
		return nil, policy, nil
	}

	model := llm.Model{Provider: cfg.Model.Provider, Name: cfg.Model.Name}
	summaryClient, err := llm.LoadModelClient(model, llm.ReasoningLevelOff)
	if err != nil {
		return nil, policy, fmt.Errorf("load summary model client: %w", err)
	}
	summarizer, err := runtime.NewDefaultToolOutputSummarizer(runtime.DefaultToolOutputSummarizerConfig{
		ModelClient: summaryClient,
		Now:         func() time.Time { return time.Now() },
	})
	if err != nil {
		return nil, policy, err
	}
	return summarizer, policy, nil
}

func startupSession(store session.Store, workingDir string, metadata session.Metadata, resume bool) (session.Session, error) {
	if resume {
		activeSession, ok, err := store.LoadActive(workingDir)
		if err != nil {
			return session.Session{}, fmt.Errorf("failed to load session: %w", err)
		}
		if ok {
			return activeSession, nil
		}
	}

	activeSession, err := store.Create(workingDir, metadata)
	if err != nil {
		return session.Session{}, fmt.Errorf("failed to create session: %w", err)
	}
	return activeSession, nil
}

func sameWorkingDir(a, b string) bool {
	absA, err := filepath.Abs(a)
	if err != nil {
		return false
	}
	absB, err := filepath.Abs(b)
	if err != nil {
		return false
	}
	return filepath.Clean(absA) == filepath.Clean(absB)
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

func resolveModel(settings config.Settings) (llm.Model, llm.ReasoningLevel, error) {
	level := llm.ReasoningLevel(settings.ReasoningLevel)
	if !llm.IsValidReasoningLevel(level) {
		return llm.Model{}, "", fmt.Errorf("unknown reasoning level %q in config", settings.ReasoningLevel)
	}

	for _, model := range llm.Models() {
		if model.Provider == settings.Model.Provider && model.Name == settings.Model.Name {
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

func runTUI(conv *runtime.Conversation, catalog *workflow.Catalog, agent workflow.AgentFunc) error {
	models := llm.Models()

	switchModelCmds := make([]tui.Command, len(models))
	for i, model := range models {
		switchModelCmds[i] = tui.SwitchModel{
			Model: model,
		}
	}

	levels := llm.ReasoningLevels()

	switchReasoningLevelCmds := make([]tui.Command, len(levels))
	for i, level := range levels {
		switchReasoningLevelCmds[i] = tui.SwitchReasoningLevel{
			Level: level,
		}
	}

	cmds := []tui.Command{
		tui.StartNewConversation{},
		tui.CompactConversation{},
	}
	for _, item := range catalog.Workflows() {
		cmds = append(cmds, tui.InvokeWorkflow{Workflow: item})
	}

	cmds = append(cmds, switchModelCmds...)
	cmds = append(cmds, switchReasoningLevelCmds...)
	cmds = append(cmds, tui.Exit{})

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, os.Kill)
	defer cancel()

	if err := tui.Run(ctx, tui.Config{
		Conversation:   conv,
		WorkflowRunner: workflowRunner{workingDir: conv.CWD(), agent: agent},
		Commands:       cmds,
		Input:          os.Stdin,
		Output:         os.Stdout,
	}); err != nil {
		return fmt.Errorf("run tui: %w", err)
	}

	return nil
}
