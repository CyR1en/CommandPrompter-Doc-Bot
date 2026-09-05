package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/cyr1en/ref0/internal/auth"
	commitlog "github.com/cyr1en/ref0/internal/events"
	"github.com/prometheus/client_golang/prometheus"
)

type fixedEventReader struct {
	events []commitlog.Event
	window commitlog.CursorWindow
}

type pruningEventReader struct{ reads, windows int }

func (reader *pruningEventReader) ReadAfter(context.Context, int64, int) ([]commitlog.Event, error) {
	reader.reads++
	if reader.reads == 1 {
		return nil, commitlog.ErrCursorPruned
	}
	return nil, nil
}

func (reader *pruningEventReader) Window(context.Context) (commitlog.CursorWindow, error) {
	reader.windows++
	if reader.windows == 1 {
		return commitlog.CursorWindow{Tail: 8, PrunedThrough: 6}, nil
	}
	return commitlog.CursorWindow{Tail: 9, PrunedThrough: 7}, nil
}

type revokingSessionService struct {
	*fakeSessionService
	authenticateCalls int
	revokeAfter       int
}

func (service *revokingSessionService) Authenticate(
	ctx context.Context,
	token auth.SessionToken,
) (auth.OperatorSession, error) {
	service.authenticateCalls++
	if service.authenticateCalls > service.revokeAfter {
		return auth.OperatorSession{}, auth.ErrAuthentication
	}
	return service.fakeSessionService.Authenticate(ctx, token)
}

func (reader fixedEventReader) ReadAfter(_ context.Context, cursor int64, _ int) ([]commitlog.Event, error) {
	result := make([]commitlog.Event, 0, len(reader.events))
	for _, event := range reader.events {
		if event.Sequence > cursor {
			result = append(result, event)
		}
	}
	return result, nil
}

func (reader fixedEventReader) Window(context.Context) (commitlog.CursorWindow, error) {
	window := reader.window
	for _, event := range reader.events {
		window.Tail = max(window.Tail, event.Sequence)
	}
	return window, nil
}

func TestEventStreamOrdersSnapshotsResumesAndAuthenticates(t *testing.T) {
	authenticated := fixedAuthenticatedSession(t)
	authenticated.Session.ExpiresAt = time.Now().Add(time.Hour)
	sessions := &fakeSessionService{session: authenticated.Session}
	reader := fixedEventReader{events: []commitlog.Event{
		{Sequence: 4, Type: "knowledge_base.updated", Snapshot: json.RawMessage(`{"number":1,"state":"active"}`)},
		{Sequence: 5, Type: "knowledge_base.updated", Snapshot: json.RawMessage(`{"number":2,"state":"active"}`)},
	}}
	config := Config{
		version:           "test",
		eventPollInterval: time.Millisecond,
		eventBeatInterval: time.Second,
		eventLimit:        2,
	}
	handler := eventTestHandler(t, config, sessions, reader)

	unauthorized := authRequest(t, handler, http.MethodGet, eventStreamPath, "", nil)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d body=%s", unauthorized.Code, unauthorized.Body.String())
	}
	response := authRequest(t, handler, http.MethodGet, eventStreamPath+"?after=3", "", map[string]string{
		"Cookie": sessionCookie(authenticated.Token.Reveal()),
	})
	want := "id: 4\nevent: knowledge_base.updated\ndata: {\"number\":1,\"state\":\"active\"}\n\n" +
		"id: 5\nevent: knowledge_base.updated\ndata: {\"number\":2,\"state\":\"active\"}\n\n"
	if response.Code != http.StatusOK || !response.Flushed || response.Body.String() != want {
		t.Fatalf("event stream=%d flushed=%v %q", response.Code, response.Flushed, response.Body.String())
	}
	if !strings.HasPrefix(response.Header().Get("Content-Type"), "text/event-stream") ||
		response.Header().Get("Cache-Control") != "no-store" ||
		response.Header().Get("X-Accel-Buffering") != "no" {
		t.Fatalf("event stream headers=%v", response.Header())
	}

	config.eventLimit = 1
	resumedHandler := eventTestHandler(t, config, sessions, reader)
	resumed := authRequest(t, resumedHandler, http.MethodGet, eventStreamPath+"?after=1", "", map[string]string{
		"Cookie":        sessionCookie(authenticated.Token.Reveal()),
		"Last-Event-ID": "4",
	})
	if !strings.HasPrefix(resumed.Body.String(), "id: 5\n") {
		t.Fatalf("resumed stream=%q", resumed.Body.String())
	}
}

func TestEventStreamFreshDashboardStartsAtTail(t *testing.T) {
	authenticated := fixedAuthenticatedSession(t)
	authenticated.Session.ExpiresAt = time.Now().Add(time.Hour)
	config := Config{
		version: "test", eventPollInterval: time.Millisecond,
		eventBeatInterval: time.Millisecond, eventBeatLimit: 1,
	}
	reader := fixedEventReader{events: []commitlog.Event{
		{Sequence: 4, Type: "knowledge_base.updated", Snapshot: json.RawMessage(`{"id":"old"}`)},
		{Sequence: 5, Type: "knowledge_base.updated", Snapshot: json.RawMessage(`{"id":"current"}`)},
	}}
	response := authRequest(t, eventTestHandler(t, config,
		&fakeSessionService{session: authenticated.Session}, reader,
	), http.MethodGet, eventStreamPath, "", map[string]string{
		"Cookie": sessionCookie(authenticated.Token.Reveal()),
	})
	if response.Code != http.StatusOK || response.Body.String() != ": heartbeat\n\n" {
		t.Fatalf("fresh stream=%d %q", response.Code, response.Body.String())
	}
}

func TestEventStreamCursorValidationAndHeartbeat(t *testing.T) {
	authenticated := fixedAuthenticatedSession(t)
	authenticated.Session.ExpiresAt = time.Now().Add(time.Hour)
	sessions := &fakeSessionService{session: authenticated.Session}
	config := Config{
		version:           "test",
		eventPollInterval: time.Millisecond,
		eventBeatInterval: time.Millisecond,
		eventBeatLimit:    1,
	}
	handler := eventTestHandler(t, config, sessions, fixedEventReader{})
	cookie := sessionCookie(authenticated.Token.Reveal())

	for _, path := range []string{
		eventStreamPath + "?after=wat",
		eventStreamPath + "?after=-1",
		eventStreamPath + "?after=9223372036854775808",
	} {
		response := authRequest(t, handler, http.MethodGet, path, "", map[string]string{"Cookie": cookie})
		if response.Code != http.StatusBadRequest || problemDetail(t, response) != "Event cursor is invalid." {
			t.Fatalf("cursor response for %s=%d %s", path, response.Code, response.Body.String())
		}
	}
	heartbeat := authRequest(t, handler, http.MethodGet, eventStreamPath+"?after=0", "", map[string]string{
		"Cookie": cookie, "Last-Event-ID": "0",
	})
	if heartbeat.Code != http.StatusOK || heartbeat.Body.String() != ": heartbeat\n\n" {
		t.Fatalf("heartbeat=%d %q", heartbeat.Code, heartbeat.Body.String())
	}
}

func TestEventStreamResetsPrunedAndAheadCursorsInBand(t *testing.T) {
	authenticated := fixedAuthenticatedSession(t)
	authenticated.Session.ExpiresAt = time.Now().Add(time.Hour)
	config := Config{
		version: "test", eventPollInterval: time.Millisecond,
		eventBeatInterval: time.Millisecond, eventBeatLimit: 1,
	}
	reader := fixedEventReader{window: commitlog.CursorWindow{Tail: 8, PrunedThrough: 4}}
	handler := eventTestHandler(t, config, &fakeSessionService{session: authenticated.Session}, reader)
	cookie := sessionCookie(authenticated.Token.Reveal())
	for _, test := range []struct {
		cursor string
		reason string
	}{
		{cursor: "3", reason: "cursor_pruned"},
		{cursor: "9", reason: "cursor_ahead"},
	} {
		response := authRequest(t, handler, http.MethodGet, eventStreamPath+"?after="+test.cursor, "", map[string]string{"Cookie": cookie})
		want := "id: 8\nevent: stream.reset\ndata: {\"id\":\"event_stream\",\"reason\":\"" + test.reason + "\"}\n\n"
		if response.Code != http.StatusOK || !strings.HasPrefix(response.Body.String(), want) {
			t.Fatalf("cursor %s reset=%d %q", test.cursor, response.Code, response.Body.String())
		}
	}
}

func TestEventStreamResetsWhenRetentionWinsTheReadRace(t *testing.T) {
	authenticated := fixedAuthenticatedSession(t)
	authenticated.Session.ExpiresAt = time.Now().Add(time.Hour)
	config := Config{
		version: "test", eventPollInterval: time.Millisecond,
		eventBeatInterval: time.Millisecond, eventBeatLimit: 1,
	}
	reader := &pruningEventReader{}
	response := authRequest(t, eventTestHandler(t, config,
		&fakeSessionService{session: authenticated.Session}, reader,
	), http.MethodGet, eventStreamPath+"?after=6", "", map[string]string{
		"Cookie": sessionCookie(authenticated.Token.Reveal()),
	})
	want := "id: 9\nevent: stream.reset\ndata: {\"id\":\"event_stream\",\"reason\":\"cursor_pruned\"}\n\n: heartbeat\n\n"
	if response.Code != http.StatusOK || response.Body.String() != want || reader.reads < 2 || reader.windows != 2 {
		t.Fatalf("raced stream=%d reads=%d windows=%d %q", response.Code, reader.reads, reader.windows, response.Body.String())
	}
}

func TestEventStreamStopsAtSessionExpiry(t *testing.T) {
	authenticated := fixedAuthenticatedSession(t)
	authenticated.Session.ExpiresAt = time.Now().Add(5 * time.Millisecond)
	config := Config{
		version: "test", eventPollInterval: time.Millisecond,
		eventBeatInterval: time.Second,
	}
	started := time.Now()
	response := authRequest(t, eventTestHandler(t, config,
		&fakeSessionService{session: authenticated.Session}, fixedEventReader{},
	), http.MethodGet, eventStreamPath, "", map[string]string{
		"Cookie": sessionCookie(authenticated.Token.Reveal()),
	})
	if response.Code != http.StatusOK || response.Body.Len() != 0 || time.Since(started) > time.Second {
		t.Fatalf("expired stream=%d body=%q elapsed=%s", response.Code, response.Body.String(), time.Since(started))
	}
}

func TestEventStreamStopsWhenSessionIsRevoked(t *testing.T) {
	authenticated := fixedAuthenticatedSession(t)
	authenticated.Session.ExpiresAt = time.Now().Add(time.Hour)
	sessions := &revokingSessionService{
		fakeSessionService: &fakeSessionService{session: authenticated.Session},
		revokeAfter:        1,
	}
	config := Config{
		version: "test", eventPollInterval: time.Millisecond,
		eventBeatInterval: time.Millisecond,
	}
	started := time.Now()
	response := authRequest(t, eventTestHandler(t, config, sessions, fixedEventReader{}),
		http.MethodGet, eventStreamPath, "", map[string]string{
			"Cookie": sessionCookie(authenticated.Token.Reveal()),
		})
	if response.Code != http.StatusOK || response.Body.Len() != 0 ||
		sessions.authenticateCalls != 2 || time.Since(started) > time.Second {
		t.Fatalf("revoked stream=%d body=%q authenticate_calls=%d elapsed=%s",
			response.Code, response.Body.String(), sessions.authenticateCalls, time.Since(started))
	}
}

func TestEventSettingsFailClosed(t *testing.T) {
	for _, config := range []Config{
		{eventPollInterval: time.Second + time.Nanosecond},
		{eventBeatInterval: 15*time.Second + time.Nanosecond},
		{eventLimit: -1},
		{eventBeatLimit: -1},
	} {
		if _, err := config.eventSettings(); err == nil {
			t.Fatalf("invalid event settings accepted: %+v", config)
		}
	}
}

func eventTestHandler(
	t *testing.T,
	config Config,
	sessions auth.SessionService,
	reader eventReader,
) http.Handler {
	t.Helper()
	registry := prometheus.NewRegistry()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler, err := newHandler(config, allReady(), sessions, reader, inertJobService{}, inertOperationsService{}, registry, registry, logger)
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

var _ eventReader = fixedEventReader{}
