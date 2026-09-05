package events

import (
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
)

func TestParseCursor(t *testing.T) {
	maximum := "9223372036854775807"
	for _, test := range []struct {
		name        string
		after       *string
		lastEventID *string
		want        int64
		wantError   error
	}{
		{name: "absent"},
		{name: "zero", after: pointer("0")},
		{name: "maximum", lastEventID: &maximum, want: math.MaxInt64},
		{name: "matching", after: pointer("12"), lastEventID: pointer("12"), want: 12},
		{name: "reconnect header wins", after: pointer("1"), lastEventID: pointer("2"), want: 2},
		{name: "reconnect ignores stale invalid query", after: pointer("invalid"), lastEventID: pointer("2"), want: 2},
		{name: "empty", after: pointer(""), wantError: ErrInvalidCursor},
		{name: "negative", after: pointer("-1"), wantError: ErrInvalidCursor},
		{name: "positive sign", after: pointer("+1"), wantError: ErrInvalidCursor},
		{name: "leading zero", after: pointer("01"), wantError: ErrInvalidCursor},
		{name: "overflow", after: pointer("9223372036854775808"), wantError: ErrInvalidCursor},
		{name: "whitespace", after: pointer(" 1"), wantError: ErrInvalidCursor},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := ParseCursor(test.after, test.lastEventID)
			if got != test.want || !errors.Is(err, test.wantError) {
				t.Fatalf("ParseCursor()=(%d,%v), want (%d,%v)", got, err, test.want, test.wantError)
			}
		})
	}
}

func TestMessageIsDeterministicAndRejectsInjection(t *testing.T) {
	event := Event{
		Sequence: 9,
		Type:     "knowledge_base.updated",
		Snapshot: json.RawMessage(`{"z":"café 🤔 <>& \\u003c","a":{"two":2,"one":1}}`),
	}
	message, err := Message(event)
	if err != nil {
		t.Fatal(err)
	}
	want := "id: 9\nevent: knowledge_base.updated\ndata: " +
		`{"a":{"one":1,"two":2},"z":"caf\u00e9 \ud83e\udd14 <>& \\u003c"}` + "\n\n"
	if string(message) != want {
		t.Fatalf("message=%q\nwant=%q", message, want)
	}

	for _, invalid := range []Event{
		{Sequence: -1, Type: "valid", Snapshot: json.RawMessage(`{}`)},
		{Sequence: 1, Type: "bad\nevent: injected", Snapshot: json.RawMessage(`{}`)},
		{Sequence: 1, Type: "valid", Snapshot: json.RawMessage(`[]`)},
		{Sequence: 1, Type: "valid", Snapshot: json.RawMessage(`{} {}`)},
		{Sequence: 1, Type: "valid", Snapshot: json.RawMessage(`{} trailing`)},
	} {
		if _, err = Message(invalid); !errors.Is(err, ErrInvalidEvent) {
			t.Fatalf("invalid event accepted: %+v, err=%v", invalid, err)
		}
	}
}

func TestReadBounds(t *testing.T) {
	reader := &Reader{}
	for _, input := range [][2]int64{{-1, 1}, {0, 0}, {0, MaxReadLimit + 1}} {
		if _, err := reader.ReadAfter(t.Context(), input[0], int(input[1])); !errors.Is(err, ErrInvalidRead) {
			t.Fatalf("ReadAfter(%d,%d) error=%v", input[0], input[1], err)
		}
	}
}

func pointer(value string) *string {
	return &value
}

func TestASCIIEncoderDoesNotMutateInput(t *testing.T) {
	input := []byte(`{"value":"<>&é"}`)
	copyOfInput := append([]byte(nil), input...)
	_ = escapeNonASCIIJSON(input)
	if !reflect.DeepEqual(input, copyOfInput) {
		t.Fatal("encoder mutated its input")
	}
}

func TestResourceEventValidation(t *testing.T) {
	for _, event := range []ResourceEvent{
		{},
		{Type: "bad\nevent", ResourceType: "test", Snapshot: map[string]any{}},
		{Type: "valid", ResourceType: strings.Repeat("x", 65), Snapshot: map[string]any{}},
		{Type: "valid", ResourceType: "test", Snapshot: map[string]any{"number": math.Inf(1)}},
	} {
		if err := Append(t.Context(), nil, event); !errors.Is(err, ErrInvalidEvent) {
			t.Fatalf("invalid resource event error=%v", err)
		}
	}
}
