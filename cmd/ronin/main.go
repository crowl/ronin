package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
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
)

func main() {
	var acpMode bool
	var resume bool
	var prompt string
	var workingDir string
	var modelFlag string
	var reasoningLevelFlag string

	flag.BoolVar(&acpMode, "acp", false, "Run an ACP server over stdin/stdout.")
	flag.BoolVar(&resume, "resume", false, "Load the active session for the working directory instead of starting a fresh session.")
	flag.StringVar(&prompt, "prompt", "", "Prompt to run without launching the TUI.")
	flag.StringVar(&workingDir, "working_dir", ".", "Working directory. Defaults to the current directory.")
	flag.StringVar(&modelFlag, "model", "", "Model to use as <provider>:<name>. Overrides the configured model.")
	flag.StringVar(&reasoningLevelFlag, "reasoning", "", "Reasoning level to use. Overrides the configured reasoning level.")

	flag.Parse()

	if openai.HasLocalOAuth() {
		if err := openai.SetupOAuth(); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "openai oauth setup failed: %v\n", err)
			os.Exit(1)
		}
	} else if apiKey := os.Getenv("OPENAI_API_KEY"); apiKey != "" {
		if err := openai.Setup(apiKey); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "openai LLM provider setup failed: %v\n", err)
			os.Exit(1)
		}
	}
	if apiKey := os.Getenv("GEMINI_API_KEY"); apiKey != "" {
		if err := google.Setup(apiKey); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "google LLM provider setup failed: %v\n", err)
			os.Exit(1)
		}
	}
	if apiKey := os.Getenv("ANTHROPIC_API_KEY"); apiKey != "" {
		if err := anthropic.Setup(apiKey); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "anthropic LLM provider setup failed: %v\n", err)
			os.Exit(1)
		}
	}

	if len(llm.Models()) == 0 {
		_, _ = fmt.Fprintf(os.Stderr, "no models available, please define at least one of OPENAI_API_KEY, GEMINI_API_KEY or ANTHROPIC_API_KEY env vars, or configure local ChatGPT/Codex OAuth credentials\n")
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

	skills, err := runtime.LoadSkills(skillsDir)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "failed to load skills: %v\n", err)
		os.Exit(1)
	}

	contextFiles, err := runtime.LoadContextFiles(configDir, workingDir)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "failed to load context files: %v\n", err)
		os.Exit(1)
	}

	readCache := fsutil.NewReadCache()
	mutationQueue := fsutil.NewMutationQueue()

	tools := []runtime.Tool{
		readfile.New(workingDir, readCache),
		editfile.New(workingDir, mutationQueue),
		writefile.New(workingDir, mutationQueue),
		shell.New(workingDir),
		gosemantic.NewFindSymbol(workingDir),
		gosemantic.NewOutlinePackage(workingDir),
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

	if err := runTUI(conv); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}

	os.Exit(0)
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

func runTUI(conv *runtime.Conversation) error {
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

	cmds = append(cmds, switchModelCmds...)
	cmds = append(cmds, switchReasoningLevelCmds...)
	cmds = append(cmds,
		tui.SwitchTheme{Name: "light", Theme: tui.LightTheme()},
		tui.SwitchTheme{Name: "dark", Theme: tui.DarkTheme()},
	)
	cmds = append(cmds, tui.Exit{})

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, os.Kill)
	defer cancel()

	if err := tui.Run(ctx, tui.Config{
		Conversation: conv,
		Commands:     cmds,
		Input:        os.Stdin,
		Output:       os.Stdout,
	}); err != nil {
		return fmt.Errorf("run tui: %w", err)
	}

	return nil
}
