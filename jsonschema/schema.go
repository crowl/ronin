package jsonschema

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

type Schema struct {
	Type                 string             `json:"type,omitempty"`
	Description          string             `json:"description,omitempty"`
	Properties           map[string]*Schema `json:"properties,omitempty"`
	Required             []string           `json:"required,omitempty"`
	Items                *Schema            `json:"items,omitempty"`
	Enum                 []any              `json:"enum,omitempty"`
	AdditionalProperties any                `json:"additionalProperties,omitempty"`

	raw json.RawMessage
}

func FromRaw(data []byte) (*Schema, error) {
	if !json.Valid(data) {
		return nil, fmt.Errorf("invalid JSON schema")
	}
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, fmt.Errorf("decode JSON schema: %w", err)
	}
	if _, ok := value.(map[string]any); !ok {
		return nil, fmt.Errorf("JSON schema must be an object")
	}
	return &Schema{raw: append(json.RawMessage(nil), data...)}, nil
}

func (s Schema) MarshalJSON() ([]byte, error) {
	if len(s.raw) > 0 {
		return append([]byte(nil), s.raw...), nil
	}
	type schema Schema
	return json.Marshal(schema(s))
}

func FromType[T any]() *Schema {
	var zero T
	return fromReflectType(reflect.TypeOf(zero))
}

func fromReflectType(t reflect.Type) *Schema {
	if t == nil {
		return &Schema{}
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	switch t.Kind() {
	case reflect.Struct:
		return structSchema(t)
	case reflect.String:
		return &Schema{Type: "string"}
	case reflect.Bool:
		return &Schema{Type: "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return &Schema{Type: "integer"}
	case reflect.Float32, reflect.Float64:
		return &Schema{Type: "number"}
	case reflect.Slice, reflect.Array:
		return &Schema{
			Type:  "array",
			Items: fromReflectType(t.Elem()),
		}
	case reflect.Map:
		if t.Key().Kind() != reflect.String {
			return &Schema{}
		}
		return &Schema{
			Type:                 "object",
			AdditionalProperties: fromReflectType(t.Elem()),
		}
	default:
		return &Schema{}
	}
}

func structSchema(t reflect.Type) *Schema {
	s := &Schema{
		Type:                 "object",
		Properties:           map[string]*Schema{},
		AdditionalProperties: false,
	}

	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)

		if field.PkgPath != "" && !field.Anonymous {
			continue
		}

		if strings.TrimSpace(field.Tag.Get("jsonschema")) == "-" {
			continue
		}

		name, options, skip := parseJSONTag(field)
		if skip {
			continue
		}

		if shouldFlattenField(field, name) {
			mergeEmbeddedStruct(s, field.Type)
			continue
		}

		if name == "" {
			name = field.Name
		}

		fieldSchema := fromReflectType(field.Type)
		if options.asString && canRepresentAsJSONString(field.Type) {
			fieldSchema = &Schema{Type: "string"}
		}

		if desc := strings.TrimSpace(field.Tag.Get("jsonschema")); desc != "" {
			fieldSchema.Description = desc
		}

		s.Properties[name] = fieldSchema

		if !options.omitempty && field.Type.Kind() != reflect.Pointer {
			found := false
			for _, required := range s.Required {
				if required == name {
					found = true
					break
				}
			}
			if !found {
				s.Required = append(s.Required, name)
			}
		} else {
			for i, required := range s.Required {
				if required == name {
					s.Required = append(s.Required[:i], s.Required[i+1:]...)
					break
				}
			}
		}
	}

	return s
}

func shouldFlattenField(field reflect.StructField, jsonName string) bool {
	if !field.Anonymous || jsonName != "" {
		return false
	}
	fieldType := field.Type
	for fieldType.Kind() == reflect.Pointer {
		fieldType = fieldType.Elem()
	}
	return fieldType.Kind() == reflect.Struct
}

func mergeEmbeddedStruct(parent *Schema, fieldType reflect.Type) {
	embedded := fromReflectType(fieldType)
	for name, property := range embedded.Properties {
		if _, exists := parent.Properties[name]; !exists {
			parent.Properties[name] = property
		}
	}
	for _, name := range embedded.Required {
		if _, exists := parent.Properties[name]; !exists {
			continue
		}

		found := false
		for _, required := range parent.Required {
			if required == name {
				found = true
				break
			}
		}
		if !found {
			parent.Required = append(parent.Required, name)
		}
	}
}

func canRepresentAsJSONString(t reflect.Type) bool {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.String, reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return true
	default:
		return false
	}
}

type tagOptions struct {
	omitempty bool
	asString  bool
}

func parseJSONTag(field reflect.StructField) (name string, options tagOptions, skip bool) {
	tag := field.Tag.Get("json")
	if tag == "-" {
		return "", tagOptions{}, true
	}
	if tag == "" {
		return "", tagOptions{}, false
	}

	parts := strings.Split(tag, ",")
	name = parts[0]

	for _, part := range parts[1:] {
		switch part {
		case "omitempty":
			options.omitempty = true
		case "string":
			options.asString = true
		}
	}

	return name, options, false
}
