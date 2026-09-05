package capsule

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"regexp"
	"unicode/utf8"

	"github.com/cyr1en/ref0/internal/safenet"
)

var operationID = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

type wire struct {
	connection net.Conn
	limits     Limits
	aggregate  int
}

func newWire(connection net.Conn, limits Limits) *wire {
	return &wire{connection: connection, limits: limits}
}

func (transport *wire) send(message map[string]any) error {
	if err := validateWireMessage(message); err != nil {
		return err
	}
	encoded, err := canonicalJSON(message)
	if err != nil || len(encoded) == 0 || len(encoded) > transport.limits.MaxFrameBytes {
		return errors.New("capsule outbound frame exceeds limit")
	}
	if _, _, err := parseStrictJSON(encoded, complexityLimits{
		transport.limits.MaxDepth, transport.limits.MaxStringBytes, transport.limits.MaxKeys,
	}); err != nil {
		return errors.New("capsule outbound message exceeds JSON limits")
	}
	transport.aggregate += len(encoded) + 4
	if transport.aggregate > transport.limits.MaxAggregateBytes {
		return errors.New("capsule protocol aggregate exceeds limit")
	}
	frame := make([]byte, len(encoded)+4)
	binary.BigEndian.PutUint32(frame, uint32(len(encoded)))
	copy(frame[4:], encoded)
	return writeFull(transport.connection, frame)
}

func (transport *wire) receive() (map[string]any, error) {
	var header [4]byte
	if _, err := io.ReadFull(transport.connection, header[:]); err != nil {
		return nil, err
	}
	length := int(binary.BigEndian.Uint32(header[:]))
	if length < 1 || length > transport.limits.MaxFrameBytes {
		return nil, errors.New("capsule inbound frame exceeds limit")
	}
	transport.aggregate += length + 4
	if transport.aggregate > transport.limits.MaxAggregateBytes {
		return nil, errors.New("capsule protocol aggregate exceeds limit")
	}
	encoded := make([]byte, length)
	if _, err := io.ReadFull(transport.connection, encoded); err != nil {
		return nil, err
	}
	value, _, err := parseStrictJSON(encoded, complexityLimits{
		transport.limits.MaxDepth, transport.limits.MaxStringBytes, transport.limits.MaxKeys,
	})
	if err != nil {
		return nil, errors.New("capsule protocol JSON is invalid")
	}
	message, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("capsule protocol message must be an object")
	}
	if err := validateWireMessage(message); err != nil {
		return nil, err
	}
	return message, nil
}

func writeFull(writer io.Writer, value []byte) error {
	for len(value) != 0 {
		written, err := writer.Write(value)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		value = value[written:]
	}
	return nil
}

func validateWireMessage(message map[string]any) error {
	typeName, ok := message["type"].(string)
	if !ok {
		return errors.New("capsule protocol schema rejected a message")
	}
	var required, optional map[string]wireField
	switch typeName {
	case "start":
		required = map[string]wireField{
			"type": exactString("start"), "protocol_version": exactInteger(1), "attempt_id": operationIDField(),
			"role": enumString("PLANNER", "PAGE_WRITER"), "system_prompt": stringField(0, -1), "prompt": stringField(0, -1),
			"tools": toolArrayField(), "output_schema": objectField(), "provider": providerField(), "limits": limitsField(),
		}
	case "tool_result":
		required = map[string]wireField{
			"type": exactString("tool_result"), "id": operationIDField(), "result": anyField(), "content": stringField(0, -1),
		}
	case "model_result":
		required = map[string]wireField{
			"type": exactString("model_result"), "id": operationIDField(), "body_base64": stringField(0, -1),
		}
	case "cancel":
		required = map[string]wireField{"type": exactString("cancel"), "reason": stringField(0, 256)}
	case "tool_call":
		required = map[string]wireField{
			"type": exactString("tool_call"), "id": operationIDField(), "provider_call_id": operationIDField(),
			"name": stringField(1, 128), "arguments": objectField(),
		}
	case "model_request":
		required = map[string]wireField{
			"type": exactString("model_request"), "id": operationIDField(), "turn": integerRange(1, 1_000),
		}
	case "complete":
		required = map[string]wireField{"type": exactString("complete"), "output": objectField()}
	case "failed":
		required = map[string]wireField{
			"type": exactString("failed"), "code": enumString("cancelled", "protocol", "provider", "tool", "invalid_result", "internal"),
			"message": stringField(1, 400),
		}
	default:
		return errors.New("capsule protocol schema rejected a message")
	}
	for name, validator := range required {
		value, exists := message[name]
		if !exists || !validator(value) {
			return errors.New("capsule protocol schema rejected a message")
		}
	}
	for name, value := range message {
		validator, exists := required[name]
		if !exists {
			validator, exists = optional[name]
		}
		if !exists || !validator(value) {
			return errors.New("capsule protocol schema rejected a message")
		}
	}
	return nil
}

type wireField func(any) bool

func anyField() wireField { return func(any) bool { return true } }
func objectField() wireField {
	return func(value any) bool { _, ok := value.(map[string]any); return ok }
}
func exactString(want string) wireField {
	return func(value any) bool { item, ok := value.(string); return ok && item == want }
}
func exactInteger(want int64) wireField {
	return func(value any) bool { item, ok := jsonInteger(value); return ok && item == want }
}
func integerRange(low, high int64) wireField {
	return func(value any) bool { item, ok := jsonInteger(value); return ok && item >= low && item <= high }
}
func stringField(minimum, maximum int) wireField {
	return func(value any) bool {
		item, ok := value.(string)
		length := utf8.RuneCountInString(item)
		return ok && length >= minimum && (maximum < 0 || length <= maximum)
	}
}
func enumString(values ...string) wireField {
	allowed := set(values...)
	return func(value any) bool { item, ok := value.(string); _, exists := allowed[item]; return ok && exists }
}
func operationIDField() wireField {
	return func(value any) bool { item, ok := value.(string); return ok && validOperationID(item) }
}

func validOperationID(value string) bool { return operationID.MatchString(value) }

func toolArrayField() wireField {
	return func(value any) bool {
		items, ok := value.([]any)
		if !ok || len(items) > 64 {
			return false
		}
		for _, item := range items {
			tool, ok := item.(map[string]any)
			if !ok || len(tool) != 3 || !stringField(1, 128)(tool["name"]) || !stringField(0, 4_096)(tool["description"]) || !objectField()(tool["parameters"]) {
				return false
			}
			for name := range tool {
				if name != "name" && name != "description" && name != "parameters" {
					return false
				}
			}
		}
		return true
	}
}

func providerField() wireField {
	return func(value any) bool {
		provider, ok := value.(map[string]any)
		if !ok || len(provider) != 7 {
			return false
		}
		fields := map[string]wireField{
			"model_id": stringField(1, 512), "body_options": samplingField(),
			"reasoning_effort": enumString("none", "minimal", "low", "medium", "high", "max"),
			"context_window":   integerRange(1_024, 10_000_000), "max_output_tokens": integerRange(1, 1_000_000),
			"timeout_ms": integerRange(safenet.MinModelTimeout.Milliseconds(), safenet.MaxModelTimeout.Milliseconds()), "capsule_runtime_revision": stringField(1, 128),
		}
		for name, validator := range fields {
			if !validator(provider[name]) {
				return false
			}
		}
		for name := range provider {
			if _, exists := fields[name]; !exists {
				return false
			}
		}
		return true
	}
}

func samplingField() wireField {
	return func(value any) bool {
		object, ok := value.(map[string]any)
		if !ok {
			return false
		}
		_, err := validateSamplingOptions(object)
		return err == nil
	}
}

func limitsField() wireField {
	return func(value any) bool {
		object, ok := value.(map[string]any)
		if !ok || len(object) != 8 {
			return false
		}
		fields := map[string]wireField{
			"max_frame_bytes": integerRange(1_024, 4_194_304), "max_aggregate_bytes": integerRange(4_096, 33_554_432),
			"max_string_bytes": integerRange(256, 2_097_152), "max_depth": integerRange(4, 64),
			"max_keys": integerRange(32, 100_000), "max_fetch_body_bytes": integerRange(1_024, 1_048_576),
			"max_fetches": integerRange(1, 1_000), "max_model_requests": integerRange(1, 1_000),
		}
		for name, validator := range fields {
			if !validator(object[name]) {
				return false
			}
		}
		for name := range object {
			if _, exists := fields[name]; !exists {
				return false
			}
		}
		return true
	}
}
