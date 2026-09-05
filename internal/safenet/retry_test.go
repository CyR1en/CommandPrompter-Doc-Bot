package safenet

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRetryHeadersSurviveJSONFailuresAndRespectBounds(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After-Ms", "125")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()
	client, err := NewClient(server.URL, privateHTTPPolicy(), ClientOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	_, _, err = client.PostJSON(context.Background(), "chat/completions", map[string]any{})
	var failure *RequestError
	if !errors.As(err, &failure) || !failure.Retryable {
		t.Fatalf("failure=%v", err)
	}
	delay, err := RetryDelay(failure.RetryHeaders, 0, time.Now())
	if err != nil || delay != 125*time.Millisecond {
		t.Fatalf("server delay=%s %v", delay, err)
	}
	now := time.Now().Truncate(time.Second)
	for _, test := range []struct {
		headers map[string]string
		attempt int
		want    time.Duration
	}{
		{nil, 0, 500 * time.Millisecond}, {nil, 8, 8 * time.Second},
		{map[string]string{"retry-after": "2"}, 0, 2 * time.Second},
		{map[string]string{"retry-after": now.Add(3 * time.Second).UTC().Format(http.TimeFormat)}, 0, 3 * time.Second},
	} {
		delay, err := RetryDelay(test.headers, test.attempt, now)
		if err != nil || delay != test.want {
			t.Fatalf("retry delay=%s %v", delay, err)
		}
	}
	for _, value := range []string{"61", "NaN", "Infinity"} {
		if _, err := RetryDelay(map[string]string{"retry-after": value}, 0, now); err == nil {
			t.Fatalf("unsafe delay accepted: %s", value)
		}
	}
}
