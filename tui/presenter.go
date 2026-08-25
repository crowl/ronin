package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/tui/internal/text"
)

// Conversation

const (
	maxToolOutputLinesNotExpanded = 10
	conversationBoxPadding        = " "
	toolMarker                    = "• "
)

type boxesPresenter struct {
	Boxes         []box
	ToolsExpanded bool
}

func (p boxesPresenter) Lines(width int) []string {
	var lines []string
	for i, block := range p.Boxes {
		if i > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, renderBoxLines(block, width, p.ToolsExpanded)...)
	}
	return lines
}

func renderBoxLines(block box, width int, toolsExpanded bool) []string {
	return renderBoxLinesAt(block, width, toolsExpanded, time.Now())
}

func renderBoxLinesAt(block box, width int, toolsExpanded bool, now time.Time) []string {
	contentWidth := max(1, width-text.VisibleLen(conversationBoxPadding))
	lines := renderBoxContentLinesAt(block, contentWidth, toolsExpanded, now)
	for i := range lines {
		lines[i] = conversationBoxPadding + lines[i]
	}
	return lines
}

func renderBoxContentLinesAt(block box, width int, toolsExpanded bool, now time.Time) []string {
	switch typedBlock := block.(type) {
	case userMessageBox:
		return markedLines("> ", typedBlock.Text, width, style{})
	case assistantMessageBox:
		return markdownLines(typedBlock.Text, width, defaultTextStyles())
	case assistantThinkingBox:
		return markdownLines(typedBlock.Text, width, thinkingTextStyles())
	case toolCallBox:
		lines := markedLines(toolMarker, typedBlock.Title, width, strongStyle)
		var artifactLines []string
		for _, artifact := range typedBlock.Artifacts {
			artifactLines = append(artifactLines, toolArtifactLines(artifact, width)...)
		}
		if typedBlock.Error != "" {
			for _, line := range text.Wrap("  ", typedBlock.Error, width) {
				artifactLines = append(artifactLines, errorStyle.apply(line))
			}
		}
		if len(artifactLines) > maxToolOutputLinesNotExpanded && !toolsExpanded {
			skipped := len(artifactLines) - maxToolOutputLinesNotExpanded
			notice := fmt.Sprintf("  ... (%d more lines, ctrl+o to expand)", skipped)
			artifactLines = append(artifactLines[:maxToolOutputLinesNotExpanded], notice)
		}
		lines = append(lines, artifactLines...)

		endedAt := typedBlock.EndedAt
		label := "Took"
		if endedAt.IsZero() {
			endedAt = now
			label = "Elapsed"
		}
		duration := max(0, endedAt.Sub(typedBlock.StartedAt).Seconds())
		return append(lines, fmt.Sprintf("  %s %.1fs", label, duration))
	case workflowBox:
		return renderWorkflowBoxLines(typedBlock, width, toolsExpanded, now)
	case systemMessageBox:
		return markedLines(toolMarker, typedBlock.Text, width, style{})
	case errorMessageBox:
		return markedLines("! ", typedBlock.Text, width, errorStyle)
	default:
		return nil
	}
}

func markedLines(marker, value string, width int, lineStyle style) []string {
	continuation := strings.Repeat(" ", text.VisibleLen(marker))
	wrapped := text.Wrap("", value, max(1, width-text.VisibleLen(marker)))
	lines := make([]string, 0, len(wrapped))
	for i, line := range wrapped {
		prefix := continuation
		if i == 0 {
			prefix = marker
		}
		lines = append(lines, lineStyle.apply(prefix+line))
	}
	return lines
}

// Working Indicator

var workingIndicatorDots = []string{"   ", ".  ", ".. ", "...", ".. ", ".  "}

type workingIndicator struct {
	Frame int
	Label string
}

func (p workingIndicator) Lines(_ int) []string {
	label := p.Label
	if label == "" {
		label = "Working"
	}
	indicator := label + workingIndicatorDots[p.Frame%len(workingIndicatorDots)]
	return []string{indicator}
}

// pendingSteeringPresenter renders a queued steering prompt as a muted
// System box above the working indicator. It is transient and not stored
// in the box history.
type pendingSteeringPresenter struct {
	Text string
}

func (p pendingSteeringPresenter) Lines(width int) []string {
	return markedLines(toolMarker+"(queued) ", p.Text, width, style{})
}

// editorPresenter

const editorPrefix = "> "

type editorPresenter struct {
	Text   []rune
	Cursor int
	Label  string
}

func (p editorPresenter) Lines(width int) []string {
	cursor := p.Cursor
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(p.Text) {
		cursor = len(p.Text)
	}

	logicalLines := spliteditorPresenterLogicalLines(p.Text)
	cursorLine, cursorColumn := editorCursorLineColumn(p.Text, cursor)

	lines := make([]string, 0, len(logicalLines)+1)
	if p.Label != "" {
		lines = append(lines, "% "+p.Label)
	}

	firstVisualLine := true
	for logicalLineIndex, logicalLine := range logicalLines {
		segments := wrapeditorPresenterLogicalLine(logicalLine, width, firstVisualLine)
		for _, segment := range segments {
			prefix := "  "
			if firstVisualLine {
				prefix = editorPrefix
			}
			containsCursor := logicalLineIndex == cursorLine && segment.startColumn <= cursorColumn && cursorColumn <= segment.endColumn
			lines = append(lines, rendereditorPresenterSegment(prefix, segment, cursorColumn, containsCursor))
			firstVisualLine = false
		}
	}

	return lines
}

type editorSegment struct {
	text        []rune
	startColumn int
	endColumn   int
}

func spliteditorPresenterLogicalLines(input []rune) [][]rune {
	var lines [][]rune
	start := 0
	for i, r := range input {
		if r == '\n' {
			lines = append(lines, append([]rune(nil), input[start:i]...))
			start = i + 1
		}
	}
	lines = append(lines, append([]rune(nil), input[start:]...))
	return lines
}

func editorCursorLineColumn(input []rune, cursor int) (int, int) {
	line := 0
	column := 0
	for i, r := range input {
		if i >= cursor {
			break
		}
		if r == '\n' {
			line++
			column = 0
			continue
		}
		column++
	}
	return line, column
}

func wrapeditorPresenterLogicalLine(line []rune, width int, firstVisualLine bool) []editorSegment {
	prefixWidth := text.VisibleLen("  ")
	if firstVisualLine {
		prefixWidth = text.VisibleLen(editorPrefix)
	}
	available := width - prefixWidth
	if available < 1 {
		available = 1
	}
	if len(line) == 0 {
		return []editorSegment{{startColumn: 0, endColumn: 0}}
	}

	segments := make([]editorSegment, 0, (len(line)/available)+1)
	for start := 0; start < len(line); {
		end := start + available
		if end > len(line) {
			end = len(line)
		}
		segments = append(segments, editorSegment{
			text:        line[start:end],
			startColumn: start,
			endColumn:   end,
		})
		start = end
	}
	return segments
}

func rendereditorPresenterSegment(prefix string, segment editorSegment, cursorColumn int, containsCursor bool) string {
	if !containsCursor {
		return prefix + string(segment.text)
	}

	segmentCursor := cursorColumn - segment.startColumn
	if segmentCursor < 0 {
		segmentCursor = 0
	}
	if segmentCursor > len(segment.text) {
		segmentCursor = len(segment.text)
	}

	beforeCursor := string(segment.text[:segmentCursor])
	if segmentCursor < len(segment.text) {
		cursorCell := string(segment.text[segmentCursor])
		afterCursor := string(segment.text[segmentCursor+1:])
		return prefix + beforeCursor + cursorStyle.apply(cursorCell) + afterCursor
	}

	return prefix + beforeCursor + cursorStyle.apply(" ")
}

// Menu

type menuPresenter struct {
	SelectedIndex int
	Items         []menuItem
}

const (
	menuMaxVisibleItems = 6

	menuItemPrefix         = "  "
	menuItemPrefixSelected = "→ "
	menuDescriptionGap     = 2
)

func (p menuPresenter) Lines(width int) []string {
	if len(p.Items) == 0 {
		return nil
	}

	selectedIndex := p.SelectedIndex
	if selectedIndex < 0 {
		selectedIndex = 0
	}
	if selectedIndex >= len(p.Items) {
		selectedIndex = len(p.Items) - 1
	}

	start := 0
	if selectedIndex >= menuMaxVisibleItems {
		start = selectedIndex - menuMaxVisibleItems + 1
	}

	end := min(len(p.Items), start+menuMaxVisibleItems)

	columns := p.columns()

	out := make([]string, 0, end-start)
	for i := start; i < end; i++ {
		out = append(out, p.line(p.Items[i], i == selectedIndex, columns, width))
	}

	return out
}

type menuColumns struct {
	NameWidth     int
	ArgumentWidth int
}

// columns aligns command names and arguments across all menu rows.
func (p menuPresenter) columns() menuColumns {
	var columns menuColumns
	for i := range p.Items {
		name, argument := p.Items[i].displayParts()
		if w := text.VisibleLen(name); w > columns.NameWidth {
			columns.NameWidth = w
		}
		if w := text.VisibleLen(argument); w > columns.ArgumentWidth {
			columns.ArgumentWidth = w
		}
	}
	return columns
}

func (p menuPresenter) line(item menuItem, selected bool, columns menuColumns, width int) string {
	prefix := menuItemPrefix
	if selected {
		prefix = menuItemPrefixSelected
	}

	name, argument := item.displayParts()
	value := prefix + text.Fill(name, columns.NameWidth) + strings.Repeat(" ", menuDescriptionGap)
	value += text.Fill(argument, columns.ArgumentWidth) + strings.Repeat(" ", menuDescriptionGap)
	if text.VisibleLen(value) >= width {
		value = text.Truncate(value, width)
		if selected {
			return selectedStyle.apply(value)
		}
		return value
	}

	column := min(text.VisibleLen(value), width)
	valueColumn := text.Fill(value, column)
	descriptionColumnText := text.Fill(item.Description, width-column)

	line := text.Fill(valueColumn+descriptionColumnText, width)
	if selected {
		return selectedStyle.apply(line)
	}
	return line
}

// statusBar

type statusBar struct {
	CWD string

	CWDStatus    string
	UseCWDStatus bool

	Model          llm.Model
	ReasoningLevel llm.ReasoningLevel
	ContextUsage   llm.Usage
}

func (p statusBar) Lines(width int) []string {
	cwdStatus := p.CWDStatus
	if !p.UseCWDStatus {
		cwdStatus = statusBarCWDStatus(p.CWD)
	}

	contextUsed := p.ContextUsage.InputTokens + p.ContextUsage.OutputTokens
	contextPercent := 0.0
	if p.Model.ContextWindow > 0 {
		contextPercent = min(100.0, (float64(contextUsed)*100)/float64(p.Model.ContextWindow))
	}

	usage := fmt.Sprintf(
		"↑%s ↓%s R%s %.1f%%/%s",
		statusBarTokenCountText(p.ContextUsage.InputTokens),
		statusBarTokenCountText(p.ContextUsage.OutputTokens),
		statusBarTokenCountText(p.ContextUsage.CachedTokens),
		contextPercent,
		statusBarTokenCountText(int(p.Model.ContextWindow)),
	)
	details := fmt.Sprintf("%s %s | %s", p.Model, p.ReasoningLevel, usage)
	return []string{twoColumnsLine(cwdStatus, details, width)}
}

func statusBarTokenCountText(tokens int) string {
	if tokens < 1000 {
		return fmt.Sprintf("%d", tokens)
	}
	return fmt.Sprintf("%d.%dK", tokens/1000, (tokens%1000)/100)
}

func twoColumnsLine(left string, right string, width int) string {
	if width <= 0 {
		return ""
	}

	right = text.Truncate(right, width)
	rightWidth := text.VisibleLen(right)
	leftWidth := width - rightWidth
	if rightWidth > 0 && leftWidth > 0 {
		leftWidth--
	}
	if leftWidth < 0 {
		leftWidth = 0
	}

	left = text.Truncate(left, leftWidth)
	line := text.Fill(left, width-rightWidth)
	return line + right
}

func statusBarCWDStatus(cwd string) string {
	var cwdStatus string

	cwdAbsolute, err := filepath.Abs(cwd)
	if err != nil {
		cwdAbsolute = cwd
	}

	homeDir, err := os.UserHomeDir()
	if err == nil && homeDir != "" {
		homeAbs, homeErr := filepath.Abs(homeDir)
		if homeErr == nil {
			homeDir = homeAbs
		}
		if cwdAbsolute == homeDir {
			cwdStatus = "~"
		}
		prefix := homeDir + string(filepath.Separator)
		if strings.HasPrefix(cwdAbsolute, prefix) {
			cwdStatus = "~" + string(filepath.Separator) + strings.TrimPrefix(cwdAbsolute, prefix)
		}
	}

	if branch := findGitBranch(cwdAbsolute); branch != "" {
		cwdStatus += " (" + branch + ")"
	}

	return cwdStatus
}

// git stuff

func findGitBranch(cwd string) string {
	gitDir := findGitDir(cwd)
	if gitDir == "" {
		return ""
	}

	data, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return ""
	}

	head := strings.TrimSpace(string(data))
	if strings.HasPrefix(head, "ref:") {
		ref := strings.TrimSpace(strings.TrimPrefix(head, "ref:"))
		const prefix = "refs/heads/"
		if strings.HasPrefix(ref, prefix) {
			return strings.TrimPrefix(ref, prefix)
		}
		return filepath.Base(ref)
	}

	if len(head) >= 7 && isHex(head) {
		return "detached " + head[:7]
	}

	return ""
}

func findGitDir(cwd string) string {
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return ""
	}
	current := filepath.Clean(abs)
	for {
		candidate := filepath.Join(current, ".git")
		info, err := os.Stat(candidate)
		if err == nil {
			if info.IsDir() {
				return candidate
			}
			if gitDir := readGitFile(candidate, current); gitDir != "" {
				return gitDir
			}
		}
		parent := filepath.Dir(current)
		if parent == current {
			return ""
		}
		current = parent
	}
}

func readGitFile(path string, base string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}

	content := strings.TrimSpace(string(data))
	if !strings.HasPrefix(content, "gitdir:") {
		return ""
	}

	gitDir := strings.TrimSpace(strings.TrimPrefix(content, "gitdir:"))
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(base, gitDir)
	}

	info, err := os.Stat(gitDir)
	if err != nil || !info.IsDir() {
		return ""
	}

	return gitDir
}

func isHex(s string) bool {
	for _, r := range s {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F') {
			continue
		}
		return false
	}
	return true
}
