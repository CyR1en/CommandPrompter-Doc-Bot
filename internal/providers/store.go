package providers

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cyr1en/ref0/internal/credentials"
	"github.com/cyr1en/ref0/internal/events"
	"github.com/cyr1en/ref0/internal/idempotency"
	"github.com/cyr1en/ref0/internal/jobs"
	"github.com/cyr1en/ref0/internal/security"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

const idempotencyTTL = 24 * time.Hour

type Store struct {
	pool  *pgxpool.Pool
	vault *security.CredentialVault
	jobs  *jobs.Store
}

func NewStore(pool *pgxpool.Pool, vault *security.CredentialVault) (*Store, error) {
	if pool == nil || vault == nil {
		return nil, errors.New("provider store dependencies are incomplete")
	}
	return &Store{pool: pool, vault: vault, jobs: jobs.NewStore(pool, nil)}, nil
}

func (store *Store) GetEndpoint(ctx context.Context, id EndpointID) (Endpoint, error) {
	value, err := scanEndpoint(store.pool.QueryRow(ctx, endpointQuery(false), uuid(id)))
	if errors.Is(err, pgx.ErrNoRows) {
		return Endpoint{}, fmt.Errorf("%w: provider endpoint not found", ErrNotFound)
	}
	return value, err
}

func (store *Store) ListEndpoints(ctx context.Context) ([]Endpoint, error) {
	rows, err := store.pool.Query(ctx, strings.Replace(endpointQuery(false), " WHERE id=$1", " ORDER BY created_at, id", 1))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []Endpoint{}
	for rows.Next() {
		value, err := scanEndpoint(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (store *Store) CreateEndpoint(ctx context.Context, command CreateEndpoint, actor ActorID, requestKey string) (Endpoint, error) {
	command, err := command.normalize()
	if err != nil {
		return Endpoint{}, err
	}
	request, err := store.request(actor, requestKey, "provider_endpoint.create", configurationPayload(command.Configuration))
	if err != nil {
		return Endpoint{}, err
	}
	value, err := withTx(ctx, store.pool, func(tx pgx.Tx) (Endpoint, error) {
		result, err := idempotency.Execute(ctx, tx, request, func(ctx context.Context, tx pgx.Tx) (idempotency.Result, error) {
			if _, err := store.credentialVersion(ctx, tx, command.Configuration.CredentialID); err != nil {
				return idempotency.Result{}, err
			}
			id, err := newUUID()
			if err != nil {
				return idempotency.Result{}, err
			}
			headers, err := pythonCanonicalJSON(command.Configuration.Headers)
			if err != nil {
				return idempotency.Result{}, err
			}
			_, err = tx.Exec(ctx, `
				INSERT INTO provider_endpoints (
					id, display_name, display_key, base_url, credential_id, headers,
					chat_completions_path, responses_path, models_path, allow_http,
					allow_private_network, lifecycle, health, version,
					configuration_version, created_at, updated_at
				) VALUES ($1,$2,$3,$4,$5,$6::jsonb,$7,$8,$9,$10,$11,'ACTIVE','UNKNOWN',1,1,
				          clock_timestamp(),clock_timestamp())
			`, uuid(id), command.Configuration.DisplayName, command.Configuration.DisplayKey,
				command.Configuration.BaseURL, nullableUUID(command.Configuration.CredentialID), string(headers),
				command.Configuration.ChatCompletionsPath, command.Configuration.ResponsesPath,
				command.Configuration.ModelsPath, command.Configuration.AllowHTTP,
				command.Configuration.AllowPrivateNetwork)
			if err != nil {
				return idempotency.Result{}, uniqueConflict(err)
			}
			created, err := scanEndpoint(tx.QueryRow(ctx, endpointQuery(false), uuid(id)))
			if err != nil {
				return idempotency.Result{}, err
			}
			if err := store.recordEndpoint(ctx, tx, created, &actor, "provider_endpoint.create", "provider_endpoint.created"); err != nil {
				return idempotency.Result{}, err
			}
			return idempotency.Result{Type: "provider_endpoint:1", ID: id}, nil
		})
		if err != nil {
			return Endpoint{}, err
		}
		return store.endpointResult(ctx, tx, result)
	})
	return value, err
}

func (store *Store) UpdateEndpoint(ctx context.Context, command UpdateEndpoint, actor ActorID, requestKey string) (Endpoint, error) {
	command, err := command.normalize()
	if err != nil {
		return Endpoint{}, err
	}
	request, err := store.request(actor, requestKey, "provider_endpoint.update", map[string]any{
		"endpoint_id": command.EndpointID.String(), "expected_version": command.ExpectedVersion,
		"configuration": configurationPayload(command.Configuration), "lifecycle": command.Lifecycle,
	})
	if err != nil {
		return Endpoint{}, err
	}
	return withTx(ctx, store.pool, func(tx pgx.Tx) (Endpoint, error) {
		result, err := idempotency.Execute(ctx, tx, request, func(ctx context.Context, tx pgx.Tx) (idempotency.Result, error) {
			current, err := scanEndpoint(tx.QueryRow(ctx, endpointQuery(true), uuid(command.EndpointID)))
			if errors.Is(err, pgx.ErrNoRows) {
				return idempotency.Result{}, fmt.Errorf("%w: provider endpoint not found", ErrNotFound)
			}
			if err != nil {
				return idempotency.Result{}, err
			}
			if current.Version != command.ExpectedVersion {
				return idempotency.Result{}, fmt.Errorf("%w: provider resource version is stale", ErrConflict)
			}
			if _, err := store.credentialVersion(ctx, tx, command.Configuration.CredentialID); err != nil {
				return idempotency.Result{}, err
			}
			configurationChanged := !reflect.DeepEqual(current.Configuration, command.Configuration)
			lifecycleChanged := current.Lifecycle != command.Lifecycle
			if !configurationChanged && !lifecycleChanged {
				return idempotency.Result{}, fmt.Errorf("%w: provider endpoint update has no changes", ErrConflict)
			}
			runtimeChanged := !reflect.DeepEqual(runtimeKey(current.Configuration), runtimeKey(command.Configuration)) || lifecycleChanged
			headers, err := pythonCanonicalJSON(command.Configuration.Headers)
			if err != nil {
				return idempotency.Result{}, err
			}
			newVersion := current.Version + 1
			configurationVersion := current.ConfigurationVersion
			if runtimeChanged {
				configurationVersion++
			}
			health := current.Health
			healthCheckedAt := current.HealthCheckedAt
			if runtimeChanged {
				health, healthCheckedAt = Unknown, nil
			}
			_, err = tx.Exec(ctx, `
				UPDATE provider_endpoints SET
					display_name=$2, display_key=$3, base_url=$4, credential_id=$5,
					headers=$6::jsonb, chat_completions_path=$7, responses_path=$8,
					models_path=$9, allow_http=$10, allow_private_network=$11,
					lifecycle=$12, health=$13, health_checked_at=$14, version=$15,
					configuration_version=$16, archived_at=CASE WHEN $12='ARCHIVED' THEN clock_timestamp() ELSE NULL END,
					updated_at=clock_timestamp()
				WHERE id=$1
			`, uuid(command.EndpointID), command.Configuration.DisplayName, command.Configuration.DisplayKey,
				command.Configuration.BaseURL, nullableUUID(command.Configuration.CredentialID), string(headers),
				command.Configuration.ChatCompletionsPath, command.Configuration.ResponsesPath,
				command.Configuration.ModelsPath, command.Configuration.AllowHTTP,
				command.Configuration.AllowPrivateNetwork, command.Lifecycle, health,
				healthCheckedAt, newVersion, configurationVersion)
			if err != nil {
				return idempotency.Result{}, uniqueConflict(err)
			}
			updated, err := scanEndpoint(tx.QueryRow(ctx, endpointQuery(false), uuid(command.EndpointID)))
			if err != nil {
				return idempotency.Result{}, err
			}
			if err := store.recordEndpoint(ctx, tx, updated, &actor, "provider_endpoint.update", "provider_endpoint.updated"); err != nil {
				return idempotency.Result{}, err
			}
			return idempotency.Result{Type: "provider_endpoint:" + strconv.Itoa(int(newVersion)), ID: [16]byte(command.EndpointID)}, nil
		})
		if err != nil {
			return Endpoint{}, err
		}
		return store.endpointResult(ctx, tx, result)
	})
}

func (store *Store) GetProfile(ctx context.Context, id ProfileID) (Profile, error) {
	return withTx(ctx, store.pool, func(tx pgx.Tx) (Profile, error) { return getProfileTx(ctx, tx, id, false) })
}

func (store *Store) ListProfiles(ctx context.Context, endpointID *EndpointID) ([]Profile, error) {
	return withTx(ctx, store.pool, func(tx pgx.Tx) ([]Profile, error) {
		query := `SELECT id FROM model_profiles`
		args := []any{}
		if endpointID != nil {
			query += ` WHERE endpoint_id=$1`
			args = append(args, uuid(*endpointID))
		}
		query += ` ORDER BY created_at, id`
		rows, err := tx.Query(ctx, query, args...)
		if err != nil {
			return nil, err
		}
		ids := []ProfileID{}
		for rows.Next() {
			var id pgtype.UUID
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			ids = append(ids, ProfileID(id.Bytes))
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
		values := make([]Profile, 0, len(ids))
		for _, id := range ids {
			value, err := getProfileTx(ctx, tx, id, false)
			if err != nil {
				return nil, err
			}
			values = append(values, value)
		}
		return values, nil
	})
}

func (store *Store) CreateProfile(ctx context.Context, command CreateProfile, actor ActorID, requestKey string) (Profile, error) {
	command, err := command.normalize()
	if err != nil {
		return Profile{}, err
	}
	request, err := store.request(actor, requestKey, "model_profile.create", map[string]any{
		"endpoint_id": command.EndpointID.String(), "model_id": command.ModelID, "settings": settingsPayload(command.Settings),
	})
	if err != nil {
		return Profile{}, err
	}
	return withTx(ctx, store.pool, func(tx pgx.Tx) (Profile, error) {
		result, err := idempotency.Execute(ctx, tx, request, func(ctx context.Context, tx pgx.Tx) (idempotency.Result, error) {
			endpoint, err := scanEndpoint(tx.QueryRow(ctx, endpointQuery(true), uuid(command.EndpointID)))
			if errors.Is(err, pgx.ErrNoRows) {
				return idempotency.Result{}, fmt.Errorf("%w: provider endpoint not found", ErrNotFound)
			}
			if err != nil {
				return idempotency.Result{}, err
			}
			if endpoint.Lifecycle != Active {
				return idempotency.Result{}, fmt.Errorf("%w: provider endpoint is archived", ErrConflict)
			}
			profileID, err := newUUID()
			if err != nil {
				return idempotency.Result{}, err
			}
			versionID, err := newUUID()
			if err != nil {
				return idempotency.Result{}, err
			}
			_, err = tx.Exec(ctx, `
				INSERT INTO model_profiles (id, endpoint_id, model_id, availability,
					current_version_id, version, created_at, updated_at)
				VALUES ($1,$2,$3,'MANUAL',$4,1,clock_timestamp(),clock_timestamp())
			`, uuid(profileID), uuid(command.EndpointID), command.ModelID, uuid(versionID))
			if err != nil {
				return idempotency.Result{}, uniqueConflict(err)
			}
			if err := insertVersion(ctx, tx, ProfileID(profileID), ProfileVersionID(versionID), 1,
				endpoint.ConfigurationVersion, command.Settings, VersionOperator, &actor); err != nil {
				return idempotency.Result{}, err
			}
			created, err := getProfileTx(ctx, tx, ProfileID(profileID), false)
			if err != nil {
				return idempotency.Result{}, err
			}
			if err := store.recordProfile(ctx, tx, created, &actor, "model_profile.create", "model_profile.created"); err != nil {
				return idempotency.Result{}, err
			}
			return idempotency.Result{Type: "model_profile:1", ID: profileID}, nil
		})
		if err != nil {
			return Profile{}, err
		}
		return store.profileResult(ctx, tx, result)
	})
}

func (store *Store) EditProfile(ctx context.Context, command EditProfile, actor ActorID, requestKey string) (Profile, error) {
	command, err := command.normalize()
	if err != nil {
		return Profile{}, err
	}
	request, err := store.request(actor, requestKey, "model_profile.edit", map[string]any{
		"profile_id": command.ProfileID.String(), "expected_version": command.ExpectedVersion,
		"settings": settingsPayload(command.Settings),
	})
	if err != nil {
		return Profile{}, err
	}
	return withTx(ctx, store.pool, func(tx pgx.Tx) (Profile, error) {
		result, err := idempotency.Execute(ctx, tx, request, func(ctx context.Context, tx pgx.Tx) (idempotency.Result, error) {
			hint, err := scanProfileRow(tx.QueryRow(ctx, profileQuery(false), uuid(command.ProfileID)))
			if errors.Is(err, pgx.ErrNoRows) {
				return idempotency.Result{}, fmt.Errorf("%w: model profile not found", ErrNotFound)
			}
			if err != nil {
				return idempotency.Result{}, err
			}
			endpoint, err := scanEndpoint(tx.QueryRow(ctx, endpointQuery(true), uuid(hint.EndpointID)))
			if err != nil {
				return idempotency.Result{}, err
			}
			if endpoint.Lifecycle != Active {
				return idempotency.Result{}, fmt.Errorf("%w: provider endpoint is archived", ErrConflict)
			}
			current, err := getProfileTx(ctx, tx, command.ProfileID, true)
			if err != nil {
				return idempotency.Result{}, err
			}
			if current.Version != command.ExpectedVersion {
				return idempotency.Result{}, fmt.Errorf("%w: provider resource version is stale", ErrConflict)
			}
			replacement, err := ApplyOperatorEdit(current.CurrentVersion.Settings, command.Settings)
			if err != nil {
				return idempotency.Result{}, err
			}
			_, err = appendVersion(ctx, tx, current, replacement, VersionOperator, &actor, endpoint.ConfigurationVersion)
			if err != nil {
				return idempotency.Result{}, err
			}
			updated, err := getProfileTx(ctx, tx, command.ProfileID, false)
			if err != nil {
				return idempotency.Result{}, err
			}
			if err := store.recordProfile(ctx, tx, updated, &actor, "model_profile.edit", "model_profile.version_appended"); err != nil {
				return idempotency.Result{}, err
			}
			return idempotency.Result{Type: "model_profile:" + strconv.Itoa(int(updated.Version)), ID: [16]byte(command.ProfileID)}, nil
		})
		if err != nil {
			return Profile{}, err
		}
		return store.profileResult(ctx, tx, result)
	})
}

func (store *Store) request(actor ActorID, key, operation string, payload map[string]any) (idempotency.Request, error) {
	canonical, err := pythonCanonicalJSON(payload)
	if err != nil {
		return idempotency.Request{}, errors.New("provider idempotency payload is invalid")
	}
	digests, err := store.vault.KeyedDigests([]byte(operation), canonical)
	if err != nil || len(digests) == 0 {
		return idempotency.Request{}, errors.New("provider idempotency digest is unavailable")
	}
	request := idempotency.Request{
		Scope: "operator:" + actor.String(), Key: key, Operation: operation, TTL: idempotencyTTL,
	}
	copy(request.Digest[:], digests[0])
	for _, raw := range digests[1:] {
		var digest idempotency.Digest
		copy(digest[:], raw)
		request.AcceptedDigests = append(request.AcceptedDigests, digest)
	}
	return request, nil
}

func (store *Store) credentialVersion(ctx context.Context, tx pgx.Tx, id *credentials.ID) (*int32, error) {
	if id == nil {
		return nil, nil
	}
	var kind string
	var version int32
	var deleted pgtype.Timestamptz
	err := tx.QueryRow(ctx, `
		SELECT kind, secret_version, deleted_at FROM credentials WHERE id=$1 FOR UPDATE
	`, uuid(*id)).Scan(&kind, &version, &deleted)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && (kind != string(security.CredentialProviderAPIKey) || deleted.Valid) {
		return nil, fmt.Errorf("%w: provider credential is unavailable", ErrConflict)
	}
	if err != nil {
		return nil, err
	}
	return &version, nil
}

func (store *Store) endpointResult(ctx context.Context, tx pgx.Tx, result idempotency.Result) (Endpoint, error) {
	version, ok := resultVersion(result.Type, "provider_endpoint")
	if !ok {
		return Endpoint{}, idempotency.ErrConflict
	}
	value, err := scanEndpoint(tx.QueryRow(ctx, endpointQuery(false), uuid(result.ID)))
	if err != nil || value.Version != version {
		if err == nil || errors.Is(err, pgx.ErrNoRows) {
			return Endpoint{}, idempotency.ErrConflict
		}
		return Endpoint{}, err
	}
	return value, nil
}

func (store *Store) profileResult(ctx context.Context, tx pgx.Tx, result idempotency.Result) (Profile, error) {
	version, ok := resultVersion(result.Type, "model_profile")
	if !ok {
		return Profile{}, idempotency.ErrConflict
	}
	value, err := getProfileTx(ctx, tx, ProfileID(result.ID), false)
	if err != nil || value.Version != version {
		if err == nil || errors.Is(err, ErrNotFound) {
			return Profile{}, idempotency.ErrConflict
		}
		return Profile{}, err
	}
	return value, nil
}

func resultVersion(value, expected string) (int32, bool) {
	parts := strings.Split(value, ":")
	if len(parts) != 2 || parts[0] != expected {
		return 0, false
	}
	parsed, err := strconv.ParseInt(parts[1], 10, 32)
	return int32(parsed), err == nil && parsed > 0
}

func withTx[T any](ctx context.Context, pool *pgxpool.Pool, operation func(pgx.Tx) (T, error)) (value T, err error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return value, err
	}
	defer tx.Rollback(ctx)
	value, err = operation(tx)
	if err != nil {
		return value, err
	}
	return value, tx.Commit(ctx)
}

func configurationPayload(value Configuration) map[string]any {
	var credentialID any
	if value.CredentialID != nil {
		credentialID = value.CredentialID.String()
	}
	return map[string]any{
		"display_name": value.DisplayName, "display_key": value.DisplayKey,
		"base_url": value.BaseURL, "credential_id": credentialID, "headers": value.Headers,
		"chat_completions_path": value.ChatCompletionsPath, "responses_path": value.ResponsesPath,
		"models_path": value.ModelsPath, "allow_http": value.AllowHTTP,
		"allow_private_network": value.AllowPrivateNetwork,
	}
}

func runtimeKey(value Configuration) map[string]any {
	payload := configurationPayload(value)
	delete(payload, "display_name")
	delete(payload, "display_key")
	return payload
}

func insertVersion(ctx context.Context, tx pgx.Tx, profileID ProfileID, versionID ProfileVersionID,
	versionNumber, configurationVersion int32, settings Settings, source VersionSource, actor *ActorID) error {
	var reasoningJSON any
	if settings.ReasoningMapping != nil {
		reasoning, err := pythonCanonicalJSON(settings.ReasoningMapping)
		if err != nil {
			return err
		}
		reasoningJSON = string(reasoning)
	}
	extra, err := pythonCanonicalJSON(settings.ExtraBody)
	if err != nil {
		return err
	}
	origins, err := pythonCanonicalJSON(settings.MetadataOrigin)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO model_profile_versions (
			id, profile_id, version_number, configuration_version, transport,
			context_window_tokens, max_output_tokens, supports_streaming, supports_tools,
			supports_structured_output, supports_temperature, reasoning_transport,
			reasoning_mapping, timeout_seconds, max_retries, max_concurrent_tasks, extra_body, metadata_origin,
			source, created_by_operator_id, created_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13::jsonb,$14,$15,$16,$17::jsonb,$18::jsonb,$19,$20,clock_timestamp())
	`, uuid(versionID), uuid(profileID), versionNumber, configurationVersion, settings.Transport,
		settings.ContextWindowTokens, settings.MaxOutputTokens, settings.SupportsStreaming,
		settings.SupportsTools, settings.SupportsStructuredOutput, settings.SupportsTemperature,
		settings.ReasoningTransport, reasoningJSON, settings.TimeoutSeconds, settings.MaxRetries,
		settings.MaxConcurrentTasks, string(extra), string(origins), source, nullableUUID(actor))
	return err
}

func appendVersion(ctx context.Context, tx pgx.Tx, profile Profile, settings Settings,
	source VersionSource, actor *ActorID, configurationVersion int32) (ProfileVersion, error) {
	id, err := newUUID()
	if err != nil {
		return ProfileVersion{}, err
	}
	versionID := ProfileVersionID(id)
	if err := insertVersion(ctx, tx, profile.ID, versionID, profile.CurrentVersion.VersionNumber+1,
		configurationVersion, settings, source, actor); err != nil {
		return ProfileVersion{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE model_profiles SET current_version_id=$2, version=version+1,
		updated_at=clock_timestamp() WHERE id=$1
	`, uuid(profile.ID), uuid(versionID)); err != nil {
		return ProfileVersion{}, err
	}
	return scanVersion(tx.QueryRow(ctx, "SELECT "+versionColumns+" FROM model_profile_versions WHERE id=$1", uuid(versionID)))
}

func uniqueConflict(err error) error {
	var state interface{ SQLState() string }
	if errors.As(err, &state) && state.SQLState() == "23505" {
		return fmt.Errorf("%w: provider resource already exists", ErrConflict)
	}
	return err
}

func (store *Store) recordEndpoint(ctx context.Context, tx pgx.Tx, value Endpoint, actor *ActorID, action, eventType string) error {
	return store.record(ctx, tx, actor, action, eventType, "provider_endpoint", [16]byte(value.ID), endpointSnapshot(value))
}

func (store *Store) recordProfile(ctx context.Context, tx pgx.Tx, value Profile, actor *ActorID, action, eventType string) error {
	return store.record(ctx, tx, actor, action, eventType, "model_profile", [16]byte(value.ID), profileSnapshot(value))
}

func (store *Store) record(ctx context.Context, tx pgx.Tx, actor *ActorID, action, eventType, resourceType string, resourceID [16]byte, snapshot map[string]any) error {
	return recordProviderChange(ctx, tx, actor, action, eventType, resourceType, resourceID, snapshot)
}

func recordProviderChange(ctx context.Context, tx pgx.Tx, actor *ActorID, action, eventType, resourceType string, resourceID [16]byte, snapshot map[string]any) error {
	requestID, err := newUUID()
	if err != nil {
		return err
	}
	actorType := "system"
	var actorID *[16]byte
	if actor != nil {
		actorType = "operator"
		id := [16]byte(*actor)
		actorID = &id
	}
	if err := events.AppendAudit(ctx, tx, events.AuditEvent{
		ActorType: actorType, ActorID: actorID, Action: action, TargetType: resourceType,
		TargetID: &resourceID, RequestID: requestID, Details: snapshot,
	}); err != nil {
		return err
	}
	return events.Append(ctx, tx, events.ResourceEvent{
		Type: eventType, ResourceType: resourceType, ResourceID: resourceID, Snapshot: snapshot,
	})
}

func endpointSnapshot(value Endpoint) map[string]any {
	headerNames := make([]string, 0, len(value.Configuration.Headers))
	for name := range value.Configuration.Headers {
		headerNames = append(headerNames, name)
	}
	sort.Slice(headerNames, func(i, j int) bool { return strings.ToLower(headerNames[i]) < strings.ToLower(headerNames[j]) })
	var credentialID any
	if value.Configuration.CredentialID != nil {
		credentialID = value.Configuration.CredentialID.String()
	}
	return map[string]any{
		"id": value.ID.String(), "display_name": value.Configuration.DisplayName,
		"display_key": value.Configuration.DisplayKey, "base_url": value.Configuration.BaseURL,
		"credential_id": credentialID, "header_names": headerNames,
		"chat_completions_path": value.Configuration.ChatCompletionsPath,
		"responses_path":        value.Configuration.ResponsesPath, "models_path": value.Configuration.ModelsPath,
		"allow_http": value.Configuration.AllowHTTP, "allow_private_network": value.Configuration.AllowPrivateNetwork,
		"lifecycle": strings.ToLower(string(value.Lifecycle)), "health": strings.ToLower(string(value.Health)),
		"health_checked_at": optionalTime(value.HealthCheckedAt), "version": value.Version,
		"configuration_version": value.ConfigurationVersion, "created_at": pythonTime(value.CreatedAt),
		"updated_at": pythonTime(value.UpdatedAt), "archived_at": optionalTime(value.ArchivedAt),
	}
}

func profileSnapshot(value Profile) map[string]any {
	return map[string]any{
		"id": value.ID.String(), "endpoint_id": value.EndpointID.String(), "model_id": value.ModelID,
		"availability": strings.ToLower(string(value.Availability)), "version": value.Version,
		"current_version_id":     value.CurrentVersion.ID.String(),
		"current_version_number": value.CurrentVersion.VersionNumber,
		"configuration_version":  value.CurrentVersion.ConfigurationVersion,
		"settings":               settingsPayload(value.CurrentVersion.Settings),
		"created_at":             pythonTime(value.CreatedAt), "updated_at": pythonTime(value.UpdatedAt),
	}
}
