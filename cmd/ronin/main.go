package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/crowl/ronin/agent"
	"github.com/crowl/ronin/config"
	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/llm/anthropic"
	"github.com/crowl/ronin/llm/google"
	"github.com/crowl/ronin/llm/openai"
	"github.com/crowl/ronin/tool/editfile"
	"github.com/crowl/ronin/tool/fsutil"
	"github.com/crowl/ronin/tool/readfile"
	"github.com/crowl/ronin/tool/shell"
	"github.com/crowl/ronin/tool/writefile"
	"github.com/crowl/ronin/tui"
)

func main() {
	var prompt string
	var workingDir string
	var modelFlag string
	var reasoningLevelFlag string

	flag.StringVar(&prompt, "prompt", "", "Prompt to run without launching the TUI.")
	flag.StringVar(&workingDir, "working_dir", ".", "Working directory. Defaults to the current directory.")
	flag.StringVar(&modelFlag, "model", "", "Model to use as <provider>:<name>. Overrides the configured model.")
	flag.StringVar(&reasoningLevelFlag, "reasoning", "", "Reasoning level to use. Overrides the configured reasoning level.")

	flag.Parse()

	if apiKey := os.Getenv("OPENAI_API_KEY"); apiKey != "" {
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

	defaultLLM, err := llm.Load(model, level)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "failed to load configured LLM: %v\n", err)
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

	skills, err := agent.LoadSkills(skillsDir)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "failed to load skills: %v\n", err)
		os.Exit(1)
	}

	contextFiles, err := agent.LoadContextFiles(configDir, workingDir)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "failed to load context files: %v\n", err)
		os.Exit(1)
	}

	readCache := fsutil.NewReadCache()
	mutationQueue := fsutil.NewMutationQueue()

	tools := []agent.Tool{
		readfile.New(workingDir, readCache),
		editfile.New(workingDir, mutationQueue),
		writefile.New(workingDir, mutationQueue),
		shell.New(workingDir),
	}

	systemPrompt, err := agent.BuildSystemPrompt(agent.SystemPromptInput{
		CWD:          workingDir,
		Skills:       skills,
		ContextFiles: contextFiles,
	})
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "failed to build system prompt: %v\n", err)
		os.Exit(1)
	}

	compactor, err := agent.NewDefaultCompactor(agent.DefaultCompactorConfig{
		LLM: defaultLLM,
		Now: func() time.Time { return time.Now() },
	})
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "failed to initialize compactor: %v\n", err)
		os.Exit(1)
	}

	agt, err := agent.New(agent.Config{
		CWD:          workingDir,
		LLM:          defaultLLM,
		Compactor:    compactor,
		Tools:        tools,
		SystemPrompt: systemPrompt,
		MaxTurns:     settings.MaxTurns,
		Now:          func() time.Time { return time.Now() },
	})
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "failed to initialize agent: %v\n", err)
		os.Exit(1)
	}

	if prompt != "" {
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, os.Kill)
		defer cancel()

		if err := runPrompt(ctx, agt, prompt, os.Stdout); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "%v\n", err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	if err := runTUI(agt); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}

	os.Exit(0)
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
	Prompt(context.Context, string) (<-chan agent.Event, <-chan error)
}

func runPrompt(ctx context.Context, agt prompter, prompt string, output io.Writer) error {
	events, errs := agt.Prompt(ctx, prompt)
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

	writeStatus := func(text string) error {
		if !atLineStart {
			if _, err := io.WriteString(output, "\n"); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintf(output, "%s\n", text); err != nil {
			return err
		}
		atLineStart = true
		return nil
	}

	for event := range events {
		switch typedEvent := event.(type) {
		case agent.AssistantMessageDeltaReceived:
			if err := writeText(typedEvent.Text); err != nil {
				return err
			}
		case agent.ToolExecutionStarted:
			if err := writeStatus("[tool] " + typedEvent.Tool.Name()); err != nil {
				return err
			}
		case agent.ToolExecutionFailed:
			if err := writeStatus(fmt.Sprintf("[tool error] %s: %v", typedEvent.Tool.Name(), typedEvent.Error)); err != nil {
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

func runTUI(agt *agent.Agent) error {
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
	cmds = append(cmds, tui.Exit{})

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, os.Kill)
	defer cancel()

	if err := tui.Run(ctx, tui.Config{
		Agent:    agt,
		Commands: cmds,
		Input:    os.Stdin,
		Output:   os.Stdout,
	}); err != nil {
		return fmt.Errorf("run tui: %w", err)
	}

	return nil
}
