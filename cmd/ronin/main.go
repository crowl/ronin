package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/crowl/ronin/agent"
	"github.com/crowl/ronin/llm"
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
	var cwd string
	var maxTurns int

	flag.StringVar(&cwd, "cwd", ".", "The cwd is used to run the application. The default is the current directory.")
	flag.IntVar(&maxTurns, "max_turns", 512, "")

	flag.Parse()

	if err := agent.EnsureConfigDir(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "failed to create config directory: %v\n", err)
		os.Exit(1)
	}

	configDir, err := agent.ConfigDir()
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "failed to get config directory: %v\n", err)
		os.Exit(1)
	}

	if err := openai.Setup(os.Getenv("OPENAI_API_KEY")); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "openai LLM provider setup failed: %v\n", err)
		os.Exit(1)
	}
	if err := google.Setup(os.Getenv("GEMINI_API_KEY")); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "google LLM provider setup failed: %v\n", err)
		os.Exit(1)
	}

	defaultLLM, err := llm.Load(openai.Gpt55, llm.ReasoningLevelMedium)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "failed to load default LLM: %v\n", err)
		os.Exit(1)
	}

	skillsDir, err := agent.SkillsDir()
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "failed to create directory: %v\n", err)
		os.Exit(1)
	}

	skills, err := agent.LoadSkills(skillsDir)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "failed to load skills: %v\n", err)
		os.Exit(1)
	}

	contextFiles, err := agent.LoadContextFiles(configDir, cwd)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "failed to load context files: %v\n", err)
		os.Exit(1)
	}

	readCache := fsutil.NewReadCache()
	mutationQueue := fsutil.NewMutationQueue()

	tools := []agent.Tool{
		readfile.New(cwd, readCache),
		editfile.New(cwd, mutationQueue),
		writefile.New(cwd, mutationQueue),
		shell.New(cwd),
	}

	systemPrompt, err := agent.BuildSystemPrompt(agent.SystemPromptInput{
		CWD:          cwd,
		Skills:       skills,
		ContextFiles: contextFiles,
	})

	compactor, err := agent.NewDefaultCompactor(agent.DefaultCompactorConfig{
		LLM: defaultLLM,
		Now: func() time.Time { return time.Now() },
	})
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "failed to initialize compactor: %v\n", err)
		os.Exit(1)
	}

	agt, err := agent.New(agent.Config{
		CWD:          cwd,
		LLM:          defaultLLM,
		Compactor:    compactor,
		ContextFiles: contextFiles,
		Skills:       skills,
		Tools:        tools,
		SystemPrompt: systemPrompt,
		MaxTurns:     maxTurns,
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
