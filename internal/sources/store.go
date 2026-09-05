package sources

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/cyr1en/ref0/internal/events"
	"github.com/cyr1en/ref0/internal/idempotency"
	"github.com/cyr1en/ref0/internal/jobs"
	"github.com/cyr1en/ref0/internal/security"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

const idempotencyTTL = 24 * time.Hour

type Queue interface {
	EnqueueTx(context.Context, pgx.Tx, jobs.Command) (jobs.JobID, error)
	AssertPermit(context.Context, pgx.Tx, jobs.Permit) error
}

type Digester interface {
	KeyedDigests(...[]byte) ([][]byte, error)
}

type Store struct {
	pool     *pgxpool.Pool
	queue    Queue
	digester Digester
}

func NewStore(pool *pgxpool.Pool, queue Queue, digester Digester) (*Store, error) {
	if pool == nil || queue == nil || digester == nil {
		return nil, errors.New("source store dependencies are incomplete")
	}
	return &Store{pool: pool, queue: queue, digester: digester}, nil
}

type CreateRepository struct {
	KnowledgeBaseID ID
	Configuration   RepositoryConfiguration
}

type CreateWebsite struct {
	KnowledgeBaseID ID
	Configuration   WebsiteConfiguration
}

type UpdateRepository struct {
	SourceID        ID
	ExpectedVersion int
	Configuration   RepositoryConfiguration
}

type UpdateWebsite struct {
	SourceID        ID
	ExpectedVersion int
	Configuration   WebsiteConfiguration
}

type ChangeLifecycle struct {
	SourceID        ID
	ExpectedVersion int
	Lifecycle       Lifecycle
}

type RequestOperation struct {
	SourceID        ID
	ExpectedVersion int
}

func (store *Store) List(ctx context.Context, knowledgeBaseID *ID) ([]Source, error) {
	query := sourceSelect
	arguments := []any{}
	if knowledgeBaseID != nil {
		query += " WHERE s.knowledge_base_id = $1"
		arguments = append(arguments, pgUUID(*knowledgeBaseID))
	}
	query += " ORDER BY s.created_at, s.id"
	rows, err := store.pool.Query(ctx, query, arguments...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Source{}
	for rows.Next() {
		value, err := scanSource(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func (store *Store) Get(ctx context.Context, id ID) (Source, error) {
	return getSource(ctx, store.pool, id, false)
}

func (store *Store) CreateRepository(ctx context.Context, command CreateRepository, actor ID, requestKey string) (Created, error) {
	config, err := command.Configuration.normalize()
	if err != nil {
		return Created{}, err
	}
	payload := map[string]any{"knowledge_base_id": command.KnowledgeBaseID.String(), "configuration": repositoryConfigurationPayload(config)}
	request, err := store.request(actor, requestKey, "repository_source.create", payload)
	if err != nil {
		return Created{}, err
	}
	var result Created
	err = store.transaction(ctx, func(tx pgx.Tx) error {
		stored, err := idempotency.Execute(ctx, tx, request, func(ctx context.Context, tx pgx.Tx) (idempotency.Result, error) {
			return store.createRepositoryOnce(ctx, tx, command.KnowledgeBaseID, config, actor)
		})
		if err != nil {
			return err
		}
		if stored.Type != "source_sync" {
			return idempotency.ErrConflict
		}
		validation, err := getSync(ctx, tx, ID(stored.ID), false)
		if err != nil {
			return err
		}
		source, err := getSource(ctx, tx, validation.SourceID, false)
		if err != nil {
			return err
		}
		if source.Kind != Repository {
			return idempotency.ErrConflict
		}
		result = Created{Source: source, Validation: validation}
		return nil
	})
	return result, translateUnique(err)
}

func (store *Store) CreateWebsite(ctx context.Context, command CreateWebsite, actor ID, requestKey string) (Created, error) {
	config, err := command.Configuration.normalize()
	if err != nil {
		return Created{}, err
	}
	payload := map[string]any{"knowledge_base_id": command.KnowledgeBaseID.String(), "configuration": websiteConfigurationPayload(config)}
	request, err := store.request(actor, requestKey, "website_source.create", payload)
	if err != nil {
		return Created{}, err
	}
	var result Created
	err = store.transaction(ctx, func(tx pgx.Tx) error {
		stored, err := idempotency.Execute(ctx, tx, request, func(ctx context.Context, tx pgx.Tx) (idempotency.Result, error) {
			return store.createWebsiteOnce(ctx, tx, command.KnowledgeBaseID, config, actor)
		})
		if err != nil {
			return err
		}
		if stored.Type != "source_sync" {
			return idempotency.ErrConflict
		}
		validation, err := getSync(ctx, tx, ID(stored.ID), false)
		if err != nil {
			return err
		}
		source, err := getSource(ctx, tx, validation.SourceID, false)
		if err != nil {
			return err
		}
		if source.Kind != Website {
			return idempotency.ErrConflict
		}
		result = Created{Source: source, Validation: validation}
		return nil
	})
	return result, translateUnique(err)
}

func (store *Store) UpdateRepository(ctx context.Context, command UpdateRepository, actor ID, requestKey string) (Source, error) {
	if command.ExpectedVersion <= 0 {
		return Source{}, errors.New("expected_version must be positive")
	}
	config, err := command.Configuration.normalize()
	if err != nil {
		return Source{}, err
	}
	payload := map[string]any{"source_id": command.SourceID.String(), "expected_version": command.ExpectedVersion, "configuration": repositoryConfigurationPayload(config)}
	request, err := store.request(actor, requestKey, "repository_source.update", payload)
	if err != nil {
		return Source{}, err
	}
	return store.update(ctx, request, command.SourceID, Repository, func(ctx context.Context, tx pgx.Tx) (idempotency.Result, error) {
		return store.updateRepositoryOnce(ctx, tx, command.SourceID, command.ExpectedVersion, config, actor)
	})
}

func (store *Store) UpdateWebsite(ctx context.Context, command UpdateWebsite, actor ID, requestKey string) (Source, error) {
	if command.ExpectedVersion <= 0 {
		return Source{}, errors.New("expected_version must be positive")
	}
	config, err := command.Configuration.normalize()
	if err != nil {
		return Source{}, err
	}
	payload := map[string]any{"source_id": command.SourceID.String(), "expected_version": command.ExpectedVersion, "configuration": websiteConfigurationPayload(config)}
	request, err := store.request(actor, requestKey, "website_source.update", payload)
	if err != nil {
		return Source{}, err
	}
	return store.update(ctx, request, command.SourceID, Website, func(ctx context.Context, tx pgx.Tx) (idempotency.Result, error) {
		return store.updateWebsiteOnce(ctx, tx, command.SourceID, command.ExpectedVersion, config, actor)
	})
}

func (store *Store) ChangeLifecycle(ctx context.Context, command ChangeLifecycle, actor ID, requestKey string) (Source, error) {
	if command.ExpectedVersion <= 0 {
		return Source{}, errors.New("expected_version must be positive")
	}
	if command.Lifecycle == Draft {
		return Source{}, errors.New("source lifecycle cannot be changed to draft directly")
	}
	payload := map[string]any{"source_id": command.SourceID.String(), "expected_version": command.ExpectedVersion, "lifecycle": string(command.Lifecycle)}
	request, err := store.request(actor, requestKey, "source.lifecycle.change", payload)
	if err != nil {
		return Source{}, err
	}
	return store.update(ctx, request, command.SourceID, "", func(ctx context.Context, tx pgx.Tx) (idempotency.Result, error) {
		return store.changeLifecycleOnce(ctx, tx, command, actor)
	})
}

func (store *Store) RequestValidation(ctx context.Context, command RequestOperation, actor ID, requestKey string) (Sync, error) {
	return store.requestOperation(ctx, command, actor, requestKey, Validation)
}

func (store *Store) RequestSync(ctx context.Context, command RequestOperation, actor ID, requestKey string) (Sync, error) {
	return store.requestOperation(ctx, command, actor, requestKey, Synchronization)
}

func (store *Store) requestOperation(ctx context.Context, command RequestOperation, actor ID, requestKey string, kind SyncKind) (Sync, error) {
	if command.ExpectedVersion <= 0 {
		return Sync{}, errors.New("expected_version must be positive")
	}
	operation := "source.validation.request"
	if kind == Synchronization {
		operation = "source.sync.request"
	}
	request, err := store.request(actor, requestKey, operation, map[string]any{"source_id": command.SourceID.String(), "expected_version": command.ExpectedVersion})
	if err != nil {
		return Sync{}, err
	}
	var result Sync
	err = store.transaction(ctx, func(tx pgx.Tx) error {
		stored, err := idempotency.Execute(ctx, tx, request, func(ctx context.Context, tx pgx.Tx) (idempotency.Result, error) {
			return store.scheduleRequestedOnce(ctx, tx, command, actor, kind)
		})
		if err != nil {
			return err
		}
		if stored.Type != "source_sync" {
			return idempotency.ErrConflict
		}
		result, err = getSync(ctx, tx, ID(stored.ID), false)
		return err
	})
	return result, err
}

func (store *Store) update(ctx context.Context, request idempotency.Request, id ID, expectedKind Kind, operation idempotency.Operation) (Source, error) {
	var result Source
	err := store.transaction(ctx, func(tx pgx.Tx) error {
		stored, err := idempotency.Execute(ctx, tx, request, operation)
		if err != nil {
			return err
		}
		parts := strings.Split(stored.Type, ":")
		if len(parts) != 2 {
			return idempotency.ErrConflict
		}
		result, err = getSource(ctx, tx, ID(stored.ID), false)
		if err != nil {
			return err
		}
		resourceType := strings.ToLower(string(result.Kind)) + "_source"
		if parts[0] != resourceType || fmt.Sprint(result.Version) != parts[1] || expectedKind != "" && result.Kind != expectedKind || result.ID != id {
			return idempotency.ErrConflict
		}
		return nil
	})
	return result, translateUnique(err)
}

func (store *Store) createRepositoryOnce(ctx context.Context, tx pgx.Tx, knowledgeBaseID ID, config RepositoryConfiguration, actor ID) (idempotency.Result, error) {
	if err := knowledgeBasePolicy(ctx, tx, knowledgeBaseID, config.Privacy, true); err != nil {
		return idempotency.Result{}, err
	}
	credentialVersion, err := credentialVersion(ctx, tx, config.CredentialID, security.CredentialRepositoryHTTPS, true)
	if err != nil {
		return idempotency.Result{}, err
	}
	now, err := databaseTime(ctx, tx)
	if err != nil {
		return idempotency.Result{}, err
	}
	id, err := NewID()
	if err != nil {
		return idempotency.Result{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO sources (id, knowledge_base_id, kind, display_name, display_key, privacy, lifecycle, health, version, configuration_version, created_at, updated_at) VALUES ($1,$2,'REPOSITORY',$3,$4,$5,'DRAFT','UNKNOWN',1,1,$6,$6)`, pgUUID(id), pgUUID(knowledgeBaseID), config.Name.Display, config.Name.Key, config.Privacy, now)
	if err != nil {
		return idempotency.Result{}, err
	}
	include, _ := json.Marshal(config.IncludePatterns)
	exclude, _ := json.Marshal(config.ExcludePatterns)
	_, err = tx.Exec(ctx, `INSERT INTO repository_sources (source_id, remote_url, credential_username, credential_id, ref_kind, ref_value, include_patterns, exclude_patterns, poll_interval_seconds) VALUES ($1,$2,$3,$4,$5,$6,$7::jsonb,$8::jsonb,$9)`, pgUUID(id), config.Remote.URL, config.CredentialUsername, nullableUUID(config.CredentialID), config.Reference.Kind, config.Reference.Value, include, exclude, config.PollIntervalSeconds)
	if err != nil {
		return idempotency.Result{}, err
	}
	value, err := getSource(ctx, tx, id, false)
	if err != nil {
		return idempotency.Result{}, err
	}
	if err := recordSource(ctx, tx, value, &actor, "repository_source.create", "repository_source.created"); err != nil {
		return idempotency.Result{}, err
	}
	sync, err := store.scheduleOnce(ctx, tx, value, credentialVersion, nil, &actor, Validation, now)
	if err != nil {
		return idempotency.Result{}, err
	}
	return idempotency.Result{Type: "source_sync", ID: sync.ID}, nil
}

func (store *Store) createWebsiteOnce(ctx context.Context, tx pgx.Tx, knowledgeBaseID ID, config WebsiteConfiguration, actor ID) (idempotency.Result, error) {
	if err := knowledgeBasePolicy(ctx, tx, knowledgeBaseID, config.Privacy, true); err != nil {
		return idempotency.Result{}, err
	}
	sourceCredentialVersion, err := credentialVersion(ctx, tx, config.CredentialID, security.CredentialWebsiteHeader, true)
	if err != nil {
		return idempotency.Result{}, err
	}
	var tinyVersion *int
	if config.AcquisitionMode == TinyFishCrawl {
		tinyVersion, err = credentialVersion(ctx, tx, config.TinyFishCredentialID, security.CredentialTinyFishAPIKey, true)
		if err != nil {
			return idempotency.Result{}, err
		}
	}
	now, err := databaseTime(ctx, tx)
	if err != nil {
		return idempotency.Result{}, err
	}
	id, err := NewID()
	if err != nil {
		return idempotency.Result{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO sources (id, knowledge_base_id, kind, display_name, display_key, privacy, lifecycle, health, version, configuration_version, created_at, updated_at) VALUES ($1,$2,'WEBSITE',$3,$4,$5,'DRAFT','UNKNOWN',1,1,$6,$6)`, pgUUID(id), pgUUID(knowledgeBaseID), config.Name.Display, config.Name.Key, config.Privacy, now)
	if err != nil {
		return idempotency.Result{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO website_sources (source_id, root_url, credential_header, credential_prefix, credential_id, max_concurrency, requests_per_second, max_pages, max_page_bytes, max_total_bytes, max_depth, poll_interval_seconds, acquisition_mode, tinyfish_credential_id) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`, pgUUID(id), config.Remote.URL, config.CredentialHeader, config.CredentialPrefix, nullableUUID(config.CredentialID), config.Limits.Concurrency, config.Limits.RequestsPerSecond, config.Limits.MaxPages, config.Limits.MaxPageBytes, config.Limits.MaxTotalBytes, config.Limits.MaxDepth, config.PollIntervalSeconds, config.AcquisitionMode, nullableUUID(config.TinyFishCredentialID))
	if err != nil {
		return idempotency.Result{}, err
	}
	value, err := getSource(ctx, tx, id, false)
	if err != nil {
		return idempotency.Result{}, err
	}
	if err := recordSource(ctx, tx, value, &actor, "website_source.create", "website_source.created"); err != nil {
		return idempotency.Result{}, err
	}
	sync, err := store.scheduleOnce(ctx, tx, value, sourceCredentialVersion, tinyVersion, &actor, Validation, now)
	if err != nil {
		return idempotency.Result{}, err
	}
	return idempotency.Result{Type: "source_sync", ID: sync.ID}, nil
}

func (store *Store) updateRepositoryOnce(ctx context.Context, tx pgx.Tx, id ID, expected int, config RepositoryConfiguration, actor ID) (idempotency.Result, error) {
	current, err := lockedSourceWithKnowledgeBase(ctx, tx, id)
	if err != nil {
		return idempotency.Result{}, err
	}
	if current.Version != expected {
		return idempotency.Result{}, conflict("source version is stale")
	}
	if current.Kind != Repository {
		return idempotency.Result{}, conflict("source is not a repository")
	}
	if current.Lifecycle == Removed {
		return idempotency.Result{}, conflict("removed source cannot be updated")
	}
	if err := knowledgeBasePolicy(ctx, tx, current.KnowledgeBaseID, config.Privacy, false); err != nil {
		return idempotency.Result{}, err
	}
	if _, err := credentialVersion(ctx, tx, config.CredentialID, security.CredentialRepositoryHTTPS, true); err != nil {
		return idempotency.Result{}, err
	}
	if reflect.DeepEqual(*current.Repository, config) {
		return idempotency.Result{}, conflict("repository source update has no changes")
	}
	executionChanged := !repositoryExecutionEqual(*current.Repository, config)
	now, err := databaseTime(ctx, tx)
	if err != nil {
		return idempotency.Result{}, err
	}
	lifecycle := current.Lifecycle
	if executionChanged && lifecycle == Active {
		lifecycle = Draft
	}
	version, configurationVersion := current.Version+1, current.ConfigurationVersion
	if executionChanged {
		configurationVersion++
	}
	_, err = tx.Exec(ctx, `UPDATE sources SET display_name=$2, display_key=$3, privacy=$4, lifecycle=$5, version=$6, configuration_version=$7, health=CASE WHEN $8 THEN 'UNKNOWN' ELSE health END, sanitized_error=CASE WHEN $8 THEN NULL ELSE sanitized_error END, checked_at=CASE WHEN $8 THEN NULL ELSE checked_at END, disabled_at=CASE WHEN $8 AND $5 <> 'DISABLED' THEN NULL ELSE disabled_at END, updated_at=$9 WHERE id=$1`, pgUUID(id), config.Name.Display, config.Name.Key, config.Privacy, lifecycle, version, configurationVersion, executionChanged, now)
	if err != nil {
		return idempotency.Result{}, err
	}
	include, _ := json.Marshal(config.IncludePatterns)
	exclude, _ := json.Marshal(config.ExcludePatterns)
	_, err = tx.Exec(ctx, `UPDATE repository_sources SET remote_url=$2, credential_username=$3, credential_id=$4, ref_kind=$5, ref_value=$6, include_patterns=$7::jsonb, exclude_patterns=$8::jsonb, poll_interval_seconds=$9 WHERE source_id=$1`, pgUUID(id), config.Remote.URL, config.CredentialUsername, nullableUUID(config.CredentialID), config.Reference.Kind, config.Reference.Value, include, exclude, config.PollIntervalSeconds)
	if err != nil {
		return idempotency.Result{}, err
	}
	value, err := getSource(ctx, tx, id, false)
	if err != nil {
		return idempotency.Result{}, err
	}
	if err := recordSource(ctx, tx, value, &actor, "repository_source.update", "repository_source.updated"); err != nil {
		return idempotency.Result{}, err
	}
	return idempotency.Result{Type: fmt.Sprintf("repository_source:%d", version), ID: id}, nil
}

func (store *Store) updateWebsiteOnce(ctx context.Context, tx pgx.Tx, id ID, expected int, config WebsiteConfiguration, actor ID) (idempotency.Result, error) {
	current, err := lockedSourceWithKnowledgeBase(ctx, tx, id)
	if err != nil {
		return idempotency.Result{}, err
	}
	if current.Version != expected {
		return idempotency.Result{}, conflict("source version is stale")
	}
	if current.Kind != Website {
		return idempotency.Result{}, conflict("source is not a website")
	}
	if current.Lifecycle == Removed {
		return idempotency.Result{}, conflict("removed source cannot be updated")
	}
	if err := knowledgeBasePolicy(ctx, tx, current.KnowledgeBaseID, config.Privacy, false); err != nil {
		return idempotency.Result{}, err
	}
	if _, err := credentialVersion(ctx, tx, config.CredentialID, security.CredentialWebsiteHeader, true); err != nil {
		return idempotency.Result{}, err
	}
	if config.AcquisitionMode == TinyFishCrawl {
		if _, err := credentialVersion(ctx, tx, config.TinyFishCredentialID, security.CredentialTinyFishAPIKey, true); err != nil {
			return idempotency.Result{}, err
		}
	}
	if reflect.DeepEqual(*current.Website, config) {
		return idempotency.Result{}, conflict("website source update has no changes")
	}
	executionChanged := !websiteExecutionEqual(*current.Website, config)
	now, err := databaseTime(ctx, tx)
	if err != nil {
		return idempotency.Result{}, err
	}
	lifecycle := current.Lifecycle
	if executionChanged && lifecycle == Active {
		lifecycle = Draft
	}
	version, configurationVersion := current.Version+1, current.ConfigurationVersion
	if executionChanged {
		configurationVersion++
	}
	_, err = tx.Exec(ctx, `UPDATE sources SET display_name=$2, display_key=$3, privacy=$4, lifecycle=$5, version=$6, configuration_version=$7, health=CASE WHEN $8 THEN 'UNKNOWN' ELSE health END, sanitized_error=CASE WHEN $8 THEN NULL ELSE sanitized_error END, checked_at=CASE WHEN $8 THEN NULL ELSE checked_at END, disabled_at=CASE WHEN $8 AND $5 <> 'DISABLED' THEN NULL ELSE disabled_at END, updated_at=$9 WHERE id=$1`, pgUUID(id), config.Name.Display, config.Name.Key, config.Privacy, lifecycle, version, configurationVersion, executionChanged, now)
	if err != nil {
		return idempotency.Result{}, err
	}
	_, err = tx.Exec(ctx, `UPDATE website_sources SET root_url=$2, credential_header=$3, credential_prefix=$4, credential_id=$5, max_concurrency=$6, requests_per_second=$7, max_pages=$8, max_page_bytes=$9, max_total_bytes=$10, max_depth=$11, poll_interval_seconds=$12, acquisition_mode=$13, tinyfish_credential_id=$14 WHERE source_id=$1`, pgUUID(id), config.Remote.URL, config.CredentialHeader, config.CredentialPrefix, nullableUUID(config.CredentialID), config.Limits.Concurrency, config.Limits.RequestsPerSecond, config.Limits.MaxPages, config.Limits.MaxPageBytes, config.Limits.MaxTotalBytes, config.Limits.MaxDepth, config.PollIntervalSeconds, config.AcquisitionMode, nullableUUID(config.TinyFishCredentialID))
	if err != nil {
		return idempotency.Result{}, err
	}
	value, err := getSource(ctx, tx, id, false)
	if err != nil {
		return idempotency.Result{}, err
	}
	if err := recordSource(ctx, tx, value, &actor, "website_source.update", "website_source.updated"); err != nil {
		return idempotency.Result{}, err
	}
	return idempotency.Result{Type: fmt.Sprintf("website_source:%d", version), ID: id}, nil
}

func (store *Store) changeLifecycleOnce(ctx context.Context, tx pgx.Tx, command ChangeLifecycle, actor ID) (idempotency.Result, error) {
	current, err := lockedSourceWithKnowledgeBase(ctx, tx, command.SourceID)
	if err != nil {
		return idempotency.Result{}, err
	}
	if current.Version != command.ExpectedVersion {
		return idempotency.Result{}, conflict("source version is stale")
	}
	target, err := Transition(current.Lifecycle, command.Lifecycle)
	if err != nil {
		return idempotency.Result{}, err
	}
	if target == Active {
		if err := knowledgeBasePolicy(ctx, tx, current.KnowledgeBaseID, current.Privacy, false); err != nil {
			return idempotency.Result{}, err
		}
		if current.Health != Healthy || current.ValidatedConfigurationVersion == nil || *current.ValidatedConfigurationVersion != current.ConfigurationVersion {
			return idempotency.Result{}, conflict("source configuration must be validated before activation")
		}
	}
	now, err := databaseTime(ctx, tx)
	if err != nil {
		return idempotency.Result{}, err
	}
	version := current.Version + 1
	_, err = tx.Exec(ctx, `UPDATE sources SET lifecycle=$2::varchar, disabled_at=CASE WHEN $2::varchar='DISABLED' THEN $3 ELSE NULL END, removed_at=CASE WHEN $2::varchar='REMOVED' THEN $3 ELSE NULL END, updated_at=$3, version=$4 WHERE id=$1`, pgUUID(command.SourceID), target, now, version)
	if err != nil {
		return idempotency.Result{}, err
	}
	value, err := getSource(ctx, tx, command.SourceID, false)
	if err != nil {
		return idempotency.Result{}, err
	}
	if err := recordSource(ctx, tx, value, &actor, "source.lifecycle.change", "source."+strings.ToLower(string(target))); err != nil {
		return idempotency.Result{}, err
	}
	return idempotency.Result{Type: fmt.Sprintf("%s_source:%d", strings.ToLower(string(current.Kind)), version), ID: command.SourceID}, nil
}

func (store *Store) scheduleRequestedOnce(ctx context.Context, tx pgx.Tx, command RequestOperation, actor ID, kind SyncKind) (idempotency.Result, error) {
	value, err := lockedSourceWithKnowledgeBase(ctx, tx, command.SourceID)
	if err != nil {
		return idempotency.Result{}, err
	}
	if value.Version != command.ExpectedVersion {
		return idempotency.Result{}, conflict("source version is stale")
	}
	if value.Lifecycle == Removed {
		return idempotency.Result{}, conflict("removed source cannot be validated or synced")
	}
	if kind == Synchronization && value.Lifecycle != Active {
		return idempotency.Result{}, conflict("only active sources can be synced")
	}
	if err := knowledgeBasePolicy(ctx, tx, value.KnowledgeBaseID, value.Privacy, false); err != nil {
		return idempotency.Result{}, err
	}
	credentialID, credentialKind := sourceCredential(value)
	sourceCredentialVersion, err := credentialVersion(ctx, tx, credentialID, credentialKind, true)
	if err != nil {
		return idempotency.Result{}, err
	}
	var tinyVersion *int
	if value.Website != nil && value.Website.AcquisitionMode == TinyFishCrawl {
		tinyVersion, err = credentialVersion(ctx, tx, value.Website.TinyFishCredentialID, security.CredentialTinyFishAPIKey, true)
		if err != nil {
			return idempotency.Result{}, err
		}
	}
	now, err := databaseTime(ctx, tx)
	if err != nil {
		return idempotency.Result{}, err
	}
	sync, err := store.scheduleOnce(ctx, tx, value, sourceCredentialVersion, tinyVersion, &actor, kind, now)
	if err != nil {
		return idempotency.Result{}, err
	}
	return idempotency.Result{Type: "source_sync", ID: sync.ID}, nil
}

func (store *Store) scheduleOnce(ctx context.Context, tx pgx.Tx, source Source, credentialVersion, tinyVersion *int, actor *ID, kind SyncKind, now time.Time) (Sync, error) {
	syncID, err := NewID()
	if err != nil {
		return Sync{}, err
	}
	var candidateID *ID
	if kind == Synchronization {
		value, err := NewID()
		if err != nil {
			return Sync{}, err
		}
		candidateID = &value
	}
	operationKey := operationKey(kind, source, credentialVersion, tinyVersion)
	jobType := jobs.ValidateSource
	if kind == Synchronization {
		jobType = jobs.SyncSource
	}
	jobID, err := store.queue.EnqueueTx(ctx, tx, jobs.Command{Type: jobType, TargetType: "source", TargetID: jobs.UUID(source.ID), Payload: map[string]any{"source_sync_id": syncID.String()}, OperationKey: operationKey, MaxAttempts: 3})
	if err != nil {
		return Sync{}, err
	}
	var existingID pgtype.UUID
	err = tx.QueryRow(ctx, `SELECT id FROM source_syncs WHERE job_id=$1`, pgUUID(ID(jobID))).Scan(&existingID)
	if err == nil {
		return getSync(ctx, tx, ID(existingID.Bytes), false)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Sync{}, err
	}
	var requested any
	if actor != nil {
		requested = pgUUID(*actor)
	}
	if source.Repository != nil {
		config := source.Repository
		include, _ := json.Marshal(config.IncludePatterns)
		exclude, _ := json.Marshal(config.ExcludePatterns)
		_, err = tx.Exec(ctx, `INSERT INTO source_syncs (id,source_id,job_id,sync_kind,requested_by_operator_id,captured_source_version,captured_configuration_version,captured_privacy,captured_remote_url,captured_credential_username,captured_credential_id,captured_credential_version,captured_ref_kind,captured_ref_value,captured_include_patterns,captured_exclude_patterns,candidate_revision_id,status,created_at,captured_source_kind) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15::jsonb,$16::jsonb,$17,'PENDING',$18,'REPOSITORY')`, pgUUID(syncID), pgUUID(source.ID), pgUUID(ID(jobID)), kind, requested, source.Version, source.ConfigurationVersion, source.Privacy, config.Remote.URL, config.CredentialUsername, nullableUUID(config.CredentialID), credentialVersion, config.Reference.Kind, config.Reference.Value, include, exclude, nullableUUID(candidateID), now)
	} else {
		config := source.Website
		_, err = tx.Exec(ctx, `INSERT INTO source_syncs (id,source_id,job_id,sync_kind,requested_by_operator_id,captured_source_version,captured_configuration_version,captured_privacy,captured_remote_url,captured_credential_header,captured_credential_prefix,captured_credential_id,captured_credential_version,candidate_revision_id,status,created_at,captured_source_kind,captured_max_concurrency,captured_requests_per_second,captured_max_pages,captured_max_page_bytes,captured_max_total_bytes,captured_max_depth,captured_previous_revision_id,captured_acquisition_mode,captured_tinyfish_credential_id,captured_tinyfish_credential_version) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,'PENDING',$15,'WEBSITE',$16,$17,$18,$19,$20,$21,$22,$23,$24,$25)`, pgUUID(syncID), pgUUID(source.ID), pgUUID(ID(jobID)), kind, requested, source.Version, source.ConfigurationVersion, source.Privacy, config.Remote.URL, config.CredentialHeader, config.CredentialPrefix, nullableUUID(config.CredentialID), credentialVersion, nullableUUID(candidateID), now, config.Limits.Concurrency, config.Limits.RequestsPerSecond, config.Limits.MaxPages, config.Limits.MaxPageBytes, config.Limits.MaxTotalBytes, config.Limits.MaxDepth, nullableUUID(source.CurrentRevisionID), config.AcquisitionMode, nullableUUID(config.TinyFishCredentialID), tinyVersion)
	}
	if err != nil {
		return Sync{}, err
	}
	value, err := getSync(ctx, tx, syncID, false)
	if err != nil {
		return Sync{}, err
	}
	if err := recordSync(ctx, tx, value, actor, "source_sync.scheduled"); err != nil {
		return Sync{}, err
	}
	return value, nil
}

func (store *Store) request(actor ID, key, operation string, payload map[string]any) (idempotency.Request, error) {
	encoded, err := canonicalJSON(payload)
	if err != nil {
		return idempotency.Request{}, err
	}
	digests, err := store.digester.KeyedDigests([]byte(operation), encoded)
	if err != nil || len(digests) == 0 {
		if err == nil {
			err = errors.New("source request digest is unavailable")
		}
		return idempotency.Request{}, err
	}
	convert := func(raw []byte) (idempotency.Digest, error) {
		var value idempotency.Digest
		if len(raw) != len(value) {
			return value, errors.New("source request digest is invalid")
		}
		copy(value[:], raw)
		return value, nil
	}
	current, err := convert(digests[0])
	if err != nil {
		return idempotency.Request{}, err
	}
	accepted := make([]idempotency.Digest, 0, len(digests)-1)
	for _, raw := range digests[1:] {
		value, err := convert(raw)
		if err != nil {
			return idempotency.Request{}, err
		}
		accepted = append(accepted, value)
	}
	return idempotency.Request{Scope: "operator:" + actor.String(), Key: key, Operation: operation, Digest: current, AcceptedDigests: accepted, TTL: idempotencyTTL}, nil
}

func (store *Store) transaction(ctx context.Context, operation func(pgx.Tx) error) error {
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := operation(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func translateUnique(err error) error {
	var postgres *pgconn.PgError
	if errors.As(err, &postgres) && postgres.Code == "23505" {
		return conflict("source resource already exists")
	}
	return err
}

func databaseTime(ctx context.Context, querier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}) (time.Time, error) {
	var value pgtype.Timestamptz
	if err := querier.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&value); err != nil || !value.Valid {
		if err == nil {
			err = errors.New("database clock did not return a timestamp")
		}
		return time.Time{}, err
	}
	return value.Time, nil
}

func knowledgeBasePolicy(ctx context.Context, tx pgx.Tx, id ID, privacy Privacy, lock bool) error {
	query := "SELECT lifecycle, access_policy FROM knowledge_bases WHERE id=$1"
	if lock {
		query += " FOR UPDATE"
	}
	var lifecycle, access string
	if err := tx.QueryRow(ctx, query, pgUUID(id)).Scan(&lifecycle, &access); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return conflict("knowledge base is unavailable")
		}
		return err
	}
	if lifecycle != "ACTIVE" {
		return conflict("source requires an active knowledge base")
	}
	if privacy == Private && access != "RESTRICTED" {
		return conflict("private source requires a restricted knowledge base")
	}
	return nil
}

func credentialVersion(ctx context.Context, tx pgx.Tx, id *ID, kind security.CredentialKind, strict bool) (*int, error) {
	if id == nil {
		return nil, nil
	}
	var storedKind string
	var version int
	var deleted pgtype.Timestamptz
	err := tx.QueryRow(ctx, `SELECT kind, secret_version, deleted_at FROM credentials WHERE id=$1 FOR UPDATE`, pgUUID(*id)).Scan(&storedKind, &version, &deleted)
	if err != nil || storedKind != string(kind) || deleted.Valid {
		if strict {
			return nil, conflict("source credential is unavailable")
		}
		return nil, nil
	}
	return &version, nil
}

func sourceCredential(value Source) (*ID, security.CredentialKind) {
	if value.Repository != nil {
		return value.Repository.CredentialID, security.CredentialRepositoryHTTPS
	}
	return value.Website.CredentialID, security.CredentialWebsiteHeader
}

func lockedSourceWithKnowledgeBase(ctx context.Context, tx pgx.Tx, id ID) (Source, error) {
	var knowledgeBaseID pgtype.UUID
	if err := tx.QueryRow(ctx, `SELECT knowledge_base_id FROM sources WHERE id=$1`, pgUUID(id)).Scan(&knowledgeBaseID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Source{}, ErrNotFound
		}
		return Source{}, err
	}
	var ignored int
	if err := tx.QueryRow(ctx, `SELECT 1 FROM knowledge_bases WHERE id=$1 FOR UPDATE`, knowledgeBaseID).Scan(&ignored); err != nil {
		return Source{}, err
	}
	return getSource(ctx, tx, id, true)
}

func repositoryExecutionEqual(left, right RepositoryConfiguration) bool {
	left.Name, right.Name = Name{}, Name{}
	left.PollIntervalSeconds, right.PollIntervalSeconds = nil, nil
	return reflect.DeepEqual(left, right)
}

func websiteExecutionEqual(left, right WebsiteConfiguration) bool {
	left.Name, right.Name = Name{}, Name{}
	left.PollIntervalSeconds, right.PollIntervalSeconds = nil, nil
	return reflect.DeepEqual(left, right)
}

func operationKey(kind SyncKind, source Source, credentialVersion, tinyVersion *int) string {
	prefix := "validate-source"
	if kind == Synchronization {
		prefix = "sync-source"
	}
	credential := "public"
	if credentialVersion != nil {
		credential = fmt.Sprint(*credentialVersion)
	}
	result := fmt.Sprintf("%s:%s:%d:%s", prefix, source.ID.String(), source.ConfigurationVersion, credential)
	if source.Website != nil {
		tiny := "public"
		if tinyVersion != nil {
			tiny = fmt.Sprint(*tinyVersion)
		}
		result += ":" + strings.ToLower(string(source.Website.AcquisitionMode)) + ":" + tiny
	}
	return result
}

func canonicalJSON(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var normalized any
	if err := decoder.Decode(&normalized); err != nil {
		return nil, err
	}
	return json.Marshal(normalized)
}

func repositoryConfigurationPayload(value RepositoryConfiguration) map[string]any {
	return map[string]any{"name": value.Name.Display, "privacy": value.Privacy, "remote_url": value.Remote.URL, "ref_kind": value.Reference.Kind, "ref_value": value.Reference.Value, "credential_username": value.CredentialUsername, "credential_id": idString(value.CredentialID), "include_patterns": value.IncludePatterns, "exclude_patterns": value.ExcludePatterns, "poll_interval_seconds": value.PollIntervalSeconds}
}

func websiteConfigurationPayload(value WebsiteConfiguration) map[string]any {
	return map[string]any{"name": value.Name.Display, "privacy": value.Privacy, "root_url": value.Remote.URL, "credential_header": value.CredentialHeader, "credential_prefix": value.CredentialPrefix, "credential_id": idString(value.CredentialID), "limits": map[string]any{"concurrency": value.Limits.Concurrency, "requests_per_second": value.Limits.RequestsPerSecond, "max_pages": value.Limits.MaxPages, "max_page_bytes": value.Limits.MaxPageBytes, "max_total_bytes": value.Limits.MaxTotalBytes, "max_depth": value.Limits.MaxDepth}, "poll_interval_seconds": value.PollIntervalSeconds, "acquisition_mode": value.AcquisitionMode, "tinyfish_credential_id": idString(value.TinyFishCredentialID)}
}

func idString(value *ID) any {
	if value == nil {
		return nil
	}
	return value.String()
}
func pgUUID(value ID) pgtype.UUID { return pgtype.UUID{Bytes: [16]byte(value), Valid: true} }
func nullableUUID(value *ID) any {
	if value == nil {
		return nil
	}
	return pgUUID(*value)
}
func timePointer(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}
	result := value.Time
	return &result
}
func intPointer(value pgtype.Int4) *int {
	if !value.Valid {
		return nil
	}
	result := int(value.Int32)
	return &result
}
func idPointer(value pgtype.UUID) *ID {
	if !value.Valid {
		return nil
	}
	result := ID(value.Bytes)
	return &result
}
func stringPointer(value pgtype.Text) *string {
	if !value.Valid {
		return nil
	}
	result := value.String
	return &result
}

const sourceSelect = `SELECT s.id,s.knowledge_base_id,s.kind,s.display_name,s.display_key,s.privacy,s.lifecycle,s.health,s.sanitized_error,s.checked_at,s.current_revision_id,s.version,s.configuration_version,s.validated_configuration_version,s.created_at,s.updated_at,s.disabled_at,s.removed_at,
 r.remote_url,r.credential_username,r.credential_id,r.ref_kind,r.ref_value,r.include_patterns,r.exclude_patterns,r.poll_interval_seconds,
 w.root_url,w.credential_header,w.credential_prefix,w.credential_id,w.max_concurrency,w.requests_per_second,w.max_pages,w.max_page_bytes,w.max_total_bytes,w.max_depth,w.acquisition_mode,w.tinyfish_credential_id,w.poll_interval_seconds
 FROM sources s LEFT JOIN repository_sources r ON r.source_id=s.id LEFT JOIN website_sources w ON w.source_id=s.id`

type scanner interface{ Scan(...any) error }

func getSource(ctx context.Context, querier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, id ID, lock bool) (Source, error) {
	query := sourceSelect + " WHERE s.id=$1"
	if lock {
		query += " FOR UPDATE OF s"
	}
	value, err := scanSource(querier.QueryRow(ctx, query, pgUUID(id)))
	if errors.Is(err, pgx.ErrNoRows) {
		return Source{}, ErrNotFound
	}
	return value, err
}

func scanSource(row scanner) (Source, error) {
	var id, kb, current, repoCredential, webCredential, tinyCredential pgtype.UUID
	var kind, display, key, privacy, lifecycle, health string
	var sanitized, repoURL, repoUsername, repoRefKind, repoRefValue, webURL, webHeader, webPrefix, webMode pgtype.Text
	var checked, created, updated, disabled, removed pgtype.Timestamptz
	var version, configurationVersion int32
	var validated, repoPoll, concurrency, rps, maxPages, maxPageBytes, maxDepth, webPoll pgtype.Int4
	var maxTotal pgtype.Int8
	var include, exclude []byte
	if err := row.Scan(&id, &kb, &kind, &display, &key, &privacy, &lifecycle, &health, &sanitized, &checked, &current, &version, &configurationVersion, &validated, &created, &updated, &disabled, &removed, &repoURL, &repoUsername, &repoCredential, &repoRefKind, &repoRefValue, &include, &exclude, &repoPoll, &webURL, &webHeader, &webPrefix, &webCredential, &concurrency, &rps, &maxPages, &maxPageBytes, &maxTotal, &maxDepth, &webMode, &tinyCredential, &webPoll); err != nil {
		return Source{}, err
	}
	if !id.Valid || !kb.Valid || !created.Valid || !updated.Valid {
		return Source{}, errors.New("stored source is invalid")
	}
	value := Source{ID: ID(id.Bytes), KnowledgeBaseID: ID(kb.Bytes), Kind: Kind(kind), Name: display, Privacy: Privacy(privacy), Lifecycle: Lifecycle(lifecycle), Health: Health(health), SanitizedError: stringPointer(sanitized), CheckedAt: timePointer(checked), CurrentRevisionID: idPointer(current), Version: int(version), ConfigurationVersion: int(configurationVersion), ValidatedConfigurationVersion: intPointer(validated), CreatedAt: created.Time, UpdatedAt: updated.Time, DisabledAt: timePointer(disabled), RemovedAt: timePointer(removed)}
	name := Name{Display: display, Key: key}
	switch value.Kind {
	case Repository:
		var includes, excludes []string
		if err := json.Unmarshal(include, &includes); err != nil {
			return Source{}, err
		}
		if err := json.Unmarshal(exclude, &excludes); err != nil {
			return Source{}, err
		}
		config, err := (RepositoryConfiguration{Name: name, Privacy: value.Privacy, Remote: Remote{URL: repoURL.String}, Reference: Reference{Kind: RefKind(repoRefKind.String), Value: repoRefValue.String}, CredentialUsername: stringPointer(repoUsername), CredentialID: idPointer(repoCredential), IncludePatterns: includes, ExcludePatterns: excludes, PollIntervalSeconds: intPointer(repoPoll)}).normalize()
		if err != nil {
			return Source{}, err
		}
		value.Repository = &config
	case Website:
		config, err := (WebsiteConfiguration{Name: name, Privacy: value.Privacy, Remote: Remote{URL: webURL.String}, CredentialHeader: stringPointer(webHeader), CredentialPrefix: stringPointer(webPrefix), CredentialID: idPointer(webCredential), Limits: CrawlLimits{Concurrency: int(concurrency.Int32), RequestsPerSecond: int(rps.Int32), MaxPages: int(maxPages.Int32), MaxPageBytes: int(maxPageBytes.Int32), MaxTotalBytes: maxTotal.Int64, MaxDepth: int(maxDepth.Int32)}, PollIntervalSeconds: intPointer(webPoll), AcquisitionMode: AcquisitionMode(webMode.String), TinyFishCredentialID: idPointer(tinyCredential)}).normalize()
		if err != nil {
			return Source{}, err
		}
		value.Website = &config
	default:
		return Source{}, errors.New("stored source kind is invalid")
	}
	return value, nil
}

const syncSelect = `SELECT id,source_id,job_id,sync_kind,requested_by_operator_id,captured_source_version,captured_configuration_version,captured_privacy,captured_remote_url,captured_credential_username,captured_credential_id,captured_credential_version,captured_ref_kind,captured_ref_value,captured_include_patterns,captured_exclude_patterns,candidate_revision_id,status,result_revision_id,resolved_native_version,sanitized_error,created_at,started_at,completed_at,captured_source_kind,captured_credential_header,captured_credential_prefix,captured_max_concurrency,captured_requests_per_second,captured_max_pages,captured_max_page_bytes,captured_max_total_bytes,captured_max_depth,captured_previous_revision_id,captured_acquisition_mode,captured_tinyfish_credential_id,captured_tinyfish_credential_version FROM source_syncs`

func getSync(ctx context.Context, querier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, id ID, lock bool) (Sync, error) {
	query := syncSelect + " WHERE id=$1"
	if lock {
		query += " FOR UPDATE"
	}
	value, err := scanSync(querier.QueryRow(ctx, query, pgUUID(id)))
	if errors.Is(err, pgx.ErrNoRows) {
		return Sync{}, ErrNotFound
	}
	return value, err
}

func scanSync(row scanner) (Sync, error) {
	var id, sourceID, jobID, requested, credentialID, candidate, result, previous, tinyID pgtype.UUID
	var kind, privacy, status, sourceKind string
	var remote, username, refKind, refValue, resolved, sanitized, header, prefix, mode pgtype.Text
	var capturedSourceVersion, capturedConfigVersion int32
	var credentialVersion, concurrency, rps, maxPages, maxPageBytes, maxDepth, tinyVersion pgtype.Int4
	var maxTotal pgtype.Int8
	var include, exclude []byte
	var created, started, completed pgtype.Timestamptz
	if err := row.Scan(&id, &sourceID, &jobID, &kind, &requested, &capturedSourceVersion, &capturedConfigVersion, &privacy, &remote, &username, &credentialID, &credentialVersion, &refKind, &refValue, &include, &exclude, &candidate, &status, &result, &resolved, &sanitized, &created, &started, &completed, &sourceKind, &header, &prefix, &concurrency, &rps, &maxPages, &maxPageBytes, &maxTotal, &maxDepth, &previous, &mode, &tinyID, &tinyVersion); err != nil {
		return Sync{}, err
	}
	value := Sync{ID: ID(id.Bytes), SourceID: ID(sourceID.Bytes), JobID: jobs.JobID(jobID.Bytes), Kind: SyncKind(kind), RequestedBy: idPointer(requested), CapturedSourceVersion: int(capturedSourceVersion), CapturedConfigurationVersion: int(capturedConfigVersion), CandidateRevisionID: idPointer(candidate), Status: SyncStatus(status), ResultRevisionID: idPointer(result), ResolvedNativeVersion: stringPointer(resolved), SanitizedError: stringPointer(sanitized), CreatedAt: created.Time, StartedAt: timePointer(started), CompletedAt: timePointer(completed)}
	if Kind(sourceKind) == Repository {
		var includes, excludes []string
		if err := json.Unmarshal(include, &includes); err != nil {
			return Sync{}, err
		}
		if err := json.Unmarshal(exclude, &excludes); err != nil {
			return Sync{}, err
		}
		value.Repository = &CapturedRepository{Privacy: Privacy(privacy), Remote: Remote{URL: remote.String}, Reference: Reference{Kind: RefKind(refKind.String), Value: refValue.String}, CredentialUsername: stringPointer(username), CredentialID: idPointer(credentialID), CredentialVersion: intPointer(credentialVersion), IncludePatterns: includes, ExcludePatterns: excludes}
	} else if Kind(sourceKind) == Website {
		value.Website = &CapturedWebsite{Privacy: Privacy(privacy), Remote: Remote{URL: remote.String}, CredentialHeader: stringPointer(header), CredentialPrefix: stringPointer(prefix), CredentialID: idPointer(credentialID), CredentialVersion: intPointer(credentialVersion), Limits: CrawlLimits{Concurrency: int(concurrency.Int32), RequestsPerSecond: int(rps.Int32), MaxPages: int(maxPages.Int32), MaxPageBytes: int(maxPageBytes.Int32), MaxTotalBytes: maxTotal.Int64, MaxDepth: int(maxDepth.Int32)}, AcquisitionMode: AcquisitionMode(mode.String), TinyFishCredentialID: idPointer(tinyID), TinyFishCredentialVersion: intPointer(tinyVersion), PreviousRevisionID: idPointer(previous)}
	} else {
		return Sync{}, errors.New("stored source sync kind is invalid")
	}
	return value, nil
}

func recordSource(ctx context.Context, tx pgx.Tx, value Source, actor *ID, action, eventType string) error {
	return record(ctx, tx, actor, action, eventType, strings.ToLower(string(value.Kind))+"_source", value.ID, sourceSnapshot(value))
}
func recordSync(ctx context.Context, tx pgx.Tx, value Sync, actor *ID, eventType string) error {
	return record(ctx, tx, actor, strings.ReplaceAll(eventType, ".", "_"), eventType, "source_sync", value.ID, syncSnapshot(value))
}
func recordRevision(ctx context.Context, tx pgx.Tx, value Revision) error {
	return record(ctx, tx, nil, "source_revision.create", "source_revision.created", "source_revision", value.ID, revisionSnapshot(value))
}

func record(ctx context.Context, tx pgx.Tx, actor *ID, action, eventType, resourceType string, resourceID ID, snapshot map[string]any) error {
	requestID, err := NewID()
	if err != nil {
		return err
	}
	actorType := "system"
	var actorID *[16]byte
	if actor != nil {
		actorType = "operator"
		value := [16]byte(*actor)
		actorID = &value
	}
	targetID := [16]byte(resourceID)
	if err = events.AppendAudit(ctx, tx, events.AuditEvent{
		ActorType: actorType, ActorID: actorID, Action: action,
		TargetType: resourceType, TargetID: &targetID,
		RequestID: [16]byte(requestID), Details: snapshot,
	}); err != nil {
		return err
	}
	return events.Append(ctx, tx, events.ResourceEvent{Type: eventType, ResourceType: resourceType, ResourceID: [16]byte(resourceID), Snapshot: snapshot})
}

func sourceSnapshot(value Source) map[string]any {
	result := map[string]any{"id": value.ID.String(), "knowledge_base_id": value.KnowledgeBaseID.String(), "kind": strings.ToLower(string(value.Kind)), "display_name": value.Name, "privacy": strings.ToLower(string(value.Privacy)), "lifecycle": strings.ToLower(string(value.Lifecycle)), "health": strings.ToLower(string(value.Health)), "sanitized_error": value.SanitizedError, "checked_at": isoTime(value.CheckedAt), "current_revision_id": idString(value.CurrentRevisionID), "version": value.Version, "configuration_version": value.ConfigurationVersion, "validated_configuration_version": value.ValidatedConfigurationVersion, "created_at": pythonISO(value.CreatedAt), "updated_at": pythonISO(value.UpdatedAt), "disabled_at": isoTime(value.DisabledAt), "removed_at": isoTime(value.RemovedAt)}
	if value.Repository != nil {
		config := value.Repository
		result["credential_id"] = idString(config.CredentialID)
		result["poll_interval_seconds"] = config.PollIntervalSeconds
		result["remote_url"] = config.Remote.URL
		result["remote_host"] = config.Remote.Host
		parsed, _ := urlPath(config.Remote.URL)
		result["repository_path"] = parsed
		result["ref_kind"] = strings.ToLower(string(config.Reference.Kind))
		result["ref_value"] = config.Reference.Value
		result["credential_username"] = config.CredentialUsername
		result["include_patterns"] = config.IncludePatterns
		result["exclude_patterns"] = config.ExcludePatterns
	} else {
		config := value.Website
		result["credential_id"] = idString(config.CredentialID)
		result["poll_interval_seconds"] = config.PollIntervalSeconds
		result["root_url"] = config.Remote.URL
		result["root_host"] = config.Remote.Host
		result["credential_header"] = config.CredentialHeader
		result["credential_prefix"] = config.CredentialPrefix
		result["max_concurrency"] = config.Limits.Concurrency
		result["requests_per_second"] = config.Limits.RequestsPerSecond
		result["max_pages"] = config.Limits.MaxPages
		result["max_page_bytes"] = config.Limits.MaxPageBytes
		result["max_total_bytes"] = config.Limits.MaxTotalBytes
		result["max_depth"] = config.Limits.MaxDepth
		result["acquisition_mode"] = strings.ToLower(string(config.AcquisitionMode))
		result["tinyfish_credential_id"] = idString(config.TinyFishCredentialID)
	}
	return result
}

func syncSnapshot(value Sync) map[string]any {
	var credentialID *ID
	var credentialVersion *int
	var mode any
	var tinyID any
	var tinyVersion any
	if value.Repository != nil {
		credentialID = value.Repository.CredentialID
		credentialVersion = value.Repository.CredentialVersion
	} else {
		credentialID = value.Website.CredentialID
		credentialVersion = value.Website.CredentialVersion
		mode = strings.ToLower(string(value.Website.AcquisitionMode))
		tinyID = idString(value.Website.TinyFishCredentialID)
		tinyVersion = value.Website.TinyFishCredentialVersion
	}
	return map[string]any{"id": value.ID.String(), "source_id": value.SourceID.String(), "job_id": value.JobID.String(), "kind": strings.ToLower(string(value.Kind)), "status": strings.ToLower(string(value.Status)), "captured_source_version": value.CapturedSourceVersion, "captured_configuration_version": value.CapturedConfigurationVersion, "captured_credential_id": idString(credentialID), "captured_credential_version": credentialVersion, "captured_acquisition_mode": mode, "captured_tinyfish_credential_id": tinyID, "captured_tinyfish_credential_version": tinyVersion, "candidate_revision_id": idString(value.CandidateRevisionID), "result_revision_id": idString(value.ResultRevisionID), "resolved_native_version": value.ResolvedNativeVersion, "sanitized_error": value.SanitizedError, "created_at": pythonISO(value.CreatedAt), "started_at": isoTime(value.StartedAt), "completed_at": isoTime(value.CompletedAt)}
}
func revisionSnapshot(value Revision) map[string]any {
	return map[string]any{"id": value.ID.String(), "source_id": value.SourceID.String(), "observed_ref_kind": strings.ToLower(string(value.ObservedRef.Kind)), "observed_ref": value.ObservedRef.Value, "native_version": value.NativeVersion, "fingerprint": hex.EncodeToString(value.Fingerprint[:]), "artifact_key": value.ArtifactKey, "file_count": value.FileCount, "byte_count": value.ByteCount, "ignored_paths": value.IgnoredPaths, "created_at": pythonISO(value.CreatedAt)}
}
func isoTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return pythonISO(*value)
}
func pythonISO(value time.Time) string {
	base := value.Format("2006-01-02T15:04:05")
	if microseconds := value.Nanosecond() / 1000; microseconds != 0 {
		base += fmt.Sprintf(".%06d", microseconds)
	}
	_, offset := value.Zone()
	sign := '+'
	if offset < 0 {
		sign = '-'
		offset = -offset
	}
	return fmt.Sprintf("%s%c%02d:%02d", base, sign, offset/3600, offset%3600/60)
}
func urlPath(raw string) (string, error) {
	start := strings.Index(raw, "://")
	if start < 0 {
		return "", errors.New("invalid URL")
	}
	remaining := raw[start+3:]
	slash := strings.IndexByte(remaining, '/')
	if slash < 0 {
		return "", nil
	}
	return strings.TrimPrefix(remaining[slash:], "/"), nil
}
