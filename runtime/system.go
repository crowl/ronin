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
	CWD          string
	ContextFiles []ContextFile
	Skills       []Skill
}

func BuildSystemPrompt(input SystemPromptInput) (string, error) {
	var b bytes.Buffer
	if err := systemPromptTemplate.Execute(&b, input); err != nil {
		return "", err
	}
	return strings.TrimSpace(b.String()), nil
}
