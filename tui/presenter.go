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

// Boxes

const maxToolOutputLinesNotExpanded = 10

type boxesPresenter struct {
	Boxes         []box
	ToolsExpanded bool
}

func (p boxesPresenter) Lines(width int, theme Theme) []string {
	var lines []string
	for i, block := range p.Boxes {
		if i > 0 && needsBlankLineBeforeBox(p.Boxes[i-1], block) {
			lines = append(lines, "")
		}
		lines = append(lines, renderBoxLines(block, width, theme, p.ToolsExpanded)...)
	}
	return lines
}

func renderBoxLines(block box, width int, theme Theme, toolsExpanded bool) []string {
	return renderBoxLinesAt(block, width, theme, toolsExpanded, time.Now())
}

func renderBoxLinesAt(block box, width int, theme Theme, toolsExpanded bool, now time.Time) []string {
	var lines []string

	switch typedBlock := block.(type) {
	case userMessageBox:
		boxStyle := theme.Box.User
		lines = append(lines, boxStyle.ApplyBody(text.Fill("", width)))
		for _, line := range text.Wrap(" ", typedBlock.Text, width) {
			lines = append(lines, boxStyle.ApplyBody(text.Fill(line, width)))
		}
		lines = append(lines, boxStyle.ApplyBody(text.Fill("", width)))
	case assistantMessageBox:
		boxStyle := theme.Box.Assistant
		lines = append(lines, boxStyle.ApplyBody(text.Fill("", width)))
		for _, line := range markdownLines(typedBlock.Text, width, boxStyle.TextTheme(theme.Text)) {
			lines = append(lines, line)
		}
		lines = append(lines, boxStyle.ApplyBody(text.Fill("", width)))
	case assistantThinkingBox:
		boxStyle := theme.Box.AssistantThinking
		lines = append(lines, boxStyle.ApplyBody(text.Fill("", width)))
		textTheme := boxStyle.TextTheme(theme.Text)
		for _, line := range markdownLines(typedBlock.Text, width, textTheme) {
			lines = append(lines, line)
		}
		lines = append(lines, boxStyle.ApplyBody(text.Fill("", width)))
	case toolCallBox:
		boxStyle := theme.Box.ToolCall
		lines = append(lines, boxStyle.ApplyBody(text.Fill("", width)))
		header := " " + typedBlock.Title
		lines = append(lines, boxStyle.ApplyTitle(text.Fill(header, width)))
		lines = append(lines, boxStyle.ApplyBody(text.Fill("", width)))
		var artifactLines []string
		for _, artifact := range typedBlock.Artifacts {
			artifactLines = append(artifactLines, toolArtifactLines(artifact, boxStyle, width)...)
		}
		if typedBlock.Error != "" {
			for _, line := range text.Wrap(" ", typedBlock.Error, width) {
				artifactLines = append(artifactLines, boxStyle.ApplyDiffRemoved(text.Fill(line, width)))
			}
		}
		if len(artifactLines) > maxToolOutputLinesNotExpanded && !toolsExpanded {
			skipped := len(artifactLines) - maxToolOutputLinesNotExpanded
			notice := fmt.Sprintf(" ... (%d more lines, ctrl+o to expand)", skipped)
			artifactLines = append(artifactLines[:maxToolOutputLinesNotExpanded], boxStyle.ApplyMuted(text.Fill(notice, width)))
		}
		lines = append(lines, artifactLines...)
		endedAt := typedBlock.EndedAt
		label := " Took"
		if endedAt.IsZero() {
			endedAt = now
			label = " Elapsed"
		}
		duration := endedAt.Sub(typedBlock.StartedAt)
		if duration < 0 {
			duration = 0
		}
		lines = append(lines, boxStyle.ApplyBody(text.Fill("", width)))
		lines = append(lines, boxStyle.ApplyMeta(text.Fill(fmt.Sprintf("%s %.1fs", label, duration.Seconds()), width)))
		lines = append(lines, boxStyle.ApplyBody(text.Fill("", width)))
	case systemMessageBox:
		boxStyle := theme.Box.System
		lines = append(lines, boxStyle.ApplyBody(text.Fill("", width)))
		for _, line := range text.Wrap(" ", typedBlock.Text, width) {
			lines = append(lines, boxStyle.ApplyBody(text.Fill(line, width)))
		}
		lines = append(lines, boxStyle.ApplyBody(text.Fill("", width)))
	case errorMessageBox:
		boxStyle := theme.Box.Error
		lines = append(lines, boxStyle.ApplyBody(text.Fill("", width)))
		for _, line := range text.Wrap(" ", typedBlock.Text, width) {
			lines = append(lines, boxStyle.ApplyBody(text.Fill(line, width)))
		}
		lines = append(lines, boxStyle.ApplyBody(text.Fill("", width)))
	}

	return lines
}

func needsBlankLineBeforeBox(previous box, current box) bool {
	switch current.(type) {
	case userMessageBox, toolCallBox:
		switch previous.(type) {
		case userMessageBox, toolCallBox, systemMessageBox, errorMessageBox:
			return true
		}
	}
	return false
}

// Working Indicator

var workingIndicatorFrames = []string{
	"Working   ",
	"Working.  ",
	"Working.. ",
	"Working...",
	"Working.. ",
	"Working.  ",
}

type workingIndicator struct {
	Frame int
}

func (p workingIndicator) Lines(_ int, theme Theme) []string {
	indicator := workingIndicatorFrames[p.Frame%len(workingIndicatorFrames)]

	return []string{
		"",
		theme.UI.WorkingIndicator.Apply(" " + indicator),
		"",
	}
}

// editorPresenter

const editorPrefix = " "

type editorPresenter struct {
	Text   []rune
	Cursor int
}

func (p editorPresenter) Lines(width int, theme Theme) []string {
	separator := strings.Repeat("─", width)

	cursor := p.Cursor
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(p.Text) {
		cursor = len(p.Text)
	}

	logicalLines := spliteditorPresenterLogicalLines(p.Text)
	cursorLine, cursorColumn := editorCursorLineColumn(p.Text, cursor)

	lines := make([]string, 0, len(logicalLines)+2)
	lines = append(lines, theme.UI.EditorSeparator.Apply(separator))

	firstVisualLine := true
	for logicalLineIndex, logicalLine := range logicalLines {
		segments := wrapeditorPresenterLogicalLine(logicalLine, width, firstVisualLine)
		for _, segment := range segments {
			prefix := " "
			if firstVisualLine {
				prefix = editorPrefix
			}
			containsCursor := logicalLineIndex == cursorLine && segment.startColumn <= cursorColumn && cursorColumn <= segment.endColumn
			lines = append(lines, rendereditorPresenterSegment(prefix, segment, cursorColumn, containsCursor, theme))
			firstVisualLine = false
		}
	}

	lines = append(lines, theme.UI.EditorSeparator.Apply(separator))

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
	prefixWidth := text.VisibleLen(" ")
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

func rendereditorPresenterSegment(prefix string, segment editorSegment, cursorColumn int, containsCursor bool, theme Theme) string {
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
		return prefix + beforeCursor + theme.UI.EditorCursor.Apply(cursorCell) + afterCursor
	}

	return prefix + beforeCursor + theme.UI.EditorCursor.Apply(" ")
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
)

func (p menuPresenter) Lines(width int, theme Theme) []string {
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

	out := make([]string, 0, end-start)
	for i := start; i < end; i++ {
		item := p.Items[i]

		prefix := menuItemPrefix
		if i == selectedIndex {
			prefix = menuItemPrefixSelected
		}

		line := fmt.Sprintf("%s%s", prefix, item.Value)

		if item.Description != "" {
			pad := 2
			if text.VisibleLen(line) < 28 {
				pad = 28 - text.VisibleLen(line)
			}
			line += strings.Repeat(" ", pad) + item.Description
		}

		if text.VisibleLen(line) > width {
			line = text.Truncate(line, width)
		}

		if text.VisibleLen(line) < width {
			line += strings.Repeat(" ", width-text.VisibleLen(line))
		}

		if i == selectedIndex {
			line = theme.UI.MenuItemSelected.Apply(line)
		} else {
			line = theme.UI.MenuItem.Apply(line)
		}

		out = append(out, line)
	}

	return out
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

func (p statusBar) Lines(width int, theme Theme) []string {
	cwdStatus := p.CWDStatus
	if !p.UseCWDStatus {
		cwdStatus = statusBarCWDStatus(p.CWD)
	}

	usageParts := []string{
		fmt.Sprintf("↑%d", p.ContextUsage.InputTokens),
		fmt.Sprintf("↓%d", p.ContextUsage.OutputTokens),
		fmt.Sprintf("R%d", p.ContextUsage.CachedTokens),
	}

	contextUsed := p.ContextUsage.InputTokens + p.ContextUsage.OutputTokens
	contextPercent := 0.0
	if p.Model.ContextLimit > 0 {
		contextPercent = min(100.0, (float64(contextUsed)*100)/float64(p.Model.ContextLimit))
	}
	usageParts = append(usageParts, fmt.Sprintf("%.1f%%/%d", contextPercent, p.Model.ContextLimit))
	usageStatus := strings.Join(usageParts, " ")

	modelStatus := fmt.Sprintf("%s %s", p.Model, p.ReasoningLevel)

	return []string{
		theme.UI.StatusBar.Apply(text.Fill(cwdStatus, width)),
		theme.UI.StatusBar.Apply(twoColumnsLine(usageStatus, modelStatus, width)),
	}
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
