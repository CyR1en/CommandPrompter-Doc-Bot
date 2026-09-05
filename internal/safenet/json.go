package safenet

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
)

func ParseBoundedJSON(raw []byte) (any, error) {
	if len(raw) > MaxBodyBytes {
		return nil, requestError(ResponseTooLarge)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	value, err := decodeValue(decoder, 1)
	if err != nil {
		return nil, requestError(InvalidJSON)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, requestError(InvalidJSON)
	}
	return value, nil
}

func decodeValue(decoder *json.Decoder, depth int) (any, error) {
	if depth > MaxJSONDepth {
		return nil, errors.New("JSON is too deep")
	}
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		switch value := token.(type) {
		case json.Number:
			if _, err := value.Int64(); err != nil {
				parsed, parseErr := value.Float64()
				if parseErr != nil || math.IsInf(parsed, 0) || math.IsNaN(parsed) {
					return nil, errors.New("invalid JSON number")
				}
			}
		}
		return token, nil
	}
	switch delimiter {
	case '{':
		object := map[string]any{}
		for decoder.More() {
			nameToken, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			name, ok := nameToken.(string)
			if !ok {
				return nil, errors.New("invalid JSON member")
			}
			if _, duplicate := object[name]; duplicate {
				return nil, errors.New("duplicate JSON member")
			}
			value, err := decodeValue(decoder, depth+1)
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
			value, err := decodeValue(decoder, depth+1)
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
		return nil, fmt.Errorf("unexpected JSON delimiter %q", delimiter)
	}
}

func ValidateModelCatalog(payload any) ([]map[string]any, error) {
	object, ok := payload.(map[string]any)
	if !ok {
		return nil, requestError(InvalidJSON)
	}
	items, ok := object["data"].([]any)
	if !ok || len(items) > MaxModels {
		return nil, requestError(InvalidJSON)
	}
	models := make([]map[string]any, 0, len(items))
	for _, item := range items {
		model, ok := item.(map[string]any)
		if !ok {
			return nil, requestError(InvalidJSON)
		}
		id, ok := model["id"].(string)
		if !ok || len(bytes.TrimSpace([]byte(id))) == 0 {
			return nil, requestError(InvalidJSON)
		}
		models = append(models, model)
	}
	return models, nil
}
