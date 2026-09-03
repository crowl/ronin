package jsonschema_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/crowl/ronin/jsonschema"
)

func TestFromRawPreservesSchema(t *testing.T) {
	input := []byte(`{"type":"object","properties":{"value":{"oneOf":[{"type":"string"},{"type":"number"}]}},"required":["value"],"additionalProperties":false}`)
	schema, err := jsonschema.FromRaw(input)
	if err != nil {
		t.Fatalf("FromRaw() error = %v", err)
	}
	got, err := json.Marshal(schema)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if string(got) != string(input) {
		t.Fatalf("schema = %s, want %s", got, input)
	}
}

func TestFromRawRejectsInvalidSchema(t *testing.T) {
	for _, input := range [][]byte{[]byte(`not json`), []byte(`[]`)} {
		if _, err := jsonschema.FromRaw(input); err == nil {
			t.Fatalf("FromRaw(%q) error = nil", input)
		}
	}
}

func TestValidateDefinition(t *testing.T) {
	t.Run("rejects unsupported keywords", func(t *testing.T) {
		schema, err := jsonschema.FromRaw([]byte(`{"type":"object","properties":{"value":{"anyOf":[{"type":"string"},{"type":"number"}]}}}`))
		if err != nil {
			t.Fatalf("FromRaw() error = %v", err)
		}
		if err := jsonschema.ValidateDefinition(schema); err == nil || !strings.Contains(err.Error(), "$.properties.value.anyOf is not supported") {
			t.Fatalf("ValidateDefinition() error = %v", err)
		}
	})

	t.Run("rejects invalid supported keyword values", func(t *testing.T) {
		for name, input := range map[string]string{
			"type":                  `{"type":"date"}`,
			"required":              `{"type":"object","required":[1]}`,
			"additional properties": `{"type":"object","additionalProperties":{"type":"string"}}`,
			"unique items":          `{"type":"array","uniqueItems":"yes"}`,
		} {
			t.Run(name, func(t *testing.T) {
				schema, err := jsonschema.FromRaw([]byte(input))
				if err != nil {
					t.Fatalf("FromRaw() error = %v", err)
				}
				if err := jsonschema.ValidateDefinition(schema); err == nil {
					t.Fatal("ValidateDefinition() error = nil")
				}
			})
		}
	})
}

func TestValidate(t *testing.T) {
	schema, err := jsonschema.FromRaw([]byte(`{
		"type":"object",
		"additionalProperties":false,
		"required":["tasks"],
		"properties":{
			"tasks":{
				"type":"array",
				"minItems":1,
				"uniqueItems":true,
				"items":{
					"type":"object",
					"required":["id","commit_message"],
					"properties":{
						"id":{"type":"string","pattern":"^[a-z]+-[a-z-]+$"},
						"commit_message":{"type":"string","pattern":"^(feat|fix): [a-z].+[^.]$"}
					}
				}
			}
		}
	}`))
	if err != nil {
		t.Fatalf("FromRaw() error = %v", err)
	}

	if err := jsonschema.Validate(schema, []byte(`{"tasks":[{"id":"runtime-validation","commit_message":"fix: validate plans"}]}`)); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	for name, tc := range map[string]struct {
		document string
		want     string
	}{
		"placeholder":  {document: `{"tasks":[{"id":"string","commit_message":"string"}]}`, want: "$.tasks[0].id"},
		"pattern":      {document: `{"tasks":[{"id":"runtime-validation","commit_message":"Fix: Invalid."}]}`, want: "$.tasks[0].commit_message"},
		"missing":      {document: `{}`, want: "$.tasks: is required"},
		"extra":        {document: `{"tasks":[{"id":"runtime-validation","commit_message":"fix: validate plans"}],"extra":true}`, want: "$.extra: additional property"},
		"duplicate":    {document: `{"tasks":[{"id":"runtime-validation","commit_message":"fix: validate plans"},{"id":"runtime-validation","commit_message":"fix: validate plans"}]}`, want: "must be unique"},
		"invalid JSON": {document: `{`, want: "decode JSON document"},
	} {
		t.Run(name, func(t *testing.T) {
			err := jsonschema.Validate(schema, []byte(tc.document))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate() error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestFromType(t *testing.T) {
	t.Run("struct fields include required optional skipped and unexported", func(t *testing.T) {
		type args struct {
			Required string `json:"required" jsonschema:"required description"`
			Optional string `json:"optional,omitempty"`
			Pointer  *int   `json:"pointer"`
			Skipped  string `json:"-"`
			hidden   string
		}

		schema := jsonschema.FromType[args]()
		if schema.Type != "object" {
			t.Fatalf("Type = %q, want object", schema.Type)
		}
		if schema.AdditionalProperties != false {
			t.Fatalf("AdditionalProperties = %#v, want false", schema.AdditionalProperties)
		}
		if _, ok := schema.Properties["required"]; !ok {
			t.Fatal("required property missing")
		}
		if schema.Properties["required"].Description != "required description" {
			t.Fatalf("Description = %q, want required description", schema.Properties["required"].Description)
		}
		if _, ok := schema.Properties["optional"]; !ok {
			t.Fatal("optional property missing")
		}
		if _, ok := schema.Properties["pointer"]; !ok {
			t.Fatal("pointer property missing")
		}
		if _, ok := schema.Properties["Skipped"]; ok {
			t.Fatal("skipped property present")
		}
		if _, ok := schema.Properties["hidden"]; ok {
			t.Fatal("hidden property present")
		}
		assertRequired(t, schema.Required, []string{"required"})
	})

	t.Run("jsonschema dash tag skips field", func(t *testing.T) {
		type args struct {
			Visible string `json:"visible"`
			Hidden  string `json:"hidden,omitempty" jsonschema:"-"`
		}

		schema := jsonschema.FromType[args]()
		if _, ok := schema.Properties["visible"]; !ok {
			t.Fatal("visible property missing")
		}
		if _, ok := schema.Properties["hidden"]; ok {
			t.Fatal("hidden property present")
		}
		assertRequired(t, schema.Required, []string{"visible"})
	})

	t.Run("nested structs and slices", func(t *testing.T) {
		type child struct {
			Name string `json:"name"`
		}
		type args struct {
			Child child   `json:"child"`
			Items []child `json:"items"`
		}

		schema := jsonschema.FromType[args]()
		if schema.Properties["child"].Type != "object" {
			t.Fatalf("child Type = %q, want object", schema.Properties["child"].Type)
		}
		if schema.Properties["child"].Properties["name"].Type != "string" {
			t.Fatalf("child.name Type = %q, want string", schema.Properties["child"].Properties["name"].Type)
		}
		if schema.Properties["items"].Type != "array" {
			t.Fatalf("items Type = %q, want array", schema.Properties["items"].Type)
		}
		if schema.Properties["items"].Items.Properties["name"].Type != "string" {
			t.Fatalf("items.name Type = %q, want string", schema.Properties["items"].Items.Properties["name"].Type)
		}
	})

	t.Run("map string values use schema additional properties", func(t *testing.T) {
		type args struct {
			Env map[string]string `json:"env,omitempty"`
		}

		schema := jsonschema.FromType[args]()
		env := schema.Properties["env"]
		if env.Type != "object" {
			t.Fatalf("env Type = %q, want object", env.Type)
		}
		additional, ok := env.AdditionalProperties.(*jsonschema.Schema)
		if !ok {
			t.Fatalf("env AdditionalProperties = %T, want *jsonschema.Schema", env.AdditionalProperties)
		}
		if additional.Type != "string" {
			t.Fatalf("env additional Type = %q, want string", additional.Type)
		}
	})

	t.Run("unsupported map key falls back to empty schema", func(t *testing.T) {
		type args struct {
			Values map[int]string `json:"values"`
		}

		schema := jsonschema.FromType[args]()
		if schema.Properties["values"].Type != "" {
			t.Fatalf("values Type = %q, want empty", schema.Properties["values"].Type)
		}
	})

	t.Run("interfaces are empty schemas", func(t *testing.T) {
		type args struct {
			Value any `json:"value"`
		}

		schema := jsonschema.FromType[args]()
		if schema.Properties["value"].Type != "" {
			t.Fatalf("value Type = %q, want empty", schema.Properties["value"].Type)
		}
	})

	t.Run("nil type parameter is safe", func(t *testing.T) {
		schema := jsonschema.FromType[any]()
		if schema.Type != "" {
			t.Fatalf("Type = %q, want empty", schema.Type)
		}
	})

	t.Run("anonymous embedded structs are flattened", func(t *testing.T) {
		type base struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		type args struct {
			base
			Name string `json:"name,omitempty"`
			Path string `json:"path"`
		}

		schema := jsonschema.FromType[args]()
		if _, ok := schema.Properties["base"]; ok {
			t.Fatal("embedded base property present, want flattened")
		}
		if schema.Properties["id"].Type != "string" {
			t.Fatalf("id Type = %q, want string", schema.Properties["id"].Type)
		}
		if schema.Properties["name"].Type != "string" {
			t.Fatalf("name Type = %q, want string", schema.Properties["name"].Type)
		}
		assertRequired(t, schema.Required, []string{"id", "path"})
	})

	t.Run("json string option emits string for primitive", func(t *testing.T) {
		type args struct {
			Count int  `json:"count,string"`
			Flag  bool `json:"flag,string"`
		}

		schema := jsonschema.FromType[args]()
		if schema.Properties["count"].Type != "string" {
			t.Fatalf("count Type = %q, want string", schema.Properties["count"].Type)
		}
		if schema.Properties["flag"].Type != "string" {
			t.Fatalf("flag Type = %q, want string", schema.Properties["flag"].Type)
		}
	})

	t.Run("schema marshals map additional properties as object", func(t *testing.T) {
		type args struct {
			Env map[string]string `json:"env,omitempty"`
		}

		data, err := json.Marshal(jsonschema.FromType[args]())
		if err != nil {
			t.Fatalf("json.Marshal() error = %v", err)
		}
		if !json.Valid(data) {
			t.Fatalf("json.Marshal() = %q, want valid JSON", data)
		}
		if !strings.Contains(string(data), `"additionalProperties":{"type":"string"}`) {
			t.Fatalf("json.Marshal() = %s, want schema-valued additionalProperties", data)
		}
	})
}

func assertRequired(t *testing.T, got []string, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("required = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("required = %v, want %v", got, want)
		}
	}
}
