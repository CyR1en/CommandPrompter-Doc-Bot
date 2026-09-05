package discord

import (
	"context"
	"errors"
	"fmt"

	"github.com/cyr1en/ref0/internal/credentials"
	"github.com/cyr1en/ref0/internal/idempotency"
	"github.com/cyr1en/ref0/internal/jobs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type connectionDatabaseRow struct {
	id                pgtype.UUID
	displayName       string
	credentialID      pgtype.UUID
	credentialVersion int32
	applicationID     *string
	botUserID         *string
	botUsername       *string
	avatarHash        *string
	lifecycle         string
	state             string
	gatewayLatencyMS  *int32
	lastHeartbeatAt   pgtype.Timestamptz
	lastEventAt       pgtype.Timestamptz
	sanitizedError    *string
	version           int32
	createdAt         pgtype.Timestamptz
	updatedAt         pgtype.Timestamptz
}

func scanConnection(scanner discordRowScanner) (connectionDatabaseRow, error) {
	var row connectionDatabaseRow
	err := scanner.Scan(
		&row.id, &row.displayName, &row.credentialID, &row.credentialVersion,
		&row.applicationID, &row.botUserID, &row.botUsername, &row.avatarHash,
		&row.lifecycle, &row.state, &row.gatewayLatencyMS, &row.lastHeartbeatAt,
		&row.lastEventAt, &row.sanitizedError, &row.version, &row.createdAt,
		&row.updatedAt,
	)
	if err != nil {
		return connectionDatabaseRow{}, err
	}
	if !row.id.Valid || !row.credentialID.Valid || !row.createdAt.Valid || !row.updatedAt.Valid ||
		row.credentialVersion <= 0 || row.version <= 0 ||
		(row.applicationID == nil) != (row.botUserID == nil) {
		return connectionDatabaseRow{}, errors.New("stored Discord connection is invalid")
	}
	return row, nil
}

func (row connectionDatabaseRow) value() (Connection, error) {
	value := Connection{
		ID: ConnectionID(row.id.Bytes), DisplayName: row.displayName,
		CredentialID: credentials.ID(row.credentialID.Bytes), CredentialVersion: row.credentialVersion,
		BotUsername: row.botUsername, AvatarHash: row.avatarHash,
		Lifecycle: ConnectionLifecycle(row.lifecycle), State: ConnectionState(row.state),
		GatewayLatencyMS: row.gatewayLatencyMS, SanitizedError: row.sanitizedError,
		Version: row.version, CreatedAt: row.createdAt.Time, UpdatedAt: row.updatedAt.Time,
	}
	if row.applicationID != nil {
		parsed, err := ParseSnowflake(*row.applicationID)
		if err != nil {
			return Connection{}, err
		}
		value.ApplicationID = &parsed
	}
	if row.botUserID != nil {
		parsed, err := ParseSnowflake(*row.botUserID)
		if err != nil {
			return Connection{}, err
		}
		value.BotUserID = &parsed
	}
	if row.lastHeartbeatAt.Valid {
		value.LastHeartbeatAt = &row.lastHeartbeatAt.Time
	}
	if row.lastEventAt.Valid {
		value.LastEventAt = &row.lastEventAt.Time
	}
	if value.Lifecycle != ConnectionEnabled && value.Lifecycle != ConnectionDisabled {
		return Connection{}, errors.New("stored Discord connection lifecycle is invalid")
	}
	switch value.State {
	case StateDisabled, StateConnecting, StateReady, StateDegraded:
	default:
		return Connection{}, errors.New("stored Discord connection state is invalid")
	}
	return value, nil
}

func readConnection(
	ctx context.Context,
	database discordRowQueryer,
	id ConnectionID,
	lock bool,
) (Connection, error) {
	query := `
		SELECT id, display_name, credential_id, credential_version,
		       application_id, bot_user_id, bot_username, avatar_hash,
		       lifecycle, state, gateway_latency_ms, last_heartbeat_at,
		       last_event_at, sanitized_error, version, created_at, updated_at
		FROM discord_connections WHERE id=$1
	`
	if lock {
		query += " FOR UPDATE"
	}
	row, err := scanConnection(database.QueryRow(ctx, query, pgDiscordUUID([16]byte(id))))
	if errors.Is(err, pgx.ErrNoRows) {
		return Connection{}, ErrNotFound
	}
	if err != nil {
		return Connection{}, err
	}
	return row.value()
}

func (store *Store) ListConnections(ctx context.Context) ([]Connection, error) {
	rows, err := store.pool.Query(ctx, `
		SELECT id, display_name, credential_id, credential_version,
		       application_id, bot_user_id, bot_username, avatar_hash,
		       lifecycle, state, gateway_latency_ms, last_heartbeat_at,
		       last_event_at, sanitized_error, version, created_at, updated_at
		FROM discord_connections ORDER BY created_at, id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []Connection{}
	for rows.Next() {
		row, scanErr := scanConnection(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		value, valueErr := row.value()
		if valueErr != nil {
			return nil, valueErr
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (store *Store) GetConnection(ctx context.Context, id ConnectionID) (Connection, error) {
	return readConnection(ctx, store.pool, id, false)
}

func (store *Store) CreateConnection(
	ctx context.Context,
	command CreateConnection,
	actor ActorID,
	requestKey string,
) (Connection, error) {
	if err := ValidateCreateConnection(command); err != nil {
		return Connection{}, err
	}
	request, err := store.idempotencyRequest(actor, requestKey, "discord.connection.create", map[string]any{
		"credential_id": command.CredentialID.String(), "display_name": command.DisplayName,
	})
	if err != nil {
		return Connection{}, err
	}
	value, err := store.executeConnection(ctx, request, func(ctx context.Context, tx pgx.Tx) (idempotency.Result, error) {
		secretVersion, innerErr := credentialVersion(ctx, tx, [16]byte(command.CredentialID))
		if innerErr != nil {
			return idempotency.Result{}, innerErr
		}
		now, innerErr := discordClock(ctx, tx)
		if innerErr != nil {
			return idempotency.Result{}, innerErr
		}
		rawID, innerErr := newDiscordUUID()
		if innerErr != nil {
			return idempotency.Result{}, innerErr
		}
		id := ConnectionID(rawID)
		if _, innerErr = tx.Exec(ctx, `
			INSERT INTO discord_connections(
				id, display_name, display_key, credential_id, credential_version,
				lifecycle, state, created_at, updated_at
			) VALUES($1,$2,$3,$4,$5,'ENABLED','CONNECTING',$6,$6)
		`, pgDiscordUUID(rawID), command.DisplayName, DisplayKey(command.DisplayName),
			pgDiscordUUID([16]byte(command.CredentialID)), secretVersion, now); innerErr != nil {
			return idempotency.Result{}, innerErr
		}
		created, innerErr := readConnection(ctx, tx, id, false)
		if innerErr != nil {
			return idempotency.Result{}, innerErr
		}
		if innerErr = recordConnectionChange(ctx, tx, created, &actor,
			"discord.connection.create", "discord.connection.created"); innerErr != nil {
			return idempotency.Result{}, innerErr
		}
		return idempotency.Result{Type: "discord_connection", ID: rawID}, nil
	})
	if discordIntegrityConflict(err) {
		return Connection{}, ErrConflict
	}
	return value, err
}

func (store *Store) UpdateConnection(
	ctx context.Context,
	command UpdateConnection,
	actor ActorID,
	requestKey string,
) (Connection, error) {
	if err := ValidateUpdateConnection(command); err != nil {
		return Connection{}, err
	}
	request, err := store.idempotencyRequest(actor, requestKey, "discord.connection.update", map[string]any{
		"display_name": command.DisplayName, "expected_version": command.ExpectedVersion,
		"id": command.ConnectionID.String(), "lifecycle": string(command.Lifecycle),
	})
	if err != nil {
		return Connection{}, err
	}
	value, err := store.executeConnection(ctx, request, func(ctx context.Context, tx pgx.Tx) (idempotency.Result, error) {
		current, innerErr := readConnection(ctx, tx, command.ConnectionID, true)
		if innerErr != nil {
			return idempotency.Result{}, innerErr
		}
		if current.Version != command.ExpectedVersion {
			return idempotency.Result{}, ErrConflict
		}
		now, innerErr := discordClock(ctx, tx)
		if innerErr != nil {
			return idempotency.Result{}, innerErr
		}
		state := current.State
		if command.Lifecycle == ConnectionDisabled {
			state = StateDisabled
		} else if state == StateDisabled {
			state = StateConnecting
		}
		if _, innerErr = tx.Exec(ctx, `
			UPDATE discord_connections
			SET display_name=$2, display_key=$3, lifecycle=$4, state=$5,
			    version=version+1, updated_at=$6
			WHERE id=$1
		`, pgDiscordUUID([16]byte(command.ConnectionID)), command.DisplayName,
			DisplayKey(command.DisplayName), string(command.Lifecycle), string(state), now); innerErr != nil {
			return idempotency.Result{}, innerErr
		}
		updated, innerErr := readConnection(ctx, tx, command.ConnectionID, false)
		if innerErr != nil {
			return idempotency.Result{}, innerErr
		}
		if innerErr = recordConnectionChange(ctx, tx, updated, &actor,
			"discord.connection.update", "discord.connection.updated"); innerErr != nil {
			return idempotency.Result{}, innerErr
		}
		return idempotency.Result{Type: "discord_connection", ID: [16]byte(command.ConnectionID)}, nil
	})
	if discordIntegrityConflict(err) {
		return Connection{}, ErrConflict
	}
	return value, err
}

func (store *Store) RotateConnectionToken(
	ctx context.Context,
	command RotateToken,
	actor ActorID,
	requestKey string,
) (Connection, error) {
	if err := ValidateRotateToken(command); err != nil {
		return Connection{}, err
	}
	request, err := store.idempotencyRequest(actor, requestKey, "discord.connection.rotate", map[string]any{
		"credential_id": command.CredentialID.String(), "expected_version": command.ExpectedVersion,
		"id": command.ConnectionID.String(),
	})
	if err != nil {
		return Connection{}, err
	}
	value, err := store.executeConnection(ctx, request, func(ctx context.Context, tx pgx.Tx) (idempotency.Result, error) {
		current, innerErr := readConnection(ctx, tx, command.ConnectionID, true)
		if innerErr != nil {
			return idempotency.Result{}, innerErr
		}
		if current.Version != command.ExpectedVersion {
			return idempotency.Result{}, ErrConflict
		}
		secretVersion, innerErr := credentialVersion(ctx, tx, [16]byte(command.CredentialID))
		if innerErr != nil {
			return idempotency.Result{}, innerErr
		}
		now, innerErr := discordClock(ctx, tx)
		if innerErr != nil {
			return idempotency.Result{}, innerErr
		}
		if _, innerErr = tx.Exec(ctx, `
			UPDATE discord_connections
			SET credential_id=$2, credential_version=$3,
			    application_id=NULL, bot_user_id=NULL, bot_username=NULL,
			    avatar_hash=NULL, state='CONNECTING', sanitized_error=NULL,
			    version=version+1, updated_at=$4
			WHERE id=$1
		`, pgDiscordUUID([16]byte(command.ConnectionID)),
			pgDiscordUUID([16]byte(command.CredentialID)), secretVersion, now); innerErr != nil {
			return idempotency.Result{}, innerErr
		}
		updated, innerErr := readConnection(ctx, tx, command.ConnectionID, false)
		if innerErr != nil {
			return idempotency.Result{}, innerErr
		}
		if innerErr = recordConnectionChange(ctx, tx, updated, &actor,
			"discord.connection.rotate", "discord.connection.token_rotated"); innerErr != nil {
			return idempotency.Result{}, innerErr
		}
		return idempotency.Result{Type: "discord_connection", ID: [16]byte(command.ConnectionID)}, nil
	})
	if discordIntegrityConflict(err) {
		return Connection{}, ErrConflict
	}
	return value, err
}

func (store *Store) RequestConnectionValidation(
	ctx context.Context, id ConnectionID, expectedVersion int32,
	actor ActorID, requestKey string,
) (jobs.JobID, error) {
	return store.requestConnectionJob(ctx, "validate", id, expectedVersion, actor, requestKey)
}

func (store *Store) RequestConnectionRefresh(
	ctx context.Context, id ConnectionID, expectedVersion int32,
	actor ActorID, requestKey string,
) (jobs.JobID, error) {
	return store.requestConnectionJob(ctx, "refresh", id, expectedVersion, actor, requestKey)
}

func (store *Store) requestConnectionJob(
	ctx context.Context, action string, id ConnectionID, expectedVersion int32,
	actor ActorID, requestKey string,
) (jobs.JobID, error) {
	operation := "discord.connection." + action
	request, err := store.idempotencyRequest(actor, requestKey, operation, map[string]any{
		"expected_version": expectedVersion, "id": id.String(),
	})
	if err != nil {
		return jobs.JobID{}, err
	}
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return jobs.JobID{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	result, err := idempotency.Execute(ctx, tx, request, func(ctx context.Context, tx pgx.Tx) (idempotency.Result, error) {
		current, innerErr := readConnection(ctx, tx, id, true)
		if innerErr != nil {
			return idempotency.Result{}, innerErr
		}
		if expectedVersion <= 0 || current.Version != expectedVersion {
			return idempotency.Result{}, ErrConflict
		}
		payload := map[string]any{
			"action": action, "connection_id": id.String(),
			"connection_version": current.Version,
			"credential_id":      current.CredentialID.String(),
			"credential_version": current.CredentialVersion,
		}
		jobID, innerErr := jobs.NewStore(store.pool, nil).EnqueueTx(ctx, tx, jobs.Command{
			Type: jobs.RefreshDiscord, TargetType: "discord_connection", TargetID: jobs.UUID(id),
			Payload:      payload,
			OperationKey: fmt.Sprintf("discord:%s:%s:%d:%d", action, id.String(), current.Version, current.CredentialVersion),
		})
		if innerErr != nil {
			return idempotency.Result{}, innerErr
		}
		if innerErr = recordDiscordChange(ctx, tx, &actor,
			operation, "discord.connection.job_requested", "discord_connection", [16]byte(id),
			map[string]any{"id": id.String(), "action": action, "job_id": jobID.String()},
		); innerErr != nil {
			return idempotency.Result{}, innerErr
		}
		return idempotency.Result{Type: "job", ID: [16]byte(jobID)}, nil
	})
	if err != nil {
		return jobs.JobID{}, err
	}
	if result.Type != "job" {
		return jobs.JobID{}, idempotency.ErrConflict
	}
	if err = tx.Commit(ctx); err != nil {
		return jobs.JobID{}, err
	}
	return jobs.JobID(result.ID), nil
}

func (store *Store) executeConnection(
	ctx context.Context,
	request idempotency.Request,
	operation idempotency.Operation,
) (Connection, error) {
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return Connection{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	result, err := idempotency.Execute(ctx, tx, request, operation)
	if err != nil {
		return Connection{}, err
	}
	if result.Type != "discord_connection" {
		return Connection{}, idempotency.ErrConflict
	}
	value, err := readConnection(ctx, tx, ConnectionID(result.ID), false)
	if err != nil {
		return Connection{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Connection{}, err
	}
	return value, nil
}

func recordConnectionChange(
	ctx context.Context,
	tx pgx.Tx,
	value Connection,
	actor *ActorID,
	action, eventType string,
) error {
	return recordDiscordChange(ctx, tx, actor, action, eventType,
		"discord_connection", [16]byte(value.ID), connectionEventSnapshot(value))
}

func connectionEventSnapshot(value Connection) map[string]any {
	return map[string]any{
		"id": value.ID.String(), "display_name": value.DisplayName,
		"credential_id": value.CredentialID.String(), "credential_version": value.CredentialVersion,
		"application_id": snowflakeValue(value.ApplicationID), "bot_user_id": snowflakeValue(value.BotUserID),
		"lifecycle": string(value.Lifecycle), "state": string(value.State),
		"version": value.Version, "sanitized_error": value.SanitizedError,
	}
}

func snowflakeValue(value *Snowflake) any {
	if value == nil {
		return nil
	}
	return string(*value)
}
