package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/crowl/ronin/config"
	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/runtime"
	"github.com/crowl/ronin/session"
	"github.com/crowl/ronin/tui"
	"github.com/crowl/ronin/workflow"
)

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
