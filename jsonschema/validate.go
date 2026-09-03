package jsonschema

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"
)

func ValidateDefinition(schema *Schema) error {
	if schema == nil {
		return fmt.Errorf("schema is required")
	}
	schemaJSON, err := json.Marshal(schema)
	if err != nil {
		return fmt.Errorf("encode JSON schema: %w", err)
	}
	var definition map[string]any
	if err := decodeJSON(schemaJSON, &definition); err != nil {
		return fmt.Errorf("decode JSON schema: %w", err)
	}
	return validateSchemaDefinition(definition, "$")
}

// Validate checks document against the JSON Schema keywords used by Ronin.
func Validate(schema *Schema, document []byte) error {
	if schema == nil {
		return fmt.Errorf("schema is required")
	}

	schemaJSON, err := json.Marshal(schema)
	if err != nil {
		return fmt.Errorf("encode JSON schema: %w", err)
	}
	var definition map[string]any
	if err := decodeJSON(schemaJSON, &definition); err != nil {
		return fmt.Errorf("decode JSON schema: %w", err)
	}
	if err := validateSchemaDefinition(definition, "$"); err != nil {
		return fmt.Errorf("invalid JSON schema: %w", err)
	}
	var value any
	if err := decodeJSON(document, &value); err != nil {
		return fmt.Errorf("decode JSON document: %w", err)
	}

	violations, err := validateValue(definition, value, "$")
	if err != nil {
		return fmt.Errorf("validate JSON schema: %w", err)
	}
	if len(violations) > 0 {
		return fmt.Errorf("JSON document does not match schema: %s", strings.Join(violations, "; "))
	}
	return nil
}

func validateSchemaDefinition(schema map[string]any, path string) error {
	if pattern, ok := schema["pattern"].(string); ok {
		if _, err := regexp.Compile(pattern); err != nil {
			return fmt.Errorf("%s.pattern is invalid: %w", path, err)
		}
	}
	for _, keyword := range []string{"minItems", "maxItems", "minLength"} {
		if _, _, err := nonNegativeInteger(schema, keyword); err != nil {
			return fmt.Errorf("%s.%s: %w", path, keyword, err)
		}
	}
	if properties, ok := schema["properties"].(map[string]any); ok {
		for name, property := range properties {
			definition, ok := property.(map[string]any)
			if !ok {
				return fmt.Errorf("%s.properties.%s must be an object", path, name)
			}
			if err := validateSchemaDefinition(definition, path+".properties."+name); err != nil {
				return err
			}
		}
	}
	if item, ok := schema["items"]; ok {
		definition, ok := item.(map[string]any)
		if !ok {
			return fmt.Errorf("%s.items must be an object", path)
		}
		if err := validateSchemaDefinition(definition, path+".items"); err != nil {
			return err
		}
	}
	return nil
}

func decodeJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}

func validateValue(schema map[string]any, value any, path string) ([]string, error) {
	if expected, ok := schema["type"].(string); ok && !matchesType(expected, value) {
		return []string{fmt.Sprintf("%s: must be %s", path, expected)}, nil
	}
	if allowed, ok := schema["enum"].([]any); ok && !containsJSONValue(allowed, value) {
		return []string{path + ": must equal one of the allowed values"}, nil
	}

	switch value := value.(type) {
	case map[string]any:
		return validateObject(schema, value, path)
	case []any:
		return validateArray(schema, value, path)
	case string:
		return validateString(schema, value, path)
	default:
		return nil, nil
	}
}

func validateObject(schema map[string]any, value map[string]any, path string) ([]string, error) {
	var violations []string
	if required, ok := schema["required"].([]any); ok {
		for _, item := range required {
			name, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("required must contain strings")
			}
			if _, exists := value[name]; !exists {
				violations = append(violations, childPath(path, name)+": is required")
			}
		}
	}

	properties, _ := schema["properties"].(map[string]any)
	for name, property := range properties {
		item, exists := value[name]
		if !exists {
			continue
		}
		propertySchema, ok := property.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("property %q schema must be an object", name)
		}
		found, err := validateValue(propertySchema, item, childPath(path, name))
		if err != nil {
			return nil, err
		}
		violations = append(violations, found...)
	}

	if additional, ok := schema["additionalProperties"].(bool); ok && !additional {
		for name := range value {
			if _, exists := properties[name]; !exists {
				violations = append(violations, childPath(path, name)+": additional property is not allowed")
			}
		}
	}
	return violations, nil
}

func validateArray(schema map[string]any, value []any, path string) ([]string, error) {
	var violations []string
	if minimum, ok, err := nonNegativeInteger(schema, "minItems"); err != nil {
		return nil, err
	} else if ok && len(value) < minimum {
		violations = append(violations, fmt.Sprintf("%s: must contain at least %d items", path, minimum))
	}
	if maximum, ok, err := nonNegativeInteger(schema, "maxItems"); err != nil {
		return nil, err
	} else if ok && len(value) > maximum {
		violations = append(violations, fmt.Sprintf("%s: must contain at most %d items", path, maximum))
	}
	if unique, _ := schema["uniqueItems"].(bool); unique {
		for i := range value {
			if containsJSONValue(value[:i], value[i]) {
				violations = append(violations, fmt.Sprintf("%s[%d]: must be unique", path, i))
			}
		}
	}
	if item, ok := schema["items"]; ok {
		itemSchema, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("items schema must be an object")
		}
		for i, value := range value {
			found, err := validateValue(itemSchema, value, fmt.Sprintf("%s[%d]", path, i))
			if err != nil {
				return nil, err
			}
			violations = append(violations, found...)
		}
	}
	return violations, nil
}

func validateString(schema map[string]any, value, path string) ([]string, error) {
	var violations []string
	length := utf8.RuneCountInString(value)
	if minimum, ok, err := nonNegativeInteger(schema, "minLength"); err != nil {
		return nil, err
	} else if ok && length < minimum {
		violations = append(violations, fmt.Sprintf("%s: must contain at least %d characters", path, minimum))
	}
	if pattern, ok := schema["pattern"].(string); ok {
		expression, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("invalid pattern %q: %w", pattern, err)
		}
		if !expression.MatchString(value) {
			violations = append(violations, fmt.Sprintf("%s: must match pattern %q", path, pattern))
		}
	}
	return violations, nil
}

func matchesType(expected string, value any) bool {
	switch expected {
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "string":
		_, ok := value.(string)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "number":
		_, ok := value.(json.Number)
		return ok
	case "integer":
		number, ok := value.(json.Number)
		return ok && !strings.ContainsAny(number.String(), ".eE")
	case "null":
		return value == nil
	default:
		return false
	}
}

func nonNegativeInteger(schema map[string]any, name string) (int, bool, error) {
	value, exists := schema[name]
	if !exists {
		return 0, false, nil
	}
	number, ok := value.(json.Number)
	if !ok {
		return 0, false, fmt.Errorf("%s must be a non-negative integer", name)
	}
	integer, err := number.Int64()
	if err != nil || integer < 0 {
		return 0, false, fmt.Errorf("%s must be a non-negative integer", name)
	}
	return int(integer), true, nil
}

func containsJSONValue(values []any, target any) bool {
	targetJSON, err := json.Marshal(target)
	if err != nil {
		return false
	}
	for _, value := range values {
		valueJSON, err := json.Marshal(value)
		if err == nil && bytes.Equal(valueJSON, targetJSON) {
			return true
		}
	}
	return false
}

func childPath(path, name string) string {
	if regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`).MatchString(name) {
		return path + "." + name
	}
	encoded, _ := json.Marshal(name)
	return path + "[" + string(encoded) + "]"
}
