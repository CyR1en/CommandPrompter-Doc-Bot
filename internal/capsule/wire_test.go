package capsule

import (
	"encoding/binary"
	"net"
	"testing"
)

func TestWireUsesBoundedFourByteClosedFrames(t *testing.T) {
	limits := DefaultLimits()
	host, child := net.Pipe()
	defer host.Close()
	defer child.Close()
	received := make(chan map[string]any, 1)
	errors := make(chan error, 1)
	go func() {
		message, err := newWire(host, limits).receive()
		if err != nil {
			errors <- err
			return
		}
		received <- message
	}()
	message := map[string]any{"type": "model_request", "id": "model-1", "turn": 1}
	encoded, _ := canonicalJSON(message)
	frame := make([]byte, len(encoded)+4)
	binary.BigEndian.PutUint32(frame, uint32(len(encoded)))
	copy(frame[4:], encoded)
	if _, err := child.Write(frame); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errors:
		t.Fatal(err)
	case value := <-received:
		if value["type"] != "model_request" || value["id"] != "model-1" {
			t.Fatalf("unexpected frame: %#v", value)
		}
	}
}

func TestWireRejectsBodyAuthorityDuplicateJSONAndAggregateOverflow(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxAggregateBytes = limits.MaxFrameBytes
	for _, encoded := range [][]byte{
		[]byte(`{"type":"model_request","id":"model-1","turn":1,"model":"evil"}`),
		[]byte(`{"type":"model_request","id":"model-1","id":"model-2","turn":1}`),
	} {
		host, child := net.Pipe()
		result := make(chan error, 1)
		go func() { _, err := newWire(host, limits).receive(); result <- err }()
		frame := make([]byte, len(encoded)+4)
		binary.BigEndian.PutUint32(frame, uint32(len(encoded)))
		copy(frame[4:], encoded)
		_, _ = child.Write(frame)
		if err := <-result; err == nil {
			t.Fatalf("invalid frame accepted: %s", encoded)
		}
		_ = host.Close()
		_ = child.Close()
	}

	host, child := net.Pipe()
	transport := newWire(host, limits)
	transport.aggregate = limits.MaxAggregateBytes - 4
	result := make(chan error, 1)
	go func() { _, err := transport.receive(); result <- err }()
	child.Write([]byte{0, 0, 0, 1})
	if err := <-result; err == nil {
		t.Fatal("aggregate overflow was accepted")
	}
	_ = host.Close()
	_ = child.Close()
}

func TestModelRequestWireBranchIsBodyless(t *testing.T) {
	base := map[string]any{"type": "model_request", "id": "model-1", "turn": 1}
	if err := validateWireMessage(base); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]any{
		"url": "https://evil.invalid", "method": "POST", "headers": map[string]any{"authorization": "secret"},
		"model": "evil", "messages": []any{}, "tools": []any{}, "body_base64": "e30=", "usage": map[string]any{"model_calls": 999},
	} {
		message := map[string]any{"type": "model_request", "id": "model-1", "turn": 1, name: value}
		if err := validateWireMessage(message); err == nil {
			t.Fatalf("model request authority %q was accepted", name)
		}
	}
}

func TestProviderWireTimeoutUsesSharedInclusiveRange(t *testing.T) {
	provider := map[string]any{
		"model_id": "model", "body_options": map[string]any{}, "reasoning_effort": "none",
		"context_window": 8_192, "max_output_tokens": 1_024,
		"timeout_ms": 1_000, "capsule_runtime_revision": RuntimeRevision,
	}
	for _, timeout := range []int64{1_000, 60_000} {
		provider["timeout_ms"] = timeout
		if !providerField()(provider) {
			t.Fatalf("boundary timeout %d rejected", timeout)
		}
	}
	for _, timeout := range []int64{999, 60_001} {
		provider["timeout_ms"] = timeout
		if providerField()(provider) {
			t.Fatalf("out-of-range timeout %d accepted", timeout)
		}
	}
}
