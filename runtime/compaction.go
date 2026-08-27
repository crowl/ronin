package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"text/template"
	"time"

	_ "embed"

	"github.com/crowl/ronin/jsonschema"
	"github.com/crowl/ronin/llm"
)

var (
	//go:embed prompt_compaction_summary.tmpl
	compactionSummaryPromptTemplateText string

	//go:embed compacted_context.tmpl
	compactedContextTemplateText string

	compactionSummaryPromptTemplate = template.Must(template.New("prompt.compact").Parse(compactionSummaryPromptTemplateText))
	compactedContextTemplate        = template.Must(template.New("compacted_context").Parse(compactedContextTemplateText))
)

const defaultCompactKeepMessages = 12

type DefaultCompactorConfig struct {
	SessionID   string
	ModelClient llm.ModelClient
	Now         func() time.Time
}

func NewDefaultCompactor(cfg DefaultCompactorConfig) (*DefaultCompactor, error) {
	return &DefaultCompactor{
		sessionID:   cfg.SessionID,
		modelClient: cfg.ModelClient,
		now:         cfg.Now,
	}, nil
}

type DefaultCompactor struct {
	sessionID   string
	modelClient llm.ModelClient
	now         func() time.Time
}

type compactionSummary struct {
	CurrentGoal         string   `json:"current_goal" jsonschema:"The user's current goal or task. Use an empty string if unknown."`
	UserPreferences     []string `json:"user_preferences" jsonschema:"User preferences and standing instructions that remain relevant."`
	Decisions           []string `json:"decisions" jsonschema:"Important decisions made during the compacted conversation."`
	FilesAndCodeState   []string `json:"files_and_code_state" jsonschema:"Relevant files, code changes, and repository state."`
	TestsAndToolResults []string `json:"tests_and_tool_results" jsonschema:"Important commands, test results, tool results, and errors."`
	OpenTasks           []string `json:"open_tasks" jsonschema:"Known unfinished work and next steps."`
	Recovery            []string `json:"recovery" jsonschema:"Information needed to safely resume after compaction."`
}

func (c *DefaultCompactor) Compact(ctx context.Context, msgs []llm.Message) ([]llm.Message, error) {
	if len(msgs) <= 0 {
		return nil, fmt.Errorf("not enough safely compactable messages: have %d", len(msgs))
	}

	start := safeCompactionStart(msgs, defaultCompactKeepMessages)
	if start <= 0 {
		return nil, fmt.Errorf("not enough safely compactable messages: have %d", len(msgs))
	}
	if start >= len(msgs) {
		return nil, fmt.Errorf("no recent messages would remain after compaction")
	}

	older := append([]llm.Message(nil), msgs[:start]...)
	recent := append([]llm.Message(nil), msgs[start:]...)
	factSheet := buildCompactionFactSheet(older, "")
	summary, err := c.generateCompactionSummary(ctx, factSheet)
	if err != nil {
		return nil, err
	}

	messageText, err := renderCompactedContext(renderCompactionSummary(summary, factSheet))
	if err != nil {
		return nil, err
	}

	msg := llm.UserMessage{
		Timestamp: c.nowTime(),
		Text:      messageText,
	}

	compacted := make([]llm.Message, 0, 1+len(recent))
	compacted = append(compacted, msg)
	compacted = append(compacted, recent...)
	return compacted, nil
}

func (c *DefaultCompactor) generateCompactionSummary(ctx context.Context, factSheet string) (compactionSummary, error) {
	var prompt bytes.Buffer
	if err := compactionSummaryPromptTemplate.Execute(&prompt, struct{ FactSheet string }{FactSheet: factSheet}); err != nil {
		return compactionSummary{}, fmt.Errorf("render compaction prompt: %w", err)
	}

	msg := llm.UserMessage{
		Timestamp: c.nowTime(),
		Text:      prompt.String(),
	}

	raw, err := c.modelClient.PredictNextStructured(ctx, llm.PredictNextStructuredRequest{
		SystemPrompt: "You compact coding conversation context into precise structured JSON.",
		Messages:     []llm.Message{msg},
		Schema:       jsonschema.FromType[compactionSummary](),
	})
	if err != nil {
		return compactionSummary{}, fmt.Errorf("generate structured compaction summary: %w", err)
	}

	var summary compactionSummary
	if err := json.Unmarshal(raw, &summary); err != nil {
		return compactionSummary{}, fmt.Errorf("decode structured compaction summary: %w", err)
	}
	if summary.empty() {
		return compactionSummary{}, fmt.Errorf("compaction produced empty summary")
	}

	return summary, nil
}

func (c *DefaultCompactor) nowTime() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

func safeCompactionStart(messages []llm.Message, keepRecent int) int {
	if keepRecent <= 0 {
		keepRecent = defaultCompactKeepMessages
	}
	start := max(len(messages)-keepRecent, 0)
	for {
		seenCalls := map[string]bool{}
		missingCallID := ""
		missingIndex := -1
		for i := start; i < len(messages); i++ {
			for _, callID := range messageToolCallIDs(messages[i]) {
				seenCalls[callID] = true
			}
			toolResultMsg, ok := messages[i].(llm.ToolOutputMessage)
			if !ok {
				continue
			}
			if toolResultMsg.ToolCallID != "" && !seenCalls[toolResultMsg.ToolCallID] {
				missingCallID = toolResultMsg.ToolCallID
				missingIndex = i
				break
			}
		}
		if missingCallID == "" {
			return start
		}
		found := -1
		for i := start - 1; i >= 0; i-- {
			if slices.Contains(messageToolCallIDs(messages[i]), missingCallID) {
				found = i
			}
			if found >= 0 {
				break
			}
		}
		if found < 0 {
			start = missingIndex + 1
			if start >= len(messages) {
				return len(messages)
			}
			continue
		}
		start = found
	}
}

func messageToolCallIDs(msg llm.Message) []string {
	assistantMsg, ok := msg.(llm.AssistantMessage)
	if !ok {
		return nil
	}
	var ids []string
	for _, block := range assistantMsg.Blocks {
		if call, ok := block.(llm.ToolCallBlock); ok && call.ID != "" {
			ids = append(ids, call.ID)
		}
	}
	return ids
}

func buildCompactionFactSheet(messages []llm.Message, sessionPath string) string {
	var b strings.Builder

	_, _ = fmt.Fprintf(&b, "Message count being compacted: %d\n", len(messages))
	if strings.TrimSpace(sessionPath) != "" {
		_, _ = fmt.Fprintf(&b, "Full session history path: %s\n", sessionPath)
	}
	_, _ = fmt.Fprintln(&b)
	_, _ = fmt.Fprintln(&b, "Deterministic facts:")
	for i, msg := range messages {
		switch typedMsg := msg.(type) {
		case llm.UserMessage:
			prefix := fmt.Sprintf("- %03d %s", i+1, "user")
			_, _ = fmt.Fprintf(&b, "%s: %s\n", prefix, compactOneLine(typedMsg.Text, 500))
		case llm.AssistantMessage:
			prefix := fmt.Sprintf("- %03d %s", i+1, "assistant")
			text := compactOneLine(typedMsg.Text(), 500)
			if text != "" {
				_, _ = fmt.Fprintf(&b, "%s: %s\n", prefix, text)
			}
			for _, block := range typedMsg.Blocks {
				if call, ok := block.(llm.ToolCallBlock); ok {
					_, _ = fmt.Fprintf(&b, "  tool_call %s args=%s\n", call.Name, compactOneLine(fmt.Sprintf("%v", call.Arguments), 400))
				}
			}
		case llm.WorkflowResultMessage:
			prefix := fmt.Sprintf("- %03d workflow %s %s", i+1, typedMsg.Name, typedMsg.Status)
			_, _ = fmt.Fprintf(&b, "%s input=%s summary=%s\n", prefix, compactOneLine(typedMsg.Input, 500), compactOneLine(typedMsg.Summary, 700))
		case llm.ToolOutputMessage:
			prefix := fmt.Sprintf("- %03d %s", i+1, "tool_result")
			_, _ = fmt.Fprintf(&b, "%s %s: %s\n", prefix, typedMsg.ToolName, compactOneLine(typedMsg.ToolOutput, 700))
		case llm.ToolErrorMessage:
			prefix := fmt.Sprintf("- %03d %s", i+1, "tool_error")
			_, _ = fmt.Fprintf(&b, "%s %s: %s\n", prefix, typedMsg.ToolName, compactOneLine(typedMsg.Error.Error(), 700))
		}
	}

	return b.String()
}

func renderCompactionSummary(summary compactionSummary, factSheet string) string {
	var b strings.Builder

	writeMarkdownSection(&b, "Current Goal", []string{summary.CurrentGoal})
	writeMarkdownSection(&b, "User Preferences", summary.UserPreferences)
	writeMarkdownSection(&b, "Decisions", summary.Decisions)
	writeMarkdownSection(&b, "Files and Code State", summary.FilesAndCodeState)
	writeMarkdownSection(&b, "Tests and Tool Results", summary.TestsAndToolResults)
	writeMarkdownSection(&b, "Open Tasks", summary.OpenTasks)
	writeMarkdownSection(&b, "Recovery", summary.Recovery)

	b.WriteString("# Automatically Preserved Facts\n")
	b.WriteString(factSheet)

	return strings.TrimSpace(b.String())
}

func renderCompactedContext(summary string) (string, error) {
	var b bytes.Buffer
	if err := compactedContextTemplate.Execute(&b, struct{ Summary string }{Summary: summary}); err != nil {
		return "", fmt.Errorf("render compacted context: %w", err)
	}
	text := strings.TrimSpace(b.String())
	if text == "" {
		return "", fmt.Errorf("compacted context is empty")
	}
	return text, nil
}

func writeMarkdownSection(b *strings.Builder, heading string, items []string) {
	_, _ = fmt.Fprintf(b, "# %s\n", heading)

	var written bool
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if heading == "Current Goal" {
			_, _ = fmt.Fprintf(b, "%s\n", item)
		} else {
			_, _ = fmt.Fprintf(b, "- %s\n", item)
		}
		written = true
	}
	if !written {
		b.WriteString("- None recorded.\n")
	}
	b.WriteString("\n")
}

func (s compactionSummary) empty() bool {
	return strings.TrimSpace(s.CurrentGoal) == "" &&
		allEmpty(s.UserPreferences) &&
		allEmpty(s.Decisions) &&
		allEmpty(s.FilesAndCodeState) &&
		allEmpty(s.TestsAndToolResults) &&
		allEmpty(s.OpenTasks) &&
		allEmpty(s.Recovery)
}

func allEmpty(items []string) bool {
	for _, item := range items {
		if strings.TrimSpace(item) != "" {
			return false
		}
	}
	return true
}

func compactOneLine(text string, max uint16) string {
	text = strings.Join(strings.Fields(text), " ")
	if max <= 0 {
		return text
	}
	runes := []rune(text)
	if len(runes) > int(max) {
		return string(runes[:max]) + "..."
	}
	return text
}
