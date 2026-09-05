package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/cyr1en/ref0/internal/auth"
	"github.com/cyr1en/ref0/internal/idempotency"
	"github.com/cyr1en/ref0/internal/jobs"
	"github.com/danielgtaylor/huma/v2"
)

const jobsPath = "/api/v1/jobs"

type jobService interface {
	List(context.Context, jobs.ListOptions) ([]jobs.Snapshot, error)
	Get(context.Context, jobs.JobID) (jobs.Snapshot, error)
	Cancel(context.Context, jobs.JobID, jobs.ActorID, string) (jobs.Snapshot, error)
}

type listJobsInput struct {
	SessionCookie string              `cookie:"ref0_session"`
	Limit         int32               `query:"limit" default:"50" minimum:"1" maximum:"100"`
	Offset        int32               `query:"offset" default:"0" minimum:"0" maximum:"10000"`
	Status        optionalStringParam `query:"status" enum:"pending,leased,succeeded,retry_wait,failed,cancel_requested,cancelled"`
	JobType       optionalStringParam `query:"job_type" enum:"validate_source,sync_source,prepare_run,plan_run,generate_page,finalize_run,discover_endpoint,probe_model,refresh_discord,purge_knowledge_base,apply_retention"`
}

type getJobInput struct {
	SessionCookie string `cookie:"ref0_session"`
	JobID         string `path:"job_id" format:"uuid"`
}

type cancelJobInput struct {
	SessionCookie  string              `cookie:"ref0_session"`
	CSRFToken      string              `header:"X-CSRF-Token"`
	IdempotencyKey optionalStringParam `header:"Idempotency-Key" required:"true" minLength:"1" maxLength:"255"`
	JobID          string              `path:"job_id" format:"uuid"`
}

type jobResponse struct {
	ID              string         `json:"id" format:"uuid"`
	JobType         string         `json:"job_type" enum:"validate_source,sync_source,prepare_run,plan_run,generate_page,finalize_run,discover_endpoint,probe_model,refresh_discord,purge_knowledge_base,apply_retention"`
	TargetType      string         `json:"target_type"`
	TargetID        string         `json:"target_id" format:"uuid"`
	Status          string         `json:"status" enum:"pending,leased,succeeded,retry_wait,failed,cancel_requested,cancelled"`
	AttemptCount    int32          `json:"attempt_count"`
	MaxAttempts     int32          `json:"max_attempts"`
	Progress        int32          `json:"progress"`
	LeaseOwner      *string        `json:"lease_owner"`
	LeaseExpiresAt  *time.Time     `json:"lease_expires_at"`
	LeaseGeneration int64          `json:"lease_generation"`
	NotBefore       *time.Time     `json:"not_before"`
	Result          map[string]any `json:"result" nullable:"true"`
	SanitizedError  *string        `json:"sanitized_error"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	StartedAt       *time.Time     `json:"started_at"`
	FinishedAt      *time.Time     `json:"finished_at"`
}

type jobsOutput struct {
	Body []jobResponse `nullable:"false"`
}

type jobOutput struct {
	Body jobResponse
}

func registerJobs(api huma.API, sessions auth.SessionService, service jobService) {
	registerJobList(api, sessions, service)
	registerJobGet(api, sessions, service)
	registerJobCancel(api, sessions, service)
}

func registerJobList(api huma.API, sessions auth.SessionService, service jobService) {
	huma.Register(api, huma.Operation{
		OperationID: "list_jobs_api_v1_jobs_get",
		Method:      http.MethodGet,
		Path:        jobsPath,
		Summary:     "List Jobs",
		Tags:        []string{"jobs"},
		Errors:      []int{http.StatusUnauthorized, http.StatusUnprocessableEntity},
	}, func(ctx context.Context, input *listJobsInput) (*jobsOutput, error) {
		if _, _, err := AuthenticateSession(ctx, sessions, input.SessionCookie, jobsPath); err != nil {
			return nil, err
		}
		options, err := jobListOptions(input)
		if err != nil {
			return nil, parameterValidationProblem(jobsPath, "query")
		}
		values, err := service.List(ctx, options)
		if err != nil {
			return nil, jobProblem(jobsPath, err)
		}
		output := &jobsOutput{Body: make([]jobResponse, len(values))}
		for index, value := range values {
			output.Body[index] = newJobResponse(value)
		}
		return output, nil
	})
}

func registerJobGet(api huma.API, sessions auth.SessionService, service jobService) {
	const path = jobsPath + "/{job_id}"
	huma.Register(api, huma.Operation{
		OperationID: "get_job_api_v1_jobs__job_id__get",
		Method:      http.MethodGet,
		Path:        path,
		Summary:     "Get Job",
		Tags:        []string{"jobs"},
		Errors:      []int{http.StatusUnauthorized, http.StatusNotFound, http.StatusUnprocessableEntity},
	}, func(ctx context.Context, input *getJobInput) (*jobOutput, error) {
		instance := requestInstance(path, input.JobID)
		if _, _, err := AuthenticateSession(ctx, sessions, input.SessionCookie, instance); err != nil {
			return nil, err
		}
		id, err := jobs.ParseUUID(input.JobID)
		if err != nil {
			return nil, parameterValidationProblem(instance, "path")
		}
		value, err := service.Get(ctx, jobs.JobID(id))
		if err != nil {
			return nil, jobProblem(instance, err)
		}
		return &jobOutput{Body: newJobResponse(value)}, nil
	})
}

func registerJobCancel(api huma.API, sessions auth.SessionService, service jobService) {
	const path = jobsPath + "/{job_id}/cancel"
	huma.Register(api, huma.Operation{
		OperationID: "cancel_job_api_v1_jobs__job_id__cancel_post",
		Method:      http.MethodPost,
		Path:        path,
		Summary:     "Cancel Job",
		Tags:        []string{"jobs"},
		Errors: []int{
			http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound,
			http.StatusConflict, http.StatusUnprocessableEntity,
		},
	}, func(ctx context.Context, input *cancelJobInput) (*jobOutput, error) {
		instance := requestInstance(path, input.JobID)
		_, session, err := RequireAuthenticatedWrite(
			ctx,
			sessions,
			input.SessionCookie,
			input.CSRFToken,
			instance,
		)
		if err != nil {
			return nil, err
		}
		id, err := jobs.ParseUUID(input.JobID)
		if err != nil {
			return nil, parameterValidationProblem(instance, "path")
		}
		requestKey, err := normalizedIdempotencyKey(input.IdempotencyKey, instance)
		if err != nil {
			return nil, err
		}
		value, err := service.Cancel(
			ctx,
			jobs.JobID(id),
			jobs.ActorID(session.Operator.ID),
			requestKey,
		)
		if err != nil {
			return nil, jobProblem(instance, err)
		}
		return &jobOutput{Body: newJobResponse(value)}, nil
	})
}

func jobListOptions(input *listJobsInput) (jobs.ListOptions, error) {
	options := jobs.ListOptions{Limit: input.Limit, Offset: input.Offset}
	if input.Status.IsSet {
		status := jobs.Status(strings.ToUpper(input.Status.Value))
		if input.Status.Value != strings.ToLower(input.Status.Value) || !jobs.ValidStatus(status) {
			return jobs.ListOptions{}, errors.New("job status is invalid")
		}
		options.Status = &status
	}
	if input.JobType.IsSet {
		jobType := jobs.Type(strings.ToUpper(input.JobType.Value))
		if input.JobType.Value != strings.ToLower(input.JobType.Value) || !jobs.ValidType(jobType) {
			return jobs.ListOptions{}, errors.New("job type is invalid")
		}
		options.Type = &jobType
	}
	return options, nil
}

func normalizedIdempotencyKey(parameter optionalStringParam, instance string) (string, error) {
	if !parameter.IsSet || !utf8.ValidString(parameter.Value) ||
		utf8.RuneCountInString(parameter.Value) < 1 || utf8.RuneCountInString(parameter.Value) > 255 {
		return "", parameterValidationProblem(instance, "header")
	}
	value := strings.TrimFunc(parameter.Value, func(character rune) bool {
		return unicode.IsSpace(character) || character >= '\x1c' && character <= '\x1f'
	})
	if value == "" {
		return "", &apiProblem{
			Type:     "about:blank",
			Title:    "Unprocessable Content",
			Status:   http.StatusUnprocessableEntity,
			Detail:   "Idempotency-Key is required.",
			Instance: instance,
		}
	}
	return value, nil
}

func newJobResponse(value jobs.Snapshot) jobResponse {
	return jobResponse{
		ID: value.ID.String(), JobType: strings.ToLower(string(value.Type)),
		TargetType: value.TargetType, TargetID: value.TargetID.String(),
		Status: strings.ToLower(string(value.Status)), AttemptCount: value.AttemptCount,
		MaxAttempts: value.MaxAttempts, Progress: value.Progress, LeaseOwner: value.LeaseOwner,
		LeaseExpiresAt: value.LeaseExpiresAt, LeaseGeneration: value.LeaseGeneration,
		NotBefore: value.NotBefore, Result: value.Result, SanitizedError: value.SanitizedError,
		CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt,
		StartedAt: value.StartedAt, FinishedAt: value.FinishedAt,
	}
}

func parameterValidationProblem(instance, location string) error {
	return &apiProblem{
		Type:     "about:blank",
		Title:    "Unprocessable Content",
		Status:   http.StatusUnprocessableEntity,
		Detail:   "Request validation failed.",
		Instance: instance,
		InvalidParams: []invalidParameter{{
			Location: []string{location}, Message: "Invalid value.", Type: "value_error",
		}},
	}
}

func jobProblem(instance string, err error) error {
	problem := &apiProblem{Type: "about:blank", Instance: instance}
	switch {
	case errors.Is(err, jobs.ErrJobNotFound):
		problem.Title = "Not Found"
		problem.Status = http.StatusNotFound
		problem.Detail = "Job not found."
	case errors.Is(err, idempotency.ErrConflict):
		problem.Title = "Conflict"
		problem.Status = http.StatusConflict
		problem.Detail = "Idempotency key conflicts with a different request."
	case errors.Is(err, jobs.ErrJobConflict):
		problem.Title = "Conflict"
		problem.Status = http.StatusConflict
		problem.Detail = "Job state conflicts with the request."
	default:
		problem.Title = "Internal Server Error"
		problem.Status = http.StatusInternalServerError
		problem.Detail = "The request could not be completed."
	}
	return problem
}

func requestInstance(pattern, id string) string {
	return strings.Replace(pattern, "{job_id}", id, 1)
}
