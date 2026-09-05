package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"reflect"
	"time"

	"github.com/cyr1en/ref0/internal/auth"
	commitlog "github.com/cyr1en/ref0/internal/events"
	"github.com/danielgtaylor/huma/v2"
)

const eventStreamPath = "/api/v1/events"

type eventReader interface {
	ReadAfter(context.Context, int64, int) ([]commitlog.Event, error)
	Window(context.Context) (commitlog.CursorWindow, error)
}

type eventStreamSettings struct {
	pollInterval time.Duration
	beatInterval time.Duration
	eventLimit   int
	beatLimit    int
}

type eventStreamInput struct {
	SessionCookie string              `cookie:"ref0_session"`
	After         optionalStringParam `query:"after"`
	LastEventID   optionalStringParam `header:"Last-Event-ID"`
}

type optionalStringParam struct {
	Value string
	IsSet bool
}

func (parameter optionalStringParam) Schema(registry huma.Registry) *huma.Schema {
	return huma.SchemaFromType(registry, reflect.TypeFor[string]())
}

func (parameter *optionalStringParam) Receiver() reflect.Value {
	return reflect.ValueOf(parameter).Elem().FieldByName("Value")
}

func (parameter *optionalStringParam) OnParamSet(isSet bool, _ any) {
	parameter.IsSet = isSet
}

func (parameter optionalStringParam) Pointer() *string {
	if !parameter.IsSet {
		return nil
	}
	return &parameter.Value
}

type eventStreamOutput struct {
	CacheControl    string `header:"Cache-Control"`
	ContentType     string `header:"Content-Type"`
	XAccelBuffering string `header:"X-Accel-Buffering"`
	Body            func(huma.Context)
}

func registerEvents(
	api huma.API,
	sessions auth.SessionService,
	reader eventReader,
	settings eventStreamSettings,
	logger *slog.Logger,
) {
	huma.Register(api, huma.Operation{
		OperationID: "events_api_v1_events_get",
		Method:      http.MethodGet,
		Path:        eventStreamPath,
		Summary:     "Events",
		Tags:        []string{"events"},
		Errors:      []int{http.StatusBadRequest, http.StatusUnauthorized},
		Responses: map[string]*huma.Response{
			"200": {
				Description: "Server-sent resource events",
				Content: map[string]*huma.MediaType{
					"text/event-stream": {Schema: &huma.Schema{Type: huma.TypeString}},
				},
			},
		},
	}, func(ctx context.Context, input *eventStreamInput) (*eventStreamOutput, error) {
		token, session, err := AuthenticateSession(
			ctx,
			sessions,
			input.SessionCookie,
			eventStreamPath,
		)
		if err != nil {
			return nil, err
		}
		cursor, err := commitlog.ParseCursor(input.After.Pointer(), input.LastEventID.Pointer())
		if err != nil {
			return nil, eventCursorProblem(err)
		}
		window, err := reader.Window(ctx)
		if err != nil {
			return nil, err
		}
		resetReason := ""
		explicitCursor := input.After.IsSet || input.LastEventID.IsSet
		if !explicitCursor {
			cursor = window.Tail
		} else if cursor < window.PrunedThrough {
			cursor, resetReason = window.Tail, "cursor_pruned"
		} else if cursor > window.Tail {
			cursor, resetReason = window.Tail, "cursor_ahead"
		}
		return &eventStreamOutput{
			CacheControl:    "no-store",
			ContentType:     "text/event-stream; charset=utf-8",
			XAccelBuffering: "no",
			Body: func(streamContext huma.Context) {
				streamEvents(streamContext, sessions, token, session, reader, cursor, resetReason, settings, logger)
			},
		}, nil
	})
}

func eventCursorProblem(_ error) error {
	return &apiProblem{
		Type:     "about:blank",
		Title:    "Bad Request",
		Status:   http.StatusBadRequest,
		Detail:   "Event cursor is invalid.",
		Instance: eventStreamPath,
	}
}

func streamEvents(
	streamContext huma.Context,
	sessions auth.SessionService,
	token auth.SessionToken,
	session auth.OperatorSession,
	reader eventReader,
	cursor int64,
	resetReason string,
	settings eventStreamSettings,
	logger *slog.Logger,
) {
	ctx := streamContext.Context()
	writer := streamContext.BodyWriter()
	responseWriter, ok := writer.(http.ResponseWriter)
	if !ok {
		logger.ErrorContext(ctx, "event_stream_writer_unavailable")
		return
	}
	controller := http.NewResponseController(responseWriter)
	lastBeat, lastAuthentication := time.Now(), time.Now()
	eventsEmitted := 0
	beatsEmitted := 0
	if resetReason != "" {
		if err := writeStreamReset(writer, controller, cursor, resetReason); err != nil {
			logger.ErrorContext(ctx, "event_stream_reset_invalid")
			return
		}
	}

	for ctx.Err() == nil && time.Now().Before(session.ExpiresAt) {
		if time.Since(lastAuthentication) >= settings.beatInterval {
			refreshed, err := sessions.Authenticate(ctx, token)
			if err != nil || refreshed.ID != session.ID {
				return
			}
			session = refreshed
			lastAuthentication = time.Now()
		}
		committed, err := reader.ReadAfter(ctx, cursor, 100)
		if errors.Is(err, commitlog.ErrCursorPruned) {
			window, windowErr := reader.Window(ctx)
			if windowErr != nil || writeStreamReset(writer, controller, window.Tail, "cursor_pruned") != nil {
				logger.ErrorContext(ctx, "event_stream_reset_failed")
				return
			}
			cursor = window.Tail
			continue
		}
		if err != nil {
			logger.ErrorContext(ctx, "event_stream_read_failed")
			return
		}
		if len(committed) > 0 {
			for _, event := range committed {
				message, messageErr := commitlog.Message(event)
				if messageErr != nil {
					logger.ErrorContext(ctx, "event_stream_record_invalid", "sequence", event.Sequence)
					return
				}
				if _, err = writer.Write(message); err != nil {
					return
				}
				if err = controller.Flush(); err != nil {
					return
				}
				cursor = event.Sequence
				eventsEmitted++
				if settings.eventLimit > 0 && eventsEmitted >= settings.eventLimit {
					return
				}
			}
			continue
		}
		if time.Since(lastBeat) >= settings.beatInterval {
			if _, err = writer.Write([]byte(": heartbeat\n\n")); err != nil {
				return
			}
			if err = controller.Flush(); err != nil {
				return
			}
			lastBeat = time.Now()
			beatsEmitted++
			if settings.beatLimit > 0 && beatsEmitted >= settings.beatLimit {
				return
			}
		}
		wait := min(settings.pollInterval, settings.beatInterval, time.Until(session.ExpiresAt))
		if wait <= 0 {
			return
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-timer.C:
		}
	}
}

func writeStreamReset(writer interface{ Write([]byte) (int, error) }, controller *http.ResponseController, cursor int64, reason string) error {
	message, err := commitlog.Message(commitlog.Event{
		Sequence: cursor,
		Type:     "stream.reset",
		Snapshot: []byte(`{"id":"event_stream","reason":"` + reason + `"}`),
	})
	if err != nil {
		return err
	}
	if _, err = writer.Write(message); err != nil {
		return err
	}
	return controller.Flush()
}
