package capsule

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"unicode/utf8"
)

type complexityLimits struct {
	maxDepth, maxStringBytes, maxKeys int
}

func parseStrictJSON(raw []byte, limits complexityLimits) (any, int, error) {
	if !utf8.Valid(raw) {
		return nil, 0, errors.New("JSON is not UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	keys := 0
	value, err := decodeJSONValue(decoder, 1, limits, &keys)
	if err != nil {
		return nil, 0, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, 0, errors.New("JSON has trailing data")
	}
	return value, keys, nil
}

func decodeJSONValue(decoder *json.Decoder, depth int, limits complexityLimits, keys *int) (any, error) {
	if depth > limits.maxDepth {
		return nil, errors.New("JSON depth exceeds limit")
	}
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if delimiter, composite := token.(json.Delim); composite {
		switch delimiter {
		case '{':
			object := map[string]any{}
			for decoder.More() {
				nameToken, err := decoder.Token()
				if err != nil {
					return nil, err
				}
				name, ok := nameToken.(string)
				if !ok || !utf8.ValidString(name) || len([]byte(name)) > limits.maxStringBytes {
					return nil, errors.New("JSON member name is invalid")
				}
				if _, duplicate := object[name]; duplicate {
					return nil, errors.New("duplicate JSON member")
				}
				*keys++
				if *keys > limits.maxKeys {
					return nil, errors.New("JSON key count exceeds limit")
				}
				value, err := decodeJSONValue(decoder, depth+1, limits, keys)
				if err != nil {
					return nil, err
				}
				object[name] = value
			}
			closing, err := decoder.Token()
			if err != nil || closing != json.Delim('}') {
				return nil, errors.New("invalid JSON object")
			}
			return object, nil
		case '[':
			array := []any{}
			for decoder.More() {
				value, err := decodeJSONValue(decoder, depth+1, limits, keys)
				if err != nil {
					return nil, err
				}
				array = append(array, value)
			}
			closing, err := decoder.Token()
			if err != nil || closing != json.Delim(']') {
				return nil, errors.New("invalid JSON array")
			}
			return array, nil
		default:
			return nil, errors.New("unexpected JSON delimiter")
		}
	}
	switch value := token.(type) {
	case string:
		if !utf8.ValidString(value) || len([]byte(value)) > limits.maxStringBytes {
			return nil, errors.New("JSON string exceeds limit")
		}
	case json.Number:
		if _, err := value.Int64(); err != nil {
			parsed, parseErr := value.Float64()
			if parseErr != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
				return nil, errors.New("invalid JSON number")
			}
		}
	}
	return token, nil
}

func canonicalJSON(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	encoded := bytes.TrimSuffix(buffer.Bytes(), []byte{'\n'})
	return unescapeJSONLineSeparators(encoded), nil
}

func unescapeJSONLineSeparators(encoded []byte) []byte {
	result := make([]byte, 0, len(encoded))
	for index := 0; index < len(encoded); {
		if encoded[index] == '\\' && index+6 <= len(encoded) &&
			(string(encoded[index:index+6]) == `\u2028` || string(encoded[index:index+6]) == `\u2029`) {
			preceding := 0
			for cursor := index - 1; cursor >= 0 && encoded[cursor] == '\\'; cursor-- {
				preceding++
			}
			if preceding%2 == 0 {
				if encoded[index+5] == '8' {
					result = append(result, []byte("\u2028")...)
				} else {
					result = append(result, []byte("\u2029")...)
				}
				index += 6
				continue
			}
		}
		result = append(result, encoded[index])
		index++
	}
	return result
}

func normalizeJSONObject(value map[string]any, maximum int, limits complexityLimits) (map[string]any, error) {
	if value == nil {
		value = map[string]any{}
	}
	encoded, err := canonicalJSON(value)
	if err != nil || len(encoded) > maximum {
		return nil, errors.New("JSON object is invalid")
	}
	normalized, _, err := parseStrictJSON(encoded, limits)
	if err != nil {
		return nil, err
	}
	object, ok := normalized.(map[string]any)
	if !ok {
		return nil, errors.New("JSON value must be an object")
	}
	return object, nil
}

func cloneJSON(value any, limits complexityLimits) (any, error) {
	encoded, err := canonicalJSON(value)
	if err != nil {
		return nil, err
	}
	result, _, err := parseStrictJSON(encoded, limits)
	return result, err
}

func sameJSON(left, right any) bool {
	a, err := canonicalJSON(left)
	if err != nil {
		return false
	}
	b, err := canonicalJSON(right)
	return err == nil && bytes.Equal(a, b)
}

func jsonNumber(value any) (float64, bool) {
	switch item := value.(type) {
	case json.Number:
		parsed, err := item.Float64()
		return parsed, err == nil
	case int:
		return float64(item), true
	case int32:
		return float64(item), true
	case int64:
		return float64(item), true
	case float64:
		return item, !math.IsNaN(item) && !math.IsInf(item, 0)
	default:
		return 0, false
	}
}

func jsonInteger(value any) (int64, bool) {
	switch item := value.(type) {
	case json.Number:
		parsed, err := item.Int64()
		return parsed, err == nil
	case int:
		return int64(item), true
	case int32:
		return int64(item), true
	case int64:
		return item, true
	default:
		return 0, false
	}
}

type compiledSchema struct {
	types                map[string]struct{}
	properties           map[string]*compiledSchema
	required             map[string]struct{}
	additionalProperties bool
	items                *compiledSchema
	minItems, maxItems   *int
	minLength, maxLength *int
	minimum, maximum     *float64
	pattern              *regexp.Regexp
	enum                 []any
	constant             any
	hasConstant          bool
}

func compileSchema(raw map[string]any) (*compiledSchema, error) {
	normalized, err := normalizeJSONObject(raw, 1_048_576, complexityLimits{64, 1_048_576, 100_000})
	if err != nil {
		return nil, err
	}
	return compileSchemaObject(normalized)
}

func compileSchemaObject(raw map[string]any) (*compiledSchema, error) {
	allowed := map[string]struct{}{
		"$schema": {}, "$id": {}, "$comment": {}, "title": {}, "description": {},
		"default": {}, "examples": {}, "deprecated": {}, "readOnly": {}, "writeOnly": {},
		"type": {}, "properties": {}, "required": {}, "additionalProperties": {}, "items": {},
		"minItems": {}, "maxItems": {}, "minLength": {}, "maxLength": {}, "minimum": {}, "maximum": {},
		"pattern": {}, "format": {}, "enum": {}, "const": {},
	}
	for name := range raw {
		if _, ok := allowed[name]; !ok {
			return nil, fmt.Errorf("unsupported JSON Schema keyword %q", name)
		}
	}
	schema := &compiledSchema{additionalProperties: true}
	if value, exists := raw["type"]; exists {
		schema.types = map[string]struct{}{}
		items := []any{value}
		if array, ok := value.([]any); ok {
			items = array
		}
		if len(items) == 0 {
			return nil, errors.New("JSON Schema type is invalid")
		}
		for _, item := range items {
			name, ok := item.(string)
			if !ok || !validJSONType(name) {
				return nil, errors.New("JSON Schema type is invalid")
			}
			schema.types[name] = struct{}{}
		}
	}
	if value, exists := raw["properties"]; exists {
		properties, ok := value.(map[string]any)
		if !ok {
			return nil, errors.New("JSON Schema properties are invalid")
		}
		schema.properties = make(map[string]*compiledSchema, len(properties))
		for name, item := range properties {
			object, ok := item.(map[string]any)
			if !ok {
				return nil, errors.New("JSON Schema property is invalid")
			}
			compiled, err := compileSchemaObject(object)
			if err != nil {
				return nil, err
			}
			schema.properties[name] = compiled
		}
	}
	if value, exists := raw["required"]; exists {
		items, ok := value.([]any)
		if !ok {
			return nil, errors.New("JSON Schema required is invalid")
		}
		schema.required = map[string]struct{}{}
		for _, item := range items {
			name, ok := item.(string)
			if !ok || name == "" {
				return nil, errors.New("JSON Schema required is invalid")
			}
			if _, duplicate := schema.required[name]; duplicate {
				return nil, errors.New("JSON Schema required is invalid")
			}
			schema.required[name] = struct{}{}
		}
	}
	if value, exists := raw["additionalProperties"]; exists {
		allowed, ok := value.(bool)
		if !ok {
			return nil, errors.New("JSON Schema additionalProperties is invalid")
		}
		schema.additionalProperties = allowed
	}
	if value, exists := raw["items"]; exists {
		object, ok := value.(map[string]any)
		if !ok {
			return nil, errors.New("JSON Schema items are invalid")
		}
		var err error
		schema.items, err = compileSchemaObject(object)
		if err != nil {
			return nil, err
		}
	}
	for name, target := range map[string]**int{
		"minItems": &schema.minItems, "maxItems": &schema.maxItems,
		"minLength": &schema.minLength, "maxLength": &schema.maxLength,
	} {
		if value, exists := raw[name]; exists {
			number, ok := jsonInteger(value)
			if !ok || number < 0 || number > math.MaxInt {
				return nil, fmt.Errorf("JSON Schema %s is invalid", name)
			}
			converted := int(number)
			*target = &converted
		}
	}
	for name, target := range map[string]**float64{"minimum": &schema.minimum, "maximum": &schema.maximum} {
		if value, exists := raw[name]; exists {
			number, ok := jsonNumber(value)
			if !ok {
				return nil, fmt.Errorf("JSON Schema %s is invalid", name)
			}
			*target = &number
		}
	}
	if value, exists := raw["pattern"]; exists {
		pattern, ok := value.(string)
		if !ok {
			return nil, errors.New("JSON Schema pattern is invalid")
		}
		var err error
		schema.pattern, err = regexp.Compile(pattern)
		if err != nil {
			return nil, errors.New("JSON Schema pattern is invalid")
		}
	}
	if value, exists := raw["format"]; exists {
		if _, ok := value.(string); !ok {
			return nil, errors.New("JSON Schema format is invalid")
		}
	}
	if value, exists := raw["enum"]; exists {
		schema.enum, _ = value.([]any)
		if len(schema.enum) == 0 {
			return nil, errors.New("JSON Schema enum is invalid")
		}
	}
	if value, exists := raw["const"]; exists {
		schema.constant, schema.hasConstant = value, true
	}
	return schema, nil
}

func (schema *compiledSchema) valid(value any) bool {
	if schema == nil {
		return false
	}
	if len(schema.types) != 0 {
		matched := false
		for name := range schema.types {
			if matchesJSONType(value, name) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	if schema.hasConstant && !sameJSON(value, schema.constant) {
		return false
	}
	if len(schema.enum) != 0 {
		matched := false
		for _, candidate := range schema.enum {
			if sameJSON(value, candidate) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	switch item := value.(type) {
	case map[string]any:
		for name := range schema.required {
			if _, exists := item[name]; !exists {
				return false
			}
		}
		for name, child := range item {
			property, exists := schema.properties[name]
			if !exists {
				if !schema.additionalProperties {
					return false
				}
				continue
			}
			if !property.valid(child) {
				return false
			}
		}
	case []any:
		if schema.minItems != nil && len(item) < *schema.minItems || schema.maxItems != nil && len(item) > *schema.maxItems {
			return false
		}
		if schema.items != nil {
			for _, child := range item {
				if !schema.items.valid(child) {
					return false
				}
			}
		}
	case string:
		length := utf8.RuneCountInString(item)
		if schema.minLength != nil && length < *schema.minLength || schema.maxLength != nil && length > *schema.maxLength ||
			schema.pattern != nil && !schema.pattern.MatchString(item) {
			return false
		}
	default:
		if number, ok := jsonNumber(item); ok {
			if schema.minimum != nil && number < *schema.minimum || schema.maximum != nil && number > *schema.maximum {
				return false
			}
		}
	}
	return true
}

func validJSONType(value string) bool {
	switch value {
	case "null", "boolean", "object", "array", "number", "integer", "string":
		return true
	default:
		return false
	}
}

func matchesJSONType(value any, expected string) bool {
	switch expected {
	case "null":
		return value == nil
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "number":
		_, ok := jsonNumber(value)
		return ok
	case "integer":
		_, ok := jsonInteger(value)
		return ok
	case "string":
		_, ok := value.(string)
		return ok
	default:
		return false
	}
}

func numberInt(value any, label string) (int, error) {
	number, ok := jsonInteger(value)
	if !ok || number < 0 || number > 1_000_000_000 {
		return 0, fmt.Errorf("provider %s is invalid", label)
	}
	return int(number), nil
}

func valueString(value any) string {
	switch item := value.(type) {
	case string:
		return item
	case json.Number:
		return item.String()
	default:
		return fmt.Sprint(item)
	}
}

func parseFloat(raw string) (float64, bool) {
	value, err := strconv.ParseFloat(raw, 64)
	return value, err == nil && !math.IsNaN(value) && !math.IsInf(value, 0)
}
