package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
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
	var workingDir string

	flag.StringVar(&workingDir, "working_dir", ".", "Working directory. Defaults to the current directory.")

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

	if err := runTUI(agt); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}

	os.Exit(0)
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
		Theme:    tui.DefaultTheme(),
	}); err != nil {
		return fmt.Errorf("run tui: %w", err)
	}

	return nil
}
