package providers

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/cyr1en/ref0/internal/idempotency"
	"github.com/cyr1en/ref0/internal/jobs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func (store *Store) GetDiscoveryRun(ctx context.Context, id DiscoveryRunID) (DiscoveryRun, error) {
	value, err := scanDiscovery(store.pool.QueryRow(ctx,
		"SELECT "+discoveryColumns+" FROM discovery_runs WHERE id=$1", uuid(id)))
	if errors.Is(err, pgx.ErrNoRows) {
		return DiscoveryRun{}, fmt.Errorf("%w: discovery run not found", ErrNotFound)
	}
	return value, err
}

func (store *Store) GetProbeRun(ctx context.Context, id ProbeRunID) (ProbeRun, error) {
	value, err := scanProbe(store.pool.QueryRow(ctx,
		"SELECT "+probeColumns+" FROM probe_runs WHERE id=$1", uuid(id)))
	if errors.Is(err, pgx.ErrNoRows) {
		return ProbeRun{}, fmt.Errorf("%w: probe run not found", ErrNotFound)
	}
	return value, err
}

func (store *Store) ScheduleDiscovery(ctx context.Context, command ScheduleDiscovery, actor ActorID, requestKey string) (DiscoveryRun, error) {
	if err := command.validate(); err != nil {
		return DiscoveryRun{}, err
	}
	request, err := store.request(actor, requestKey, "discovery.schedule", map[string]any{
		"endpoint_id": command.EndpointID.String(), "expected_version": command.ExpectedVersion,
	})
	if err != nil {
		return DiscoveryRun{}, err
	}
	return withTx(ctx, store.pool, func(tx pgx.Tx) (DiscoveryRun, error) {
		result, err := idempotency.Execute(ctx, tx, request, func(ctx context.Context, tx pgx.Tx) (idempotency.Result, error) {
			endpoint, err := scanEndpoint(tx.QueryRow(ctx, endpointQuery(true), uuid(command.EndpointID)))
			if errors.Is(err, pgx.ErrNoRows) {
				return idempotency.Result{}, fmt.Errorf("%w: provider endpoint not found", ErrNotFound)
			}
			if err != nil {
				return idempotency.Result{}, err
			}
			if endpoint.Version != command.ExpectedVersion {
				return idempotency.Result{}, fmt.Errorf("%w: provider resource version is stale", ErrConflict)
			}
			if endpoint.Lifecycle != Active {
				return idempotency.Result{}, fmt.Errorf("%w: provider endpoint is archived", ErrConflict)
			}
			credentialVersion, err := store.credentialVersion(ctx, tx, endpoint.Configuration.CredentialID)
			if err != nil {
				return idempotency.Result{}, err
			}
			runUUID, err := newUUID()
			if err != nil {
				return idempotency.Result{}, err
			}
			runID := DiscoveryRunID(runUUID)
			jobID, err := store.jobs.EnqueueTx(ctx, tx, jobs.Command{
				Type: jobs.DiscoverEndpoint, TargetType: "provider_endpoint",
				TargetID:     jobs.UUID(command.EndpointID),
				Payload:      map[string]any{"discovery_run_id": runID.String()},
				OperationKey: "discover-endpoint:" + command.EndpointID.String() + ":" + runID.String(),
				MaxAttempts:  3,
			})
			if err != nil {
				return idempotency.Result{}, err
			}
			_, err = tx.Exec(ctx, `
				INSERT INTO discovery_runs (
					id, endpoint_id, job_id, captured_configuration_version,
					captured_credential_version, tls_required, requested_by_operator_id,
					status, model_ids, created_at
				) VALUES ($1,$2,$3,$4,$5,$6,$7,'PENDING','[]'::jsonb,clock_timestamp())
			`, uuid(runID), uuid(command.EndpointID), uuid(jobID), endpoint.ConfigurationVersion,
				credentialVersion, strings.HasPrefix(strings.ToLower(endpoint.Configuration.BaseURL), "https://"), uuid(actor))
			if err != nil {
				return idempotency.Result{}, err
			}
			value, err := scanDiscovery(tx.QueryRow(ctx,
				"SELECT "+discoveryColumns+" FROM discovery_runs WHERE id=$1", uuid(runID)))
			if err != nil {
				return idempotency.Result{}, err
			}
			if err := store.recordRun(ctx, tx, value, nil, &actor, "discovery.scheduled"); err != nil {
				return idempotency.Result{}, err
			}
			return idempotency.Result{Type: "discovery_run", ID: runUUID}, nil
		})
		if err != nil {
			return DiscoveryRun{}, err
		}
		if result.Type != "discovery_run" {
			return DiscoveryRun{}, idempotency.ErrConflict
		}
		return discoveryRunTx(ctx, tx, DiscoveryRunID(result.ID), false)
	})
}

func (store *Store) BeginDiscovery(ctx context.Context, runID DiscoveryRunID, permit jobs.Permit) (DiscoveryRun, error) {
	return withTx(ctx, store.pool, func(tx pgx.Tx) (DiscoveryRun, error) {
		if err := store.jobs.AssertPermit(ctx, tx, permit); err != nil {
			return DiscoveryRun{}, err
		}
		value, err := discoveryRunTx(ctx, tx, runID, true)
		if err != nil {
			return DiscoveryRun{}, err
		}
		if err := assertCapturePermit(ctx, tx, permit, value.JobID, jobs.DiscoverEndpoint,
			"provider_endpoint", jobs.UUID(value.EndpointID)); err != nil {
			return DiscoveryRun{}, err
		}
		if value.Status != CapturePending && value.Status != CaptureFailed {
			return value, nil
		}
		_, err = tx.Exec(ctx, `
			UPDATE discovery_runs SET status='RUNNING', model_ids='[]'::jsonb,
				raw_response=NULL, tls_verified=NULL, authentication_succeeded=NULL,
				http_status=NULL, response_sha256=NULL, model_count=NULL,
				sanitized_error=NULL, started_at=clock_timestamp(), completed_at=NULL
			WHERE id=$1
		`, uuid(runID))
		if err != nil {
			return DiscoveryRun{}, err
		}
		value, err = discoveryRunTx(ctx, tx, runID, false)
		if err != nil {
			return DiscoveryRun{}, err
		}
		if err := store.recordRun(ctx, tx, value, nil, nil, "discovery.running"); err != nil {
			return DiscoveryRun{}, err
		}
		return value, nil
	})
}

func (store *Store) CompleteDiscovery(ctx context.Context, command CompleteDiscovery, permit jobs.Permit) (DiscoveryRun, error) {
	if err := command.validate(); err != nil {
		return DiscoveryRun{}, err
	}
	if command.RawResponse != nil {
		value, err := normalizeJSONObject(command.RawResponse, maxDiscoveryCapture)
		if err != nil {
			return DiscoveryRun{}, err
		}
		command.RawResponse = value
	}
	return withTx(ctx, store.pool, func(tx pgx.Tx) (DiscoveryRun, error) {
		if err := store.jobs.AssertPermit(ctx, tx, permit); err != nil {
			return DiscoveryRun{}, err
		}
		run, err := discoveryRunTx(ctx, tx, command.RunID, true)
		if err != nil {
			return DiscoveryRun{}, err
		}
		if err := assertCapturePermit(ctx, tx, permit, run.JobID, jobs.DiscoverEndpoint,
			"provider_endpoint", jobs.UUID(run.EndpointID)); err != nil {
			return DiscoveryRun{}, err
		}
		if run.Status.Terminal() {
			return run, nil
		}
		if run.Status != CaptureRunning {
			return DiscoveryRun{}, fmt.Errorf("%w: discovery run has not started", ErrConflict)
		}
		endpoint, err := scanEndpoint(tx.QueryRow(ctx, endpointQuery(true), uuid(run.EndpointID)))
		if err != nil {
			return DiscoveryRun{}, err
		}
		credentialVersion, err := store.credentialVersion(ctx, tx, endpoint.Configuration.CredentialID)
		if err != nil {
			return DiscoveryRun{}, err
		}
		stale := endpoint.ConfigurationVersion != run.CapturedConfigurationVersion || !equalOptionalInt(credentialVersion, run.CapturedCredentialVersion)
		successful := command.RawResponse != nil
		if successful && run.TLSRequired && (command.TLSVerified == nil || !*command.TLSVerified) {
			return DiscoveryRun{}, fmt.Errorf("%w: HTTPS discovery requires verified TLS", ErrConflict)
		}
		status := CaptureFailed
		if stale {
			status = CaptureSuperseded
		} else if successful {
			status = CaptureSucceeded
		}
		if status == CaptureSucceeded {
			if err := store.mergeDiscovery(ctx, tx, run.EndpointID, endpoint.ConfigurationVersion, command.ModelIDs); err != nil {
				return DiscoveryRun{}, err
			}
		}
		if status != CaptureSuperseded {
			health := Unhealthy
			if status == CaptureSucceeded {
				health = Healthy
			}
			if _, err := tx.Exec(ctx, `
				UPDATE provider_endpoints SET health=$2, health_checked_at=clock_timestamp(),
				updated_at=clock_timestamp() WHERE id=$1
			`, uuid(run.EndpointID), health); err != nil {
				return DiscoveryRun{}, err
			}
			endpoint, err = scanEndpoint(tx.QueryRow(ctx, endpointQuery(false), uuid(run.EndpointID)))
			if err != nil {
				return DiscoveryRun{}, err
			}
			if err := store.recordEndpoint(ctx, tx, endpoint, nil, "provider_endpoint.health_check", "provider_endpoint.health_checked"); err != nil {
				return DiscoveryRun{}, err
			}
		}
		modelIDsJSON, _ := pythonCanonicalJSON(command.ModelIDs)
		var rawJSON any
		var responseDigest any
		var modelCount any
		if successful {
			encoded, err := pythonCanonicalJSON(command.RawResponse)
			if err != nil {
				return DiscoveryRun{}, err
			}
			digest := sha256.Sum256(encoded)
			rawJSON, responseDigest, modelCount = string(encoded), digest[:], len(command.ModelIDs)
		}
		var sanitized any
		if command.SanitizedError != "" {
			sanitized = command.SanitizedError
		}
		_, err = tx.Exec(ctx, `
			UPDATE discovery_runs SET status=$2, model_ids=$3::jsonb,
				raw_response=$4::jsonb, tls_verified=$5,
				authentication_succeeded=$6, http_status=$7, response_sha256=$8,
				model_count=$9, sanitized_error=$10, completed_at=clock_timestamp()
			WHERE id=$1
		`, uuid(command.RunID), status, string(modelIDsJSON), rawJSON, command.TLSVerified,
			command.AuthenticationSucceeded, command.HTTPStatus, responseDigest, modelCount, sanitized)
		if err != nil {
			return DiscoveryRun{}, err
		}
		value, err := discoveryRunTx(ctx, tx, command.RunID, false)
		if err != nil {
			return DiscoveryRun{}, err
		}
		if err := store.recordRun(ctx, tx, value, nil, nil, "discovery."+strings.ToLower(string(status))); err != nil {
			return DiscoveryRun{}, err
		}
		return value, nil
	})
}

func (store *Store) ScheduleProbe(ctx context.Context, command ScheduleProbe, actor ActorID, requestKey string) (ProbeRun, error) {
	if err := command.validate(); err != nil {
		return ProbeRun{}, err
	}
	request, err := store.request(actor, requestKey, "probe.schedule", map[string]any{
		"profile_id": command.ProfileID.String(), "expected_version": command.ExpectedVersion,
		"selected_checks": command.SelectedChecks, "acknowledge_cost": command.AcknowledgeCost,
	})
	if err != nil {
		return ProbeRun{}, err
	}
	return withTx(ctx, store.pool, func(tx pgx.Tx) (ProbeRun, error) {
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
			profile, err := getProfileTx(ctx, tx, command.ProfileID, true)
			if err != nil {
				return idempotency.Result{}, err
			}
			if endpoint.Lifecycle != Active {
				return idempotency.Result{}, fmt.Errorf("%w: provider endpoint is archived", ErrConflict)
			}
			if profile.Version != command.ExpectedVersion {
				return idempotency.Result{}, fmt.Errorf("%w: provider resource version is stale", ErrConflict)
			}
			if profile.Availability == Unavailable {
				return idempotency.Result{}, fmt.Errorf("%w: unavailable model cannot be probed", ErrConflict)
			}
			credentialVersion, err := store.credentialVersion(ctx, tx, endpoint.Configuration.CredentialID)
			if err != nil {
				return idempotency.Result{}, err
			}
			runUUID, err := newUUID()
			if err != nil {
				return idempotency.Result{}, err
			}
			runID := ProbeRunID(runUUID)
			jobID, err := store.jobs.EnqueueTx(ctx, tx, jobs.Command{
				Type: jobs.ProbeModel, TargetType: "model_profile", TargetID: jobs.UUID(command.ProfileID),
				Payload:          map[string]any{"probe_run_id": runID.String()},
				OperationKey:     "probe-model:" + command.ProfileID.String() + ":" + runID.String(),
				MaxAttempts:      1,
				ConcurrencyKey:   "model-profile:" + command.ProfileID.String(),
				ConcurrencyLimit: profile.CurrentVersion.Settings.MaxConcurrentTasks,
			})
			if err != nil {
				return idempotency.Result{}, err
			}
			checks, _ := pythonCanonicalJSON(command.SelectedChecks)
			_, err = tx.Exec(ctx, `
				INSERT INTO probe_runs (
					id, model_profile_id, job_id, captured_configuration_version,
					captured_credential_version, captured_profile_version_id,
					requested_by_operator_id, selected_checks, acknowledge_cost,
					status, created_at
				) VALUES ($1,$2,$3,$4,$5,$6,$7,$8::jsonb,true,'PENDING',clock_timestamp())
			`, uuid(runID), uuid(command.ProfileID), uuid(jobID), endpoint.ConfigurationVersion,
				credentialVersion, uuid(profile.CurrentVersion.ID), uuid(actor), string(checks))
			if err != nil {
				return idempotency.Result{}, err
			}
			value, err := probeRunTx(ctx, tx, runID, false)
			if err != nil {
				return idempotency.Result{}, err
			}
			if err := store.recordRun(ctx, tx, DiscoveryRun{}, &value, &actor, "probe.scheduled"); err != nil {
				return idempotency.Result{}, err
			}
			return idempotency.Result{Type: "probe_run", ID: runUUID}, nil
		})
		if err != nil {
			return ProbeRun{}, err
		}
		if result.Type != "probe_run" {
			return ProbeRun{}, idempotency.ErrConflict
		}
		return probeRunTx(ctx, tx, ProbeRunID(result.ID), false)
	})
}

func (store *Store) BeginProbe(ctx context.Context, runID ProbeRunID, permit jobs.Permit) (ProbeRun, error) {
	return withTx(ctx, store.pool, func(tx pgx.Tx) (ProbeRun, error) {
		if err := store.jobs.AssertPermit(ctx, tx, permit); err != nil {
			return ProbeRun{}, err
		}
		value, err := probeRunTx(ctx, tx, runID, true)
		if err != nil {
			return ProbeRun{}, err
		}
		if err := assertCapturePermit(ctx, tx, permit, value.JobID, jobs.ProbeModel,
			"model_profile", jobs.UUID(value.ProfileID)); err != nil {
			return ProbeRun{}, err
		}
		if value.Status != CapturePending {
			return value, nil
		}
		if _, err := tx.Exec(ctx, `UPDATE probe_runs SET status='RUNNING', started_at=clock_timestamp() WHERE id=$1`, uuid(runID)); err != nil {
			return ProbeRun{}, err
		}
		value, err = probeRunTx(ctx, tx, runID, false)
		if err != nil {
			return ProbeRun{}, err
		}
		if err := store.recordRun(ctx, tx, DiscoveryRun{}, &value, nil, "probe.running"); err != nil {
			return ProbeRun{}, err
		}
		return value, nil
	})
}

func (store *Store) CompleteProbe(ctx context.Context, command CompleteProbe, permit jobs.Permit) (ProbeRun, error) {
	if err := command.validate(); err != nil {
		return ProbeRun{}, err
	}
	if command.RawResponse != nil {
		value, err := normalizeJSONObject(command.RawResponse, maxProbeCapture)
		if err != nil {
			return ProbeRun{}, err
		}
		command.RawResponse = value
	}
	return withTx(ctx, store.pool, func(tx pgx.Tx) (ProbeRun, error) {
		if err := store.jobs.AssertPermit(ctx, tx, permit); err != nil {
			return ProbeRun{}, err
		}
		run, err := probeRunTx(ctx, tx, command.RunID, true)
		if err != nil {
			return ProbeRun{}, err
		}
		if err := assertCapturePermit(ctx, tx, permit, run.JobID, jobs.ProbeModel,
			"model_profile", jobs.UUID(run.ProfileID)); err != nil {
			return ProbeRun{}, err
		}
		if run.Status.Terminal() {
			return run, nil
		}
		if run.Status != CaptureRunning {
			return ProbeRun{}, fmt.Errorf("%w: probe run has not started", ErrConflict)
		}
		if command.Findings != nil && !validateChecksMatch(*command.Findings, run.SelectedChecks) {
			return ProbeRun{}, fmt.Errorf("%w: probe result must match every selected check", ErrConflict)
		}
		hint, err := scanProfileRow(tx.QueryRow(ctx, profileQuery(false), uuid(run.ProfileID)))
		if err != nil {
			return ProbeRun{}, err
		}
		endpoint, err := scanEndpoint(tx.QueryRow(ctx, endpointQuery(true), uuid(hint.EndpointID)))
		if err != nil {
			return ProbeRun{}, err
		}
		profile, err := getProfileTx(ctx, tx, run.ProfileID, true)
		if err != nil {
			return ProbeRun{}, err
		}
		credentialVersion, err := store.credentialVersion(ctx, tx, endpoint.Configuration.CredentialID)
		if err != nil {
			return ProbeRun{}, err
		}
		stale := endpoint.ConfigurationVersion != run.CapturedConfigurationVersion ||
			!equalOptionalInt(credentialVersion, run.CapturedCredentialVersion) ||
			profile.CurrentVersion.ID != run.CapturedProfileVersionID
		successful := command.Findings != nil
		status := CaptureFailed
		if stale {
			status = CaptureSuperseded
		} else if successful {
			status = CaptureSucceeded
		}
		var resultingID *ProfileVersionID
		if status == CaptureSucceeded {
			merged, err := MergeProbeFindings(profile.CurrentVersion.Settings, *command.Findings)
			if err != nil {
				return ProbeRun{}, err
			}
			version, err := appendVersion(ctx, tx, profile, merged, VersionProbe, nil, endpoint.ConfigurationVersion)
			if err != nil {
				return ProbeRun{}, err
			}
			resultingID = &version.ID
			profile, err = getProfileTx(ctx, tx, run.ProfileID, false)
			if err != nil {
				return ProbeRun{}, err
			}
			if err := store.recordProfile(ctx, tx, profile, nil, "model_profile.probe_merge", "model_profile.version_appended"); err != nil {
				return ProbeRun{}, err
			}
		}
		var findingsJSON, rawJSON, sanitized any
		if command.Findings != nil {
			encoded, _ := pythonCanonicalJSON(command.Findings)
			findingsJSON = string(encoded)
		}
		if command.RawResponse != nil {
			encoded, _ := pythonCanonicalJSON(command.RawResponse)
			rawJSON = string(encoded)
		}
		if command.SanitizedError != "" {
			sanitized = command.SanitizedError
		}
		_, err = tx.Exec(ctx, `
			UPDATE probe_runs SET status=$2, findings=$3::jsonb, raw_response=$4::jsonb,
				sanitized_error=$5, resulting_version_id=$6, completed_at=clock_timestamp()
			WHERE id=$1
		`, uuid(command.RunID), status, findingsJSON, rawJSON, sanitized, nullableUUID(resultingID))
		if err != nil {
			return ProbeRun{}, err
		}
		value, err := probeRunTx(ctx, tx, command.RunID, false)
		if err != nil {
			return ProbeRun{}, err
		}
		if err := store.recordRun(ctx, tx, DiscoveryRun{}, &value, nil, "probe."+strings.ToLower(string(status))); err != nil {
			return ProbeRun{}, err
		}
		return value, nil
	})
}

func discoveryRunTx(ctx context.Context, tx pgx.Tx, id DiscoveryRunID, lock bool) (DiscoveryRun, error) {
	query := "SELECT " + discoveryColumns + " FROM discovery_runs WHERE id=$1"
	if lock {
		query += " FOR UPDATE"
	}
	value, err := scanDiscovery(tx.QueryRow(ctx, query, uuid(id)))
	if errors.Is(err, pgx.ErrNoRows) {
		return DiscoveryRun{}, fmt.Errorf("%w: discovery run not found", ErrNotFound)
	}
	return value, err
}

func probeRunTx(ctx context.Context, tx pgx.Tx, id ProbeRunID, lock bool) (ProbeRun, error) {
	query := "SELECT " + probeColumns + " FROM probe_runs WHERE id=$1"
	if lock {
		query += " FOR UPDATE"
	}
	value, err := scanProbe(tx.QueryRow(ctx, query, uuid(id)))
	if errors.Is(err, pgx.ErrNoRows) {
		return ProbeRun{}, fmt.Errorf("%w: probe run not found", ErrNotFound)
	}
	return value, err
}

func assertCapturePermit(ctx context.Context, tx pgx.Tx, permit jobs.Permit, expectedJob jobs.JobID,
	jobType jobs.Type, targetType string, targetID jobs.UUID) error {
	if permit.JobID != expectedJob {
		return fmt.Errorf("%w: work permit does not belong to capture", ErrConflict)
	}
	var storedType, storedTargetType string
	var storedTargetID pgtype.UUID
	err := tx.QueryRow(ctx, `SELECT job_type, target_type, target_id FROM jobs WHERE id=$1`, uuid(permit.JobID)).Scan(
		&storedType, &storedTargetType, &storedTargetID)
	if err != nil {
		return err
	}
	if jobs.Type(storedType) != jobType || storedTargetType != targetType || storedTargetID.Bytes != [16]byte(targetID) {
		return fmt.Errorf("%w: capture job target is invalid", ErrConflict)
	}
	return nil
}

func (store *Store) mergeDiscovery(ctx context.Context, tx pgx.Tx, endpointID EndpointID,
	configurationVersion int32, discoveredIDs []string) error {
	rows, err := tx.Query(ctx, `
		SELECT id, endpoint_id, model_id, availability, current_version_id,
		       version, created_at, updated_at
		FROM model_profiles WHERE endpoint_id=$1 ORDER BY id FOR UPDATE
	`, uuid(endpointID))
	if err != nil {
		return err
	}
	existing := map[string]profileRow{}
	for rows.Next() {
		row, err := scanProfileRow(rows)
		if err != nil {
			rows.Close()
			return err
		}
		existing[row.ModelID] = row
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	discovered := make(map[string]struct{}, len(discoveredIDs))
	for _, id := range discoveredIDs {
		discovered[id] = struct{}{}
	}
	for modelID, row := range existing {
		if row.Availability == Manual {
			continue
		}
		target := Unavailable
		if _, exists := discovered[modelID]; exists {
			target = Available
		}
		if target == row.Availability {
			continue
		}
		if _, err := tx.Exec(ctx, `
			UPDATE model_profiles SET availability=$2, version=version+1,
			updated_at=clock_timestamp() WHERE id=$1
		`, uuid(row.ID), target); err != nil {
			return err
		}
		value, err := getProfileTx(ctx, tx, row.ID, false)
		if err != nil {
			return err
		}
		if err := store.recordProfile(ctx, tx, value, nil, "model_profile.discovery_availability", "model_profile.updated"); err != nil {
			return err
		}
	}
	newIDs := []string{}
	for modelID := range discovered {
		if _, exists := existing[modelID]; !exists {
			newIDs = append(newIDs, modelID)
		}
	}
	sort.Strings(newIDs)
	for _, modelID := range newIDs {
		profileUUID, err := newUUID()
		if err != nil {
			return err
		}
		versionUUID, err := newUUID()
		if err != nil {
			return err
		}
		profileID, versionID := ProfileID(profileUUID), ProfileVersionID(versionUUID)
		if _, err := tx.Exec(ctx, `
			INSERT INTO model_profiles (id, endpoint_id, model_id, availability,
				current_version_id, version, created_at, updated_at)
			VALUES ($1,$2,$3,'AVAILABLE',$4,1,clock_timestamp(),clock_timestamp())
		`, uuid(profileID), uuid(endpointID), modelID, uuid(versionID)); err != nil {
			return err
		}
		settings := DiscoveredUnknownSettings()
		if err := insertVersion(ctx, tx, profileID, versionID, 1, configurationVersion, settings, VersionDiscovery, nil); err != nil {
			return err
		}
		value, err := getProfileTx(ctx, tx, profileID, false)
		if err != nil {
			return err
		}
		if err := store.recordProfile(ctx, tx, value, nil, "model_profile.discovery_create", "model_profile.created"); err != nil {
			return err
		}
	}
	return nil
}

func equalOptionalInt(left, right *int32) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func (store *Store) recordRun(ctx context.Context, tx pgx.Tx, discovery DiscoveryRun, probe *ProbeRun, actor *ActorID, eventType string) error {
	return recordCaptureRun(ctx, tx, discovery, probe, actor, eventType)
}

func recordCaptureRun(ctx context.Context, tx pgx.Tx, discovery DiscoveryRun, probe *ProbeRun, actor *ActorID, eventType string) error {
	if probe == nil {
		return recordProviderChange(ctx, tx, actor, strings.ReplaceAll(eventType, ".", "_"), eventType,
			"discovery_run", [16]byte(discovery.ID), discoverySnapshot(discovery))
	}
	return recordProviderChange(ctx, tx, actor, strings.ReplaceAll(eventType, ".", "_"), eventType,
		"probe_run", [16]byte(probe.ID), probeSnapshot(*probe))
}

func discoverySnapshot(value DiscoveryRun) map[string]any {
	return map[string]any{
		"id": value.ID.String(), "job_id": value.JobID.String(), "status": strings.ToLower(string(value.Status)),
		"captured_configuration_version": value.CapturedConfigurationVersion,
		"captured_credential_version":    value.CapturedCredentialVersion,
		"created_at":                     pythonTime(value.CreatedAt), "started_at": optionalTime(value.StartedAt),
		"completed_at": optionalTime(value.CompletedAt), "sanitized_error": value.SanitizedError,
		"endpoint_id": value.EndpointID.String(), "model_ids": value.ModelIDs,
		"tls_verified": value.TLSVerified, "authentication_succeeded": value.AuthenticationSucceeded,
		"http_status": value.HTTPStatus, "model_count": value.ModelCount, "tls_required": value.TLSRequired,
	}
}

func probeSnapshot(value ProbeRun) map[string]any {
	checks := make([]string, len(value.SelectedChecks))
	for index, check := range value.SelectedChecks {
		checks[index] = strings.ToLower(string(check))
	}
	var resultingID any
	if value.ResultingVersionID != nil {
		resultingID = value.ResultingVersionID.String()
	}
	return map[string]any{
		"id": value.ID.String(), "job_id": value.JobID.String(), "status": strings.ToLower(string(value.Status)),
		"captured_configuration_version": value.CapturedConfigurationVersion,
		"captured_credential_version":    value.CapturedCredentialVersion,
		"created_at":                     pythonTime(value.CreatedAt), "started_at": optionalTime(value.StartedAt),
		"completed_at": optionalTime(value.CompletedAt), "sanitized_error": value.SanitizedError,
		"model_profile_id":            value.ProfileID.String(),
		"captured_profile_version_id": value.CapturedProfileVersionID.String(),
		"selected_checks":             checks, "resulting_version_id": resultingID,
	}
}
