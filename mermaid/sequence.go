package mermaid

import (
	"fmt"
	"strings"
)

const (
	maxParticipants   = 16
	maxSequenceEvents = 128
	maxSequenceDepth  = 8
)

type participant struct{ id, label string }
type sequenceEvent struct {
	kind, label string
	from, to    int
	dashed      bool
	depth       int
}
type sequence struct {
	participants []participant
	events       []sequenceEvent
	depth        int
}
type sequenceParser struct {
	diagram sequence
	ids     map[string]int
	blocks  []string
	line    int
}

func sequenceHeader(source string) bool {
	for _, line := range strings.Split(source, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "%%") {
			continue
		}
		return line == "sequenceDiagram"
	}
	return false
}
func parseSequence(source string) (sequence, error) {
	p := sequenceParser{ids: make(map[string]int)}
	header := false
	for i, line := range strings.Split(source, "\n") {
		p.line = i + 1
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "%%") {
			continue
		}
		if !header {
			if line != "sequenceDiagram" {
				return sequence{}, p.fail("expected sequenceDiagram")
			}
			header = true
			continue
		}
		if err := p.statement(line); err != nil {
			return sequence{}, err
		}
	}
	if len(p.blocks) > 0 {
		return sequence{}, p.fail("unclosed control block")
	}
	if len(p.diagram.participants) == 0 {
		return sequence{}, p.fail("sequence has no participants")
	}
	return p.diagram, nil
}
func (p *sequenceParser) fail(message string) error {
	return fmt.Errorf("mermaid: line %d: %s", p.line, message)
}
func sequenceID(s string) bool {
	if s == "" {
		return false
	}
	for i, c := range []byte(s) {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_' || i > 0 && c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}
func (p *sequenceParser) participant(id string) (int, error) {
	if !sequenceID(id) {
		return 0, p.fail("invalid participant identifier")
	}
	if i, ok := p.ids[id]; ok {
		return i, nil
	}
	if len(p.diagram.participants) >= maxParticipants {
		return 0, p.fail("participant limit exceeded")
	}
	if err := sequenceLabel(id); err != nil {
		return 0, p.fail(err.Error())
	}
	i := len(p.diagram.participants)
	p.ids[id] = i
	p.diagram.participants = append(p.diagram.participants, participant{id, id})
	return i, nil
}
func sequenceLabel(s string) error {
	w, err := labelWidth(s)
	if err != nil {
		return err
	}
	if w > maxLabel {
		return fmt.Errorf("label too wide")
	}
	if strings.ContainsAny(s, "<>&\\") {
		return fmt.Errorf("HTML, entities, and escapes in labels are unsupported")
	}
	return nil
}
func (p *sequenceParser) append(e sequenceEvent) error {
	if len(p.diagram.events) >= maxSequenceEvents {
		return p.fail("sequence event limit exceeded")
	}
	if err := sequenceLabel(e.label); err != nil {
		return p.fail(err.Error())
	}
	p.diagram.events = append(p.diagram.events, e)
	return nil
}
func (p *sequenceParser) statement(line string) error {
	word, rest, _ := strings.Cut(line, " ")
	// Permit tabs between keywords and arguments, but never within labels.
	if i := strings.IndexAny(line, " \t"); i >= 0 {
		word, rest = line[:i], strings.TrimSpace(line[i:])
	}
	if word == "participant" {
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			return p.fail("expected participant identifier")
		}
		id := fields[0]
		label := id
		suffix := strings.TrimSpace(strings.TrimPrefix(rest, id))
		if suffix != "" {
			if !strings.HasPrefix(suffix, "as ") && !strings.HasPrefix(suffix, "as\t") {
				return p.fail("expected participant alias: as Label")
			}
			label = strings.TrimSpace(suffix[2:])
			if label == "" {
				return p.fail("empty participant alias")
			}
		}
		if err := sequenceLabel(label); err != nil {
			return p.fail(err.Error())
		}
		i, err := p.participant(id)
		if err != nil {
			return err
		}
		p.diagram.participants[i].label = label
		return nil
	}
	switch word {
	case "alt", "opt", "loop":
		if rest == "" {
			return p.fail("control block requires a caption")
		}
		if len(p.blocks) >= maxSequenceDepth {
			return p.fail("control nesting limit exceeded")
		}
		depth := len(p.blocks)
		p.blocks = append(p.blocks, word)
		p.diagram.depth = max(p.diagram.depth, len(p.blocks))
		return p.append(sequenceEvent{kind: word, label: rest, depth: depth})
	case "else":
		if len(p.blocks) == 0 || p.blocks[len(p.blocks)-1] != "alt" {
			return p.fail("else requires an enclosing alt block")
		}
		return p.append(sequenceEvent{kind: "else", label: rest, depth: len(p.blocks) - 1})
	case "end":
		if rest != "" || len(p.blocks) == 0 {
			return p.fail("unexpected end")
		}
		p.blocks = p.blocks[:len(p.blocks)-1]
		return p.append(sequenceEvent{kind: "end", depth: len(p.blocks)})
	}
	if word == "Note" || word == "note" {
		return p.note(rest)
	}
	// Split at the first colon: subsequent colons are part of the message label.
	endpoints, label, ok := strings.Cut(line, ":")
	if !ok {
		return p.fail("unsupported sequence statement")
	}
	arrow := "->>"
	dashed := false
	if strings.Contains(endpoints, "-->>") {
		arrow = "-->>"
		dashed = true
	}
	from, to, ok := strings.Cut(endpoints, arrow)
	if !ok {
		return p.fail("supported message arrows are ->> and -->>")
	}
	a, err := p.participant(strings.TrimSpace(from))
	if err != nil {
		return err
	}
	b, err := p.participant(strings.TrimSpace(to))
	if err != nil {
		return err
	}
	return p.append(sequenceEvent{kind: "message", label: strings.TrimSpace(label), from: a, to: b, dashed: dashed})
}
func (p *sequenceParser) note(rest string) error {
	placement, label, ok := strings.Cut(rest, ":")
	if !ok || strings.TrimSpace(label) == "" {
		return p.fail("note requires a label")
	}
	fields := strings.Fields(placement)
	kind, targets := "", ""
	switch {
	case len(fields) >= 3 && fields[0] == "left" && fields[1] == "of":
		kind = "note-left"
		targets = strings.Join(fields[2:], " ")
	case len(fields) >= 3 && fields[0] == "right" && fields[1] == "of":
		kind = "note-right"
		targets = strings.Join(fields[2:], " ")
	case len(fields) >= 2 && fields[0] == "over":
		kind = "note-over"
		targets = strings.Join(fields[1:], " ")
	default:
		return p.fail("unsupported note placement")
	}
	ids := strings.Split(targets, ",")
	if len(ids) > 2 || kind != "note-over" && len(ids) != 1 {
		return p.fail("note requires one participant, or two for over")
	}
	a, err := p.participant(strings.TrimSpace(ids[0]))
	if err != nil {
		return err
	}
	b := a
	if len(ids) == 2 {
		b, err = p.participant(strings.TrimSpace(ids[1]))
		if err != nil {
			return err
		}
	}
	return p.append(sequenceEvent{kind: kind, label: strings.TrimSpace(label), from: a, to: b})
}
