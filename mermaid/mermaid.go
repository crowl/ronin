// Package mermaid renders a bounded subset of Mermaid diagrams as Unicode text.
// It uses only the Go standard library and performs no I/O.
package mermaid

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxInput = 32 * 1024
	maxNodes = 32
	maxEdges = 64
	maxLabel = 80
)

type node struct {
	id, label string
	shape     byte
	explicit  bool
}
type edge struct {
	from, to int
	label    string
}
type graph struct {
	nodes      []node
	edges      []edge
	horizontal bool
}
type parser struct {
	source string
	pos    int
	graph  graph
	ids    map[string]int
}

// Render converts a flowchart or sequence diagram to unstyled Unicode text, without a trailing newline.
// maxWidth must be positive. Invalid or unsupported syntax, resource limits, and
// layouts that cannot be routed or fit within maxWidth return an error and no text.
// Ordering is deterministic. Calls share no mutable state.
func Render(source string, maxWidth int) (string, error) {
	if maxWidth <= 0 {
		return "", fmt.Errorf("mermaid: width must be positive")
	}
	if len(source) > maxInput || !utf8.ValidString(source) {
		return "", fmt.Errorf("mermaid: input too large or invalid UTF-8")
	}
	if sequenceHeader(source) {
		diagram, err := parseSequence(source)
		if err != nil {
			return "", err
		}
		return renderSequence(diagram, maxWidth)
	}
	p := parser{source: source, ids: make(map[string]int)}
	g, err := p.parse()
	if err != nil {
		return "", err
	}
	return render(g, maxWidth)
}

func (p *parser) fail(message string) error {
	return fmt.Errorf("mermaid: line %d: %s", strings.Count(p.source[:p.pos], "\n")+1, message)
}
func (p *parser) spaces() {
	for p.pos < len(p.source) && (p.source[p.pos] == ' ' || p.source[p.pos] == '\t' || p.source[p.pos] == '\r') {
		p.pos++
	}
}
func (p *parser) take(s string) bool {
	if strings.HasPrefix(p.source[p.pos:], s) {
		p.pos += len(s)
		return true
	}
	return false
}
func (p *parser) identifier() string {
	start := p.pos
	for p.pos < len(p.source) {
		c := p.source[p.pos]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_' || p.pos > start && c >= '0' && c <= '9') {
			break
		}
		p.pos++
	}
	return p.source[start:p.pos]
}
func (p *parser) separator() bool {
	p.spaces()
	if p.take("%%") {
		for p.pos < len(p.source) && p.source[p.pos] != '\n' {
			p.pos++
		}
	}
	if p.pos == len(p.source) {
		return true
	}
	return p.take("\n") || p.take(";")
}
func (p *parser) parse() (graph, error) {
	for {
		p.spaces()
		if p.pos == len(p.source) {
			return graph{}, p.fail("expected flowchart header")
		}
		if !p.separator() {
			break
		}
	}
	header := p.identifier()
	if header != "graph" && header != "flowchart" {
		return graph{}, p.fail("expected graph or flowchart")
	}
	p.spaces()
	direction := p.identifier()
	switch direction {
	case "LR":
		p.graph.horizontal = true
	case "TD", "TB":
	default:
		return graph{}, p.fail("supported directions are TD, TB, and LR")
	}
	if !p.separator() {
		return graph{}, p.fail("expected newline or semicolon after header")
	}
	for p.pos < len(p.source) {
		p.spaces()
		if p.separator() {
			continue
		}
		from, err := p.parseNode()
		if err != nil {
			return graph{}, err
		}
		for {
			p.spaces()
			if !p.take("-->") {
				break
			}
			p.spaces()
			label := ""
			if p.take("|") {
				label, err = p.label('|')
				if err != nil {
					return graph{}, err
				}
				p.spaces()
			}
			to, err := p.parseNode()
			if err != nil {
				return graph{}, err
			}
			if len(p.graph.edges) >= maxEdges {
				return graph{}, p.fail("edge limit exceeded")
			}
			p.graph.edges = append(p.graph.edges, edge{from, to, label})
			from = to
		}
		if !p.separator() {
			return graph{}, p.fail("unsupported syntax; expected --> or statement separator")
		}
	}
	if len(p.graph.nodes) == 0 {
		return graph{}, p.fail("flowchart has no nodes")
	}
	return p.graph, nil
}
func (p *parser) parseNode() (int, error) {
	id := p.identifier()
	if id == "" {
		return 0, p.fail("expected node identifier")
	}
	switch id {
	case "subgraph", "end", "style", "classDef", "class", "click", "linkStyle", "direction":
		return 0, p.fail("unsupported directive: " + id)
	}
	p.spaces()
	shape := byte('[')
	label := id
	explicit := false
	if p.pos < len(p.source) {
		open := p.source[p.pos]
		close := byte(0)
		switch open {
		case '[':
			close = ']'
		case '(':
			close = ')'
		case '{':
			close = '}'
		}
		if close != 0 {
			p.pos++
			shape = open
			explicit = true
			var err error
			label, err = p.label(close)
			if err != nil {
				return 0, err
			}
		}
	}
	if i, ok := p.ids[id]; ok {
		if explicit {
			p.graph.nodes[i].label = label
			p.graph.nodes[i].shape = shape
			p.graph.nodes[i].explicit = true
		}
		return i, nil
	}
	if len(p.graph.nodes) >= maxNodes {
		return 0, p.fail("node limit exceeded")
	}
	i := len(p.graph.nodes)
	p.ids[id] = i
	p.graph.nodes = append(p.graph.nodes, node{id, label, shape, explicit})
	return i, nil
}
func (p *parser) label(close byte) (string, error) {
	p.spaces()
	quoted := p.take("\"")
	start := p.pos
	for p.pos < len(p.source) {
		c := p.source[p.pos]
		if quoted && c == '"' || !quoted && c == close {
			break
		}
		if c == '\n' || c == '\r' || c == '\\' || !quoted && strings.ContainsRune("[]{}()\"", rune(c)) {
			return "", p.fail("unsupported label syntax")
		}
		p.pos++
	}
	value := strings.TrimSpace(p.source[start:p.pos])
	if quoted {
		if !p.take("\"") {
			return "", p.fail("unterminated quoted label")
		}
		p.spaces()
	}
	if !p.take(string(close)) {
		return "", p.fail("unterminated label")
	}
	if value == "" {
		return "", p.fail("empty label")
	}
	width, err := labelWidth(value)
	if err != nil {
		return "", p.fail(err.Error())
	}
	if width > maxLabel {
		return "", p.fail("label too wide")
	}
	// Mermaid interprets these as markup/entities; rendering them literally would
	// promise semantics this subset does not implement.
	if strings.ContainsAny(value, "<>&") {
		return "", p.fail("HTML and entities in labels are unsupported")
	}
	return value, nil
}

// Width deliberately supports ordinary text, combining marks, and common CJK
// characters, not grapheme clusters, emoji presentation, or terminal controls.
func labelWidth(s string) (int, error) {
	width := 0
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r >= 0xFE00 && r <= 0xFE0F || r >= 0x1F000 {
			return 0, fmt.Errorf("unsupported label character U+%04X", r)
		}
		if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) {
			if width == 0 {
				return 0, fmt.Errorf("label starts with a combining mark")
			}
			continue
		}
		width += runeWidth(r)
	}
	return width, nil
}
func runeWidth(r rune) int {
	if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) {
		return 0
	}
	if r >= 0x1100 && (r <= 0x115F || r == 0x2329 || r == 0x232A || r >= 0x2E80 && r <= 0xA4CF && r != 0x303F || r >= 0xAC00 && r <= 0xD7A3 || r >= 0xF900 && r <= 0xFAFF || r >= 0xFE10 && r <= 0xFE19 || r >= 0xFE30 && r <= 0xFE6F || r >= 0xFF00 && r <= 0xFF60 || r >= 0xFFE0 && r <= 0xFFE6) {
		return 2
	}
	return 1
}
