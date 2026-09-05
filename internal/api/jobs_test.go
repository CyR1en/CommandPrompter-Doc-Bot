package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/cyr1en/ref0/internal/auth"
	"github.com/cyr1en/ref0/internal/idempotency"
	"github.com/cyr1en/ref0/internal/jobs"
	"github.com/prometheus/client_golang/prometheus"
)

type fakeJobService struct {
	values      []jobs.Snapshot
	listOptions []jobs.ListOptions
	getError    error
	cancelError error
	cancelActor jobs.ActorID
	cancelKey   string
	cancelID    jobs.JobID
}

func (service *fakeJobService) List(_ context.Context, options jobs.ListOptions) ([]jobs.Snapshot, error) {
	service.listOptions = append(service.listOptions, options)
	return service.values, nil
}

func (service *fakeJobService) Get(context.Context, jobs.JobID) (jobs.Snapshot, error) {
	if service.getError != nil {
		return jobs.Snapshot{}, service.getError
	}
	return service.values[0], nil
}

func (service *fakeJobService) Cancel(_ context.Context, id jobs.JobID, actor jobs.ActorID, key string) (jobs.Snapshot, error) {
	service.cancelID = id
	service.cancelActor = actor
	service.cancelKey = key
	if service.cancelError != nil {
		return jobs.Snapshot{}, service.cancelError
	}
	return service.values[0], nil
}

func TestJobRoutesAreAuthenticatedBoundedAndSanitized(t *testing.T) {
	authenticated := fixedAuthenticatedSession(t)
	sessions := &fakeSessionService{session: authenticated.Session}
	value := testJobSnapshot()
	service := &fakeJobService{values: []jobs.Snapshot{value}}
	handler := jobTestHandler(t, sessions, service)
	cookie := sessionCookie(authenticated.Token.Reveal())

	unauthorized := authRequest(t, handler, http.MethodGet, jobsPath, "", nil)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized=%d %s", unauthorized.Code, unauthorized.Body.String())
	}
	listed := authRequest(t, handler, http.MethodGet, jobsPath+"?status=pending&job_type=sync_source", "", map[string]string{
		"Cookie": cookie,
	})
	if listed.Code != http.StatusOK || strings.Contains(listed.Body.String(), "private-input") ||
		strings.Contains(listed.Body.String(), "operation_key") || strings.Contains(listed.Body.String(), "payload") {
		t.Fatalf("listed=%d %s", listed.Code, listed.Body.String())
	}
	if len(service.listOptions) != 1 || service.listOptions[0].Limit != 50 || service.listOptions[0].Offset != 0 ||
		service.listOptions[0].Status == nil || *service.listOptions[0].Status != jobs.Pending ||
		service.listOptions[0].Type == nil || *service.listOptions[0].Type != jobs.SyncSource {
		t.Fatalf("list options=%+v", service.listOptions)
	}
	for _, field := range []string{
		`"id"`, `"job_type":"sync_source"`, `"target_type":"source"`,
		`"status":"pending"`, `"lease_owner":null`, `"result":null`,
	} {
		if !strings.Contains(listed.Body.String(), field) {
			t.Fatalf("job response lacks %s: %s", field, listed.Body.String())
		}
	}

	fetched := authRequest(t, handler, http.MethodGet, jobsPath+"/"+value.ID.String(), "", map[string]string{"Cookie": cookie})
	if fetched.Code != http.StatusOK || !strings.Contains(fetched.Body.String(), value.ID.String()) {
		t.Fatalf("fetched=%d %s", fetched.Code, fetched.Body.String())
	}
	invalid := authRequest(t, handler, http.MethodGet, jobsPath+"?limit=101", "", map[string]string{"Cookie": cookie})
	invalidType := authRequest(t, handler, http.MethodGet, jobsPath+"?job_type=SYNC_SOURCE", "", map[string]string{"Cookie": cookie})
	if invalid.Code != http.StatusUnprocessableEntity || invalidType.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid list=%d/%d", invalid.Code, invalidType.Code)
	}

	service.getError = jobs.ErrJobNotFound
	missing := authRequest(t, handler, http.MethodGet, jobsPath+"/"+value.ID.String(), "", map[string]string{"Cookie": cookie})
	if missing.Code != http.StatusNotFound || problemDetail(t, missing) != "Job not found." {
		t.Fatalf("missing=%d %s", missing.Code, missing.Body.String())
	}
}

func TestCancelJobRequiresCSRFAndIdempotencyAndMapsConflicts(t *testing.T) {
	authenticated := fixedAuthenticatedSession(t)
	sessions := &fakeSessionService{session: authenticated.Session}
	value := testJobSnapshot()
	service := &fakeJobService{values: []jobs.Snapshot{value}}
	handler := jobTestHandler(t, sessions, service)
	path := jobsPath + "/" + value.ID.String() + "/cancel"
	cookie := sessionCookie(authenticated.Token.Reveal())
	csrf := auth.CSRFTokenFor(authenticated.Token, authenticated.Session.ID)

	missingCSRF := authRequest(t, handler, http.MethodPost, path, "", map[string]string{
		"Cookie": cookie, "Idempotency-Key": "cancel-one",
	})
	if missingCSRF.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF=%d %s", missingCSRF.Code, missingCSRF.Body.String())
	}
	missingKey := authRequest(t, handler, http.MethodPost, path, "", map[string]string{
		"Cookie": cookie, csrfHeaderName: csrf,
	})
	if missingKey.Code != http.StatusUnprocessableEntity {
		t.Fatalf("missing key=%d %s", missingKey.Code, missingKey.Body.String())
	}
	blankKey := authRequest(t, handler, http.MethodPost, path, "", map[string]string{
		"Cookie": cookie, csrfHeaderName: csrf, "Idempotency-Key": " \t ",
	})
	if blankKey.Code != http.StatusUnprocessableEntity || problemDetail(t, blankKey) != "Idempotency-Key is required." {
		t.Fatalf("blank key=%d %s", blankKey.Code, blankKey.Body.String())
	}

	cancelled := authRequest(t, handler, http.MethodPost, path, "", map[string]string{
		"Cookie": cookie, csrfHeaderName: csrf, "Idempotency-Key": "  cancel-one  ",
	})
	if cancelled.Code != http.StatusOK || service.cancelKey != "cancel-one" ||
		service.cancelActor != jobs.ActorID(authenticated.Session.Operator.ID) || service.cancelID != value.ID {
		t.Fatalf("cancelled=%d %s key=%q actor=%s", cancelled.Code, cancelled.Body.String(), service.cancelKey, jobs.UUID(service.cancelActor).String())
	}

	service.cancelError = idempotency.ErrConflict
	conflict := authRequest(t, handler, http.MethodPost, path, "", map[string]string{
		"Cookie": cookie, csrfHeaderName: csrf, "Idempotency-Key": "cancel-two",
	})
	if conflict.Code != http.StatusConflict || problemDetail(t, conflict) != "Idempotency key conflicts with a different request." {
		t.Fatalf("idempotency conflict=%d %s", conflict.Code, conflict.Body.String())
	}
	service.cancelError = jobs.ErrJobConflict
	stateConflict := authRequest(t, handler, http.MethodPost, path, "", map[string]string{
		"Cookie": cookie, csrfHeaderName: csrf, "Idempotency-Key": "cancel-three",
	})
	if stateConflict.Code != http.StatusConflict || problemDetail(t, stateConflict) != "Job state conflicts with the request." {
		t.Fatalf("state conflict=%d %s", stateConflict.Code, stateConflict.Body.String())
	}
}

func jobTestHandler(t *testing.T, sessions auth.SessionService, service jobService) http.Handler {
	t.Helper()
	registry := prometheus.NewRegistry()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler, err := newHandler(
		Config{version: "test"}, allReady(), sessions, inertEventReader{}, service,
		inertOperationsService{}, registry, registry, logger,
	)
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func testJobSnapshot() jobs.Snapshot {
	created := time.Date(2026, 8, 30, 1, 2, 3, 0, time.UTC)
	return jobs.Snapshot{
		ID: jobs.JobID{1}, Type: jobs.SyncSource, TargetType: "source",
		TargetID: jobs.UUID{2}, Status: jobs.Pending, MaxAttempts: 3,
		CreatedAt: created, UpdatedAt: created,
	}
}

var _ jobService = (*fakeJobService)(nil)
