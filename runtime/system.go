package runtime

import (
	"bytes"
	"strings"
	"text/template"

	_ "embed"
)

var (
	//go:embed system_prompt.tmpl
	systemPromptTemplateText string

	systemPromptTemplate = template.Must(template.New("prompt.system").Parse(systemPromptTemplateText))
)

type SystemPromptInput struct {
	CWD             string
	ContextFiles    []ContextFile
	Skills          []Skill
	MCPInstructions []MCPInstruction
}

type MCPInstruction struct {
	Server  string
	Content string
}

func BuildSystemPrompt(input SystemPromptInput) (string, error) {
	// Filter into a fresh slice so rendering does not mutate the caller's input.
	instructions := make([]MCPInstruction, 0, len(input.MCPInstructions))
	for _, instruction := range input.MCPInstructions {
		instruction.Content = strings.TrimSpace(instruction.Content)
		if instruction.Content != "" {
			instructions = append(instructions, instruction)
		}
	}
	input.MCPInstructions = instructions
	var b bytes.Buffer
	if err := systemPromptTemplate.Execute(&b, input); err != nil {
		return "", err
	}
	return strings.TrimSpace(b.String()), nil
}
