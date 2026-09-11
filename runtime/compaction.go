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
	"unicode/utf8"

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

const (
	defaultCompactKeepMessages             = 12
	maxCompactionFactSheetBytes            = 128 << 10
	maxPreviousCompactionSummaryBytes      = maxCompactionFactSheetBytes / 2
	compactionFactsHeading                 = "# Automatically Preserved Facts"
	compactedContextPreamble               = "This is a compacted record of prior conversation state. Treat it as authoritative unless contradicted by newer messages."
	truncatedPreviousCompactionSummaryNote = "\n\n[... previous compacted context truncated ...]\n\n"
)

type DefaultCompactorConfig struct {
	SessionID   string
	ModelClient llm.ModelClient
	Now         func() time.Time
}

func NewDefaultCompactor(cfg DefaultCompactorConfig) (*DefaultCompactor, error) {
	if cfg.ModelClient == nil {
		return nil, fmt.Errorf("model client is required")
	}
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

	if len(msgs) <= defaultCompactKeepMessages && compactionMessageBytes(msgs) < 32*1024 {
		return nil, fmt.Errorf("not enough safely compactable messages: have %d", len(msgs))
	}
	keep := min(defaultCompactKeepMessages, len(msgs)-1)
	// Retain a bounded recent tail rather than a fixed count of arbitrarily
	// large results. Never separate a tool result from its call.
	window := c.modelClient.Model().ContextWindow
	budget := 32 * 1024
	if window > 0 {
		budget = max(1024, int(window)/8*4)
	}
	start := safeCompactionStart(msgs, keep)
	for keep > 1 && compactionMessageBytes(msgs[start:]) > budget {
		keep--
		start = safeCompactionStart(msgs, keep)
	}
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

	preservedFacts := buildPreservedCompactionFactSheet(older, "")
	messageText, err := renderCompactedContext(renderCompactionSummary(summary, preservedFacts))
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

	raw, err := llm.PredictStructuredObserved(ctx, c.modelClient, llm.PredictNextStructuredRequest{
		SystemPrompt: "You compact coding conversation context into precise structured JSON.",
		Messages:     []llm.Message{msg},
		Schema:       jsonschema.FromType[compactionSummary](),
	}, "compaction")
	if err != nil {
		return compactionSummary{}, fmt.Errorf("generate structured compaction summary: %w", err)
	}

	if err := jsonschema.Validate(jsonschema.FromType[compactionSummary](), raw); err != nil {
		return compactionSummary{}, fmt.Errorf("validate structured compaction summary: %w", err)
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
			resultID := ""
			switch toolResultMsg := messages[i].(type) {
			case llm.ToolOutputMessage:
				resultID = toolResultMsg.ToolCallID
			case llm.ToolErrorMessage:
				resultID = toolResultMsg.ToolCallID
			default:
				continue
			}
			if resultID != "" && !seenCalls[resultID] {
				missingCallID = resultID
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
	return buildCompactionFactSheetWithPreviousSummary(messages, sessionPath, true)
}

func buildPreservedCompactionFactSheet(messages []llm.Message, sessionPath string) string {
	return buildCompactionFactSheetWithPreviousSummary(messages, sessionPath, false)
}

func buildCompactionFactSheetWithPreviousSummary(messages []llm.Message, sessionPath string, includePreviousSummary bool) string {
	var b strings.Builder

	_, _ = fmt.Fprintf(&b, "Message count being compacted: %d\n", len(messages))
	if strings.TrimSpace(sessionPath) != "" {
		_, _ = fmt.Fprintf(&b, "Full session history path: %s\n", sessionPath)
	}
	_, _ = fmt.Fprintln(&b)
	_, _ = fmt.Fprintln(&b, "Recover omitted details with conversation_history using the history references below. References discarded by rewind or absent from this lineage are unavailable.")
	_, _ = fmt.Fprintln(&b, "Deterministic facts:")
	lines := make([]string, len(messages))
	for i, msg := range messages {
		if user, ok := msg.(llm.UserMessage); ok && isCompactedContext(user.Text) {
			if includePreviousSummary {
				lines[i] = previousCompactionSummaryFactLine(i, user.Text)
			}
			continue
		}
		lines[i] = compactionFactLine(i, msg)
	}
	const omissionReserve = 128
	available := maxCompactionFactSheetBytes - b.Len() - omissionReserve
	selected := make([]bool, len(lines))
	used := 0
	for i, message := range messages {
		if includePreviousSummary {
			if user, ok := message.(llm.UserMessage); ok && isCompactedContext(user.Text) && used+len(lines[i]) <= available {
				selected[i] = true
				used += len(lines[i])
			}
		}
	}
	// Reserve the original request before prioritizing recent requirements.
	for i, message := range messages {
		if _, ok := message.(llm.UserMessage); ok && !selected[i] && used+len(lines[i]) <= available {
			selected[i] = true
			used += len(lines[i])
			break
		}
	}
	// Preserve user requirements before spending the budget on tool chatter.
	for i := len(messages) - 1; i >= 0; i-- {
		if _, ok := messages[i].(llm.UserMessage); ok && !selected[i] && used+len(lines[i]) <= available {
			selected[i] = true
			used += len(lines[i])
		}
	}
	for i, line := range lines {
		if line == "" || selected[i] {
			continue
		}
		if used+len(line) > available/4 {
			break
		}
		selected[i] = true
		used += len(line)
	}
	for i := len(lines) - 1; i >= 0; i-- {
		line := lines[i]
		if line == "" || selected[i] || !importantCompactionFact(messages[i]) {
			continue
		}
		if used+len(line) > available {
			continue
		}
		selected[i] = true
		used += len(line)
	}
	for i := len(lines) - 1; i >= 0; i-- {
		line := lines[i]
		if line == "" || selected[i] {
			continue
		}
		if used+len(line) > available {
			continue
		}
		selected[i] = true
		used += len(line)
	}
	omitted := 0
	for i, line := range lines {
		if line == "" {
			continue
		}
		if selected[i] {
			b.WriteString(line)
		} else {
			omitted++
		}
	}
	if omitted > 0 {
		_, _ = fmt.Fprintf(&b, "- ... %d message(s) omitted to keep compaction input bounded.\n", omitted)
	}

	return b.String()
}

func importantCompactionFact(msg llm.Message) bool {
	switch typed := msg.(type) {
	case llm.UserMessage, llm.WorkflowResultMessage, llm.ToolOutputMessage, llm.ToolErrorMessage, llm.ErrorMessage:
		return true
	case llm.AssistantMessage:
		return len(messageToolCallIDs(typed)) > 0
	default:
		return false
	}
}

func compactionFactLine(index int, msg llm.Message) string {
	var b strings.Builder
	switch typedMsg := msg.(type) {
	case llm.UserMessage:
		_, _ = fmt.Fprintf(&b, "- %03d user: %s\n", index+1, truncateHeadTail(typedMsg.Text, 32*1024))
	case llm.AssistantMessage:
		prefix := fmt.Sprintf("- %03d assistant", index+1)
		text := compactOneLine(typedMsg.Text(), 4000)
		if text != "" {
			_, _ = fmt.Fprintf(&b, "%s: %s\n", prefix, text)
		}
		for _, block := range typedMsg.Blocks {
			if call, ok := block.(llm.ToolCallBlock); ok {
				_, _ = fmt.Fprintf(&b, "  tool_call %s args=%s\n", call.Name, compactOneLine(string(call.Arguments), 400))
			}
		}
	case llm.WorkflowResultMessage:
		_, _ = fmt.Fprintf(&b, "- %03d workflow %s %s input=%s summary=%s\n", index+1, typedMsg.Name, typedMsg.Status, compactOneLine(typedMsg.Input, 500), compactOneLine(typedMsg.Summary, 700))
	case llm.ToolOutputMessage:
		_, _ = fmt.Fprintf(&b, "- %03d tool_result %s: %s\n", index+1, typedMsg.ToolName, compactOneLine(typedMsg.ToolOutput, 4000))
		b.WriteString(compactionToolFacts(typedMsg.ToolName, typedMsg.ToolOutput))
	case llm.ToolErrorMessage:
		text := ""
		if typedMsg.Error != nil {
			text = typedMsg.Error.Error()
		}
		_, _ = fmt.Fprintf(&b, "- %03d tool_error %s: %s\n", index+1, typedMsg.ToolName, compactOneLine(text, 700))
	case llm.ErrorMessage:
		text := ""
		if typedMsg.Error != nil {
			text = typedMsg.Error.Error()
		}
		_, _ = fmt.Fprintf(&b, "- %03d error: %s\n", index+1, compactOneLine(text, 700))
	}
	ref, _ := historyMessage(msg)
	if b.Len() > 0 {
		_, _ = fmt.Fprintf(&b, "  source=%s\n", ref)
	}
	return b.String()
}

func previousCompactionSummaryFactLine(index int, text string) string {
	summary := extractPreviousCompactionSummary(text)
	if summary == "" {
		return ""
	}
	return fmt.Sprintf("- %03d previous compacted summary:\n%s\n", index+1, summary)
}

func extractPreviousCompactionSummary(text string) string {
	text = strings.TrimSpace(text)
	text = strings.TrimSpace(strings.TrimPrefix(text, "<compacted_context>"))
	text = strings.TrimSpace(strings.TrimSuffix(text, "</compacted_context>"))
	text = strings.TrimSpace(strings.TrimPrefix(text, compactedContextPreamble))

	if start := strings.Index(text, "# Current Goal"); start >= 0 {
		text = text[start:]
	}
	if end := strings.Index(text, compactionFactsHeading); end >= 0 {
		text = text[:end]
	}

	return truncateHeadTail(strings.TrimSpace(text), maxPreviousCompactionSummaryBytes)
}

func truncateHeadTail(text string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(text) <= maxBytes {
		return text
	}
	if maxBytes <= len(truncatedPreviousCompactionSummaryNote) {
		return truncatedPreviousCompactionSummaryNote[:maxBytes]
	}

	remaining := maxBytes - len(truncatedPreviousCompactionSummaryNote)
	headBytes := remaining / 2
	tailStart := len(text) - (remaining - headBytes)
	for headBytes > 0 && !utf8.RuneStart(text[headBytes]) {
		headBytes--
	}
	for tailStart < len(text) && !utf8.RuneStart(text[tailStart]) {
		tailStart++
	}
	return text[:headBytes] + truncatedPreviousCompactionSummaryNote + text[tailStart:]
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

	b.WriteString(compactionFactsHeading + "\n")
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
		const note = " ... [content omitted] ... "
		n := (int(max) - len(note)) / 2
		if n < 1 {
			return string(runes[:max]) + "..."
		}
		return string(runes[:n]) + note + string(runes[len(runes)-(int(max)-len(note)-n):])
	}
	return text
}
