package sources

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/cyr1en/ref0/internal/credentials"
	"github.com/cyr1en/ref0/internal/security"
	"github.com/cyr1en/ref0/internal/sourcefiles"
	"github.com/cyr1en/ref0/internal/sourcegit"
)

var ErrCredentialUnavailable = errors.New("source credential is unavailable")

type SecretReader interface {
	Read(context.Context, credentials.ID, credentials.Kind, int32) (*security.SecretValue, error)
}

type WebsiteCredential struct {
	Header string
	Value  *security.SecretValue
}

type WebsiteRequest struct {
	SourceID           ID
	RevisionID         *ID
	RemoteURL          string
	Credential         *WebsiteCredential
	TinyFishCredential *security.SecretValue
	Limits             CrawlLimits
	Mode               AcquisitionMode
	PreviousRevisionID *ID
}

type WebsiteSnapshot struct {
	NativeVersion string
	ArtifactKey   string
	Fingerprint   [32]byte
	FileCount     int
	ByteCount     int64
	Pages         []PageCapture
}

type WebsiteAdapter interface {
	Validate(context.Context, WebsiteRequest) (string, error)
	Materialize(context.Context, WebsiteRequest) (WebsiteSnapshot, error)
}

type WebsiteFailure struct {
	Code      string
	Retryable bool
}

func (failure *WebsiteFailure) Error() string { return "website acquisition failed" }

type Execution struct {
	secrets    SecretReader
	validator  *sourcegit.Validator
	repository *sourcegit.Acquirer
	artifacts  *sourcefiles.Store
	website    WebsiteAdapter
}

func NewExecution(secrets SecretReader, validator *sourcegit.Validator, repository *sourcegit.Acquirer, artifacts *sourcefiles.Store, website WebsiteAdapter) (*Execution, error) {
	if secrets == nil || validator == nil || repository == nil || artifacts == nil {
		return nil, errors.New("source execution dependencies are incomplete")
	}
	return &Execution{secrets: secrets, validator: validator, repository: repository, artifacts: artifacts, website: website}, nil
}

func (execution *Execution) Validate(ctx context.Context, run Sync) ValidationCompletion {
	completion := ValidationCompletion{SyncID: run.ID}
	if run.Kind != Validation {
		completion.SanitizedError = stringValue("source_validation:invalid_capture")
		return completion
	}
	if run.Repository != nil {
		credential, err := execution.repositoryCredential(ctx, *run.Repository)
		if err != nil {
			return validationFailure(run.ID, "source_validation:credential_unavailable", false)
		}
		var evidence sourcegit.ValidationEvidence
		if run.Repository.Reference.Kind == Branch {
			evidence, err = execution.validator.ValidateBranch(ctx, run.Repository.Remote.URL, run.Repository.Reference.Value, credential)
		} else {
			evidence, err = execution.validator.ValidateCommit(ctx, run.Repository.Remote.URL, run.Repository.Reference.Value, credential)
		}
		if err != nil {
			var remote *sourcegit.RemoteError
			if errors.As(err, &remote) {
				return validationFailure(run.ID, "source_validation:"+string(remote.Code), remote.Retryable)
			}
			return validationFailure(run.ID, "source_validation:invalid_configuration", false)
		}
		completion.ResolvedNativeVersion = stringValue(evidence.Commit)
		return completion
	}
	if run.Website == nil || execution.website == nil {
		return validationFailure(run.ID, "source_validation:invalid_configuration", false)
	}
	request, err := execution.websiteRequest(ctx, run, nil)
	if err != nil {
		return validationFailure(run.ID, "source_validation:credential_unavailable", false)
	}
	version, err := execution.website.Validate(ctx, request)
	if err != nil {
		var failure *WebsiteFailure
		if errors.As(err, &failure) {
			return validationFailure(run.ID, websiteError("source_validation", run.Website.AcquisitionMode, failure.Code), failure.Retryable)
		}
		return validationFailure(run.ID, "source_validation:invalid_configuration", false)
	}
	completion.ResolvedNativeVersion = stringValue(version)
	return completion
}

func (execution *Execution) Sync(ctx context.Context, run Sync) SyncCompletion {
	completion := SyncCompletion{SyncID: run.ID}
	if run.Kind != Synchronization || run.CandidateRevisionID == nil {
		completion.SanitizedError = stringValue("source_sync:invalid_capture")
		return completion
	}
	if run.Repository != nil {
		credential, err := execution.repositoryCredential(ctx, *run.Repository)
		if err != nil {
			return syncFailure(run.ID, "source_sync:credential_unavailable", false)
		}
		reference, err := run.Repository.Reference.gitReference()
		if err != nil {
			return syncFailure(run.ID, "source_sync:invalid_configuration", false)
		}
		snapshot, err := execution.repository.Materialize(ctx, sourcegit.MaterializeRequest{
			SourceID: sourcefiles.ID(run.SourceID), RevisionID: sourcefiles.ID(*run.CandidateRevisionID),
			RemoteURL: run.Repository.Remote.URL, SelectedRef: reference, Credential: credential,
			IncludePatterns: run.Repository.IncludePatterns, ExcludePatterns: run.Repository.ExcludePatterns,
		})
		if err != nil {
			var remote *sourcegit.RemoteError
			if errors.As(err, &remote) {
				return syncFailure(run.ID, "source_sync:git_"+string(remote.Code), remote.Retryable)
			}
			var repository *sourcegit.RepositoryError
			if errors.As(err, &repository) {
				retryable := repository.Code == sourcegit.RepositoryGit || repository.Code == sourcegit.RepositoryTimeout
				return syncFailure(run.ID, "source_sync:"+string(repository.Code), retryable)
			}
			if errors.Is(err, sourcefiles.ErrSourceStorage) {
				return syncFailure(run.ID, "source_sync:storage", false)
			}
			return syncFailure(run.ID, "source_sync:invalid_configuration", false)
		}
		if snapshot.ArtifactKey != ArtifactKey(run.SourceID, *run.CandidateRevisionID) {
			return syncFailure(run.ID, "source_sync:invalid_configuration", false)
		}
		completion.Revision = &RevisionCandidate{
			NativeVersion: snapshot.Commit, Fingerprint: snapshot.Fingerprint.Digest,
			ArtifactKey: snapshot.ArtifactKey, FileCount: snapshot.FileCount(), ByteCount: snapshot.ByteCount(),
			IgnoredPaths: snapshot.IgnoredPaths,
		}
		return completion
	}
	if run.Website == nil || execution.website == nil {
		return syncFailure(run.ID, "source_sync:invalid_configuration", false)
	}
	request, err := execution.websiteRequest(ctx, run, run.CandidateRevisionID)
	if err != nil {
		return syncFailure(run.ID, "source_sync:credential_unavailable", false)
	}
	snapshot, err := execution.website.Materialize(ctx, request)
	if err != nil {
		var failure *WebsiteFailure
		if errors.As(err, &failure) {
			return syncFailure(run.ID, websiteError("source_sync", run.Website.AcquisitionMode, failure.Code), failure.Retryable)
		}
		if errors.Is(err, sourcefiles.ErrSourceStorage) {
			return syncFailure(run.ID, "source_sync:storage", false)
		}
		return syncFailure(run.ID, "source_sync:invalid_configuration", false)
	}
	if snapshot.ArtifactKey != ArtifactKey(run.SourceID, *run.CandidateRevisionID) {
		return syncFailure(run.ID, "source_sync:invalid_configuration", false)
	}
	completion.Revision = &RevisionCandidate{
		NativeVersion: snapshot.NativeVersion, Fingerprint: snapshot.Fingerprint,
		ArtifactKey: snapshot.ArtifactKey, FileCount: snapshot.FileCount,
		ByteCount: snapshot.ByteCount, WebsitePages: snapshot.Pages,
	}
	return completion
}

func (execution *Execution) DiscardReusedCandidate(run Sync) error {
	if run.Kind == Synchronization && run.Status == SyncSucceeded && run.CandidateRevisionID != nil && run.ResultRevisionID != nil && *run.CandidateRevisionID != *run.ResultRevisionID {
		return execution.artifacts.DiscardSnapshot(sourcefiles.ID(run.SourceID), sourcefiles.ID(*run.CandidateRevisionID))
	}
	return nil
}

func (execution *Execution) repositoryCredential(ctx context.Context, captured CapturedRepository) (*sourcegit.Credential, error) {
	if captured.Privacy == Public {
		return nil, nil
	}
	if captured.CredentialID == nil || captured.CredentialVersion == nil || captured.CredentialUsername == nil {
		return nil, ErrCredentialUnavailable
	}
	secret, err := execution.secrets.Read(ctx, credentials.ID(*captured.CredentialID), credentials.RepositoryHTTPS, int32(*captured.CredentialVersion))
	if err != nil {
		return nil, ErrCredentialUnavailable
	}
	return sourcegit.NewCredential(*captured.CredentialUsername, secret.Reveal())
}

func (execution *Execution) websiteRequest(ctx context.Context, run Sync, revisionID *ID) (WebsiteRequest, error) {
	captured := run.Website
	request := WebsiteRequest{SourceID: run.SourceID, RevisionID: cloneID(revisionID), RemoteURL: captured.Remote.URL, Limits: captured.Limits, Mode: captured.AcquisitionMode, PreviousRevisionID: cloneID(captured.PreviousRevisionID)}
	if captured.Privacy == Private {
		if captured.CredentialID == nil || captured.CredentialVersion == nil || captured.CredentialHeader == nil {
			return WebsiteRequest{}, ErrCredentialUnavailable
		}
		secret, err := execution.secrets.Read(ctx, credentials.ID(*captured.CredentialID), credentials.WebsiteHeader, int32(*captured.CredentialVersion))
		if err != nil {
			return WebsiteRequest{}, ErrCredentialUnavailable
		}
		combined, err := security.NewSecretValue(valueOrEmpty(captured.CredentialPrefix) + secret.Reveal())
		if err != nil {
			return WebsiteRequest{}, ErrCredentialUnavailable
		}
		request.Credential = &WebsiteCredential{Header: *captured.CredentialHeader, Value: combined}
	}
	if captured.AcquisitionMode == TinyFishCrawl {
		if captured.TinyFishCredentialID == nil || captured.TinyFishCredentialVersion == nil {
			return WebsiteRequest{}, ErrCredentialUnavailable
		}
		secret, err := execution.secrets.Read(ctx, credentials.ID(*captured.TinyFishCredentialID), credentials.TinyFishAPIKey, int32(*captured.TinyFishCredentialVersion))
		if err != nil {
			return WebsiteRequest{}, ErrCredentialUnavailable
		}
		request.TinyFishCredential = secret
	}
	return request, nil
}

func validationFailure(id ID, message string, retryable bool) ValidationCompletion {
	return ValidationCompletion{SyncID: id, SanitizedError: stringValue(message), Retryable: retryable}
}

func syncFailure(id ID, message string, retryable bool) SyncCompletion {
	return SyncCompletion{SyncID: id, SanitizedError: stringValue(message), Retryable: retryable}
}

func websiteError(stage string, mode AcquisitionMode, code string) string {
	if mode == TinyFishCrawl {
		prefix := stage + ":tinyfish_"
		if len(code) >= len(prefix) && code[:len(prefix)] == prefix {
			return code
		}
		return prefix + code
	}
	return stage + ":website_" + code
}

func stringValue(value string) *string { return &value }
func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

type RuntimeConfig struct {
	AllowPrivateNetwork bool
	GitCAFile           string
	PollScanEvery       time.Duration
	PollBatchSize       int
}

func RuntimeConfigFromEnvironment() (RuntimeConfig, error) {
	config := RuntimeConfig{PollScanEvery: 30 * time.Second, PollBatchSize: 50}
	if raw := os.Getenv("SOURCE_ALLOW_PRIVATE_NETWORK"); raw != "" {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return RuntimeConfig{}, errors.New("SOURCE_ALLOW_PRIVATE_NETWORK must be a boolean")
		}
		config.AllowPrivateNetwork = value
	}
	config.GitCAFile = os.Getenv("SOURCE_GIT_CA_FILE")
	if raw := os.Getenv("SOURCE_POLL_SCAN_SECONDS"); raw != "" {
		seconds, err := strconv.ParseFloat(raw, 64)
		if err != nil || seconds <= 0 || seconds > 300 {
			return RuntimeConfig{}, errors.New("SOURCE_POLL_SCAN_SECONDS must be greater than 0 and at most 300")
		}
		config.PollScanEvery = time.Duration(seconds * float64(time.Second))
	}
	if raw := os.Getenv("SOURCE_POLL_BATCH_SIZE"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 50 {
			return RuntimeConfig{}, errors.New("SOURCE_POLL_BATCH_SIZE must be between 1 and 50")
		}
		config.PollBatchSize = value
	}
	return config, nil
}

func NewRepositoryRuntime(artifacts *sourcefiles.Store, config RuntimeConfig) (*sourcegit.Validator, *sourcegit.Acquirer, error) {
	if artifacts == nil {
		return nil, nil, errors.New("source artifact store is required")
	}
	validator, err := sourcegit.NewValidator(sourcegit.ValidatorOptions{
		Policy: sourcegit.NetworkPolicy{AllowPrivateAddresses: config.AllowPrivateNetwork},
		CAFile: config.GitCAFile,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("construct source Git validator: %w", err)
	}
	acquirer, err := sourcegit.NewAcquirer(artifacts, validator, sourcegit.DefaultLimits())
	if err != nil {
		return nil, nil, fmt.Errorf("construct repository source adapter: %w", err)
	}
	return validator, acquirer, nil
}
