package discord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/cyr1en/ref0/internal/agents"
	"github.com/cyr1en/ref0/internal/idempotency"
	"github.com/cyr1en/ref0/internal/jobs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

var directoryStaleAt = time.Unix(0, 0).UTC()

type bindingDatabaseRow struct {
	id, connectionID, agentID        pgtype.UUID
	serverID, listenChannelID        string
	triggers                         []string
	replyPolicy                      string
	replyChannelID                   *string
	allowedRoleJSON, allowedUserJSON []byte
	rateRequests, rateWindowSeconds  int32
	enabled                          bool
	health                           string
	sanitizedError                   *string
	version                          int32
	createdAt, updatedAt             pgtype.Timestamptz
}

const bindingSelect = `
	SELECT cb.id, cb.connection_id, cb.server_id, cb.listen_channel_id,
	       cb.agent_id,
	       ARRAY(SELECT trigger_type FROM channel_binding_triggers
	             WHERE binding_id=cb.id ORDER BY trigger_type),
	       cb.reply_policy, cb.reply_channel_id, cb.allowed_role_ids,
	       cb.allowed_user_ids, cb.rate_requests, cb.rate_window_seconds,
	       cb.enabled, cb.health, cb.sanitized_error, cb.version,
	       cb.created_at, cb.updated_at
	FROM channel_bindings AS cb`

func scanBinding(scanner discordRowScanner) (bindingDatabaseRow, error) {
	var row bindingDatabaseRow
	err := scanner.Scan(
		&row.id, &row.connectionID, &row.serverID, &row.listenChannelID,
		&row.agentID, &row.triggers, &row.replyPolicy,
		&row.replyChannelID, &row.allowedRoleJSON, &row.allowedUserJSON,
		&row.rateRequests, &row.rateWindowSeconds, &row.enabled, &row.health,
		&row.sanitizedError, &row.version, &row.createdAt, &row.updatedAt,
	)
	if err != nil {
		return bindingDatabaseRow{}, err
	}
	if !row.id.Valid || !row.connectionID.Valid || !row.agentID.Valid ||
		!row.createdAt.Valid || !row.updatedAt.Valid || row.version <= 0 {
		return bindingDatabaseRow{}, errors.New("stored Discord binding is invalid")
	}
	return row, nil
}

func (row bindingDatabaseRow) value() (Binding, error) {
	serverID, err := ParseSnowflake(row.serverID)
	if err != nil {
		return Binding{}, err
	}
	listenID, err := ParseSnowflake(row.listenChannelID)
	if err != nil {
		return Binding{}, err
	}
	value := Binding{
		ID: BindingID(row.id.Bytes), ConnectionID: ConnectionID(row.connectionID.Bytes),
		ServerID: serverID, ListenChannelID: listenID,
		AgentID: agents.AgentID(row.agentID.Bytes), ReplyPolicy: ReplyPolicy(row.replyPolicy),
		RatePolicy: RatePolicy{Requests: row.rateRequests, WindowSeconds: row.rateWindowSeconds},
		Enabled:    row.enabled, Health: BindingHealth(row.health), SanitizedError: row.sanitizedError,
		Version: row.version, CreatedAt: row.createdAt.Time, UpdatedAt: row.updatedAt.Time,
	}
	value.Triggers = make([]TriggerType, len(row.triggers))
	for index, trigger := range row.triggers {
		value.Triggers[index] = TriggerType(trigger)
	}
	if row.replyChannelID != nil {
		parsed, parseErr := ParseSnowflake(*row.replyChannelID)
		if parseErr != nil {
			return Binding{}, parseErr
		}
		value.ReplyChannelID = &parsed
	}
	if value.AllowedRoleIDs, err = decodeSnowflakes(row.allowedRoleJSON); err != nil {
		return Binding{}, err
	}
	if value.AllowedUserIDs, err = decodeSnowflakes(row.allowedUserJSON); err != nil {
		return Binding{}, err
	}
	if err = ValidateBindingConfiguration(value.configuration()); err != nil {
		return Binding{}, errors.New("stored Discord binding is invalid")
	}
	switch value.Health {
	case BindingDraft, BindingHealthy, BindingUnhealthy:
	default:
		return Binding{}, errors.New("stored Discord binding health is invalid")
	}
	return value, nil
}

func (value Binding) configuration() BindingConfiguration {
	return BindingConfiguration{
		ConnectionID: value.ConnectionID, ServerID: value.ServerID,
		ListenChannelID: value.ListenChannelID, AgentID: value.AgentID,
		Triggers: append([]TriggerType(nil), value.Triggers...), ReplyPolicy: value.ReplyPolicy,
		ReplyChannelID: value.ReplyChannelID,
		AllowedRoleIDs: append([]Snowflake(nil), value.AllowedRoleIDs...),
		AllowedUserIDs: append([]Snowflake(nil), value.AllowedUserIDs...),
		RatePolicy:     value.RatePolicy,
	}
}

func decodeSnowflakes(raw []byte) ([]Snowflake, error) {
	var stringsValue []string
	if err := json.Unmarshal(raw, &stringsValue); err != nil {
		return nil, err
	}
	values := make([]Snowflake, len(stringsValue))
	for index, item := range stringsValue {
		parsed, err := ParseSnowflake(item)
		if err != nil {
			return nil, err
		}
		values[index] = parsed
	}
	return values, nil
}

func readBinding(
	ctx context.Context,
	database discordRowQueryer,
	id BindingID,
	lock bool,
) (Binding, error) {
	query := bindingSelect + ` WHERE cb.id=$1 AND cb.deleted_at IS NULL`
	if lock {
		query += " FOR UPDATE"
	}
	row, err := scanBinding(database.QueryRow(ctx, query, pgDiscordUUID([16]byte(id))))
	if errors.Is(err, pgx.ErrNoRows) {
		return Binding{}, ErrNotFound
	}
	if err != nil {
		return Binding{}, err
	}
	return row.value()
}

func (store *Store) ListBindings(ctx context.Context) ([]Binding, error) {
	rows, err := store.pool.Query(ctx, bindingSelect+`
		WHERE cb.deleted_at IS NULL ORDER BY cb.created_at, cb.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []Binding{}
	for rows.Next() {
		row, scanErr := scanBinding(rows)
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

func (store *Store) GetBinding(ctx context.Context, id BindingID) (Binding, error) {
	return readBinding(ctx, store.pool, id, false)
}

func (store *Store) CreateBinding(
	ctx context.Context,
	command CreateBinding,
	actor ActorID,
	requestKey string,
) (Binding, error) {
	if err := ValidateCreateBinding(command); err != nil {
		return Binding{}, err
	}
	payload := bindingPayload(command.Configuration)
	payload["enabled"] = command.Enabled
	request, err := store.idempotencyRequest(actor, requestKey, "discord.binding.create", payload)
	if err != nil {
		return Binding{}, err
	}
	value, err := store.executeBinding(ctx, request, func(ctx context.Context, tx pgx.Tx) (idempotency.Result, error) {
		if _, innerErr := readConnection(ctx, tx, command.Configuration.ConnectionID, false); innerErr != nil {
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
		id := BindingID(rawID)
		if innerErr = insertBinding(ctx, tx, id, command.Configuration, now); innerErr != nil {
			return idempotency.Result{}, innerErr
		}
		if innerErr = replaceBindingTriggers(ctx, tx, id, command.Configuration, now); innerErr != nil {
			return idempotency.Result{}, innerErr
		}
		if command.Enabled {
			candidate, readErr := readBinding(ctx, tx, id, false)
			if readErr != nil {
				return idempotency.Result{}, readErr
			}
			if innerErr = validateBindingPolicyTx(ctx, tx, candidate); innerErr != nil {
				return idempotency.Result{}, innerErr
			}
			if _, innerErr = tx.Exec(ctx, `
				UPDATE channel_bindings
				SET enabled=true, health='HEALTHY', validated_at=$2
				WHERE id=$1
			`, pgDiscordUUID(rawID), now); innerErr != nil {
				return idempotency.Result{}, innerErr
			}
		}
		created, innerErr := readBinding(ctx, tx, id, false)
		if innerErr != nil {
			return idempotency.Result{}, innerErr
		}
		if created.Enabled && created.HasTrigger(TriggerSlashCommand) {
			if _, innerErr = store.scheduleRegistrationTx(ctx, tx, created); innerErr != nil {
				return idempotency.Result{}, innerErr
			}
		}
		if innerErr = recordBindingChange(ctx, tx, created, &actor,
			"discord.binding.create", "discord.binding.created"); innerErr != nil {
			return idempotency.Result{}, innerErr
		}
		return idempotency.Result{Type: "discord_binding", ID: rawID}, nil
	})
	if discordIntegrityConflict(err) {
		return Binding{}, ErrConflict
	}
	return value, err
}

func (store *Store) UpdateBinding(
	ctx context.Context,
	command UpdateBinding,
	actor ActorID,
	requestKey string,
) (Binding, error) {
	if err := ValidateUpdateBinding(command); err != nil {
		return Binding{}, err
	}
	payload := bindingPayload(command.Configuration)
	payload["id"] = command.BindingID.String()
	payload["expected_version"] = command.ExpectedVersion
	payload["enabled"] = command.Enabled
	request, err := store.idempotencyRequest(actor, requestKey, "discord.binding.update", payload)
	if err != nil {
		return Binding{}, err
	}
	value, err := store.executeBinding(ctx, request, func(ctx context.Context, tx pgx.Tx) (idempotency.Result, error) {
		current, innerErr := readBinding(ctx, tx, command.BindingID, true)
		if innerErr != nil {
			return idempotency.Result{}, innerErr
		}
		if current.Version != command.ExpectedVersion {
			return idempotency.Result{}, ErrConflict
		}
		if command.Enabled {
			candidate := current
			applyBindingConfiguration(&candidate, command.Configuration)
			candidate.Enabled = true
			if innerErr = validateBindingPolicyTx(ctx, tx, candidate); innerErr != nil {
				return idempotency.Result{}, innerErr
			}
		}
		now, innerErr := discordClock(ctx, tx)
		if innerErr != nil {
			return idempotency.Result{}, innerErr
		}
		if innerErr = updateBindingConfiguration(ctx, tx, command, now); innerErr != nil {
			return idempotency.Result{}, innerErr
		}
		if innerErr = replaceBindingTriggers(ctx, tx, command.BindingID, command.Configuration, now); innerErr != nil {
			return idempotency.Result{}, innerErr
		}
		if command.Enabled {
			if _, innerErr = tx.Exec(ctx, `
				UPDATE channel_bindings SET enabled=true, health='HEALTHY', validated_at=$2
				WHERE id=$1
			`, pgDiscordUUID([16]byte(command.BindingID)), now); innerErr != nil {
				return idempotency.Result{}, innerErr
			}
		}
		updated, innerErr := readBinding(ctx, tx, command.BindingID, false)
		if innerErr != nil {
			return idempotency.Result{}, innerErr
		}
		if updated.Enabled && updated.HasTrigger(TriggerSlashCommand) {
			if _, innerErr = store.scheduleRegistrationTx(ctx, tx, updated); innerErr != nil {
				return idempotency.Result{}, innerErr
			}
		}
		if innerErr = recordBindingChange(ctx, tx, updated, &actor,
			"discord.binding.update", "discord.binding.updated"); innerErr != nil {
			return idempotency.Result{}, innerErr
		}
		return idempotency.Result{Type: "discord_binding", ID: [16]byte(command.BindingID)}, nil
	})
	if discordIntegrityConflict(err) {
		return Binding{}, ErrConflict
	}
	return value, err
}

func (store *Store) DeleteBinding(
	ctx context.Context,
	id BindingID,
	expectedVersion int32,
	actor ActorID,
	requestKey string,
) error {
	request, err := store.idempotencyRequest(actor, requestKey, "discord.binding.delete", map[string]any{
		"expected_version": expectedVersion, "id": id.String(),
	})
	if err != nil {
		return err
	}
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	result, err := idempotency.Execute(ctx, tx, request, func(ctx context.Context, tx pgx.Tx) (idempotency.Result, error) {
		current, innerErr := readBinding(ctx, tx, id, true)
		if innerErr != nil {
			return idempotency.Result{}, innerErr
		}
		if expectedVersion <= 0 || current.Version != expectedVersion {
			return idempotency.Result{}, ErrConflict
		}
		now, innerErr := discordClock(ctx, tx)
		if innerErr != nil {
			return idempotency.Result{}, innerErr
		}
		if _, innerErr = tx.Exec(ctx, `
			UPDATE channel_bindings
			SET enabled=false, health='DRAFT', sanitized_error=NULL,
			    validated_at=NULL, deleted_at=$2, version=version+1, updated_at=$2
			WHERE id=$1
		`, pgDiscordUUID([16]byte(id)), now); innerErr != nil {
			return idempotency.Result{}, innerErr
		}
		tombstone := current
		tombstone.Enabled = false
		tombstone.Health = BindingDraft
		tombstone.SanitizedError = nil
		tombstone.Version++
		tombstone.UpdatedAt = now
		if innerErr = recordBindingChange(ctx, tx, tombstone, &actor,
			"discord.binding.delete", "discord.binding.deleted"); innerErr != nil {
			return idempotency.Result{}, innerErr
		}
		return idempotency.Result{Type: "discord_binding", ID: [16]byte(id)}, nil
	})
	if err != nil {
		return err
	}
	if result.Type != "discord_binding" || result.ID != [16]byte(id) {
		return idempotency.ErrConflict
	}
	return tx.Commit(ctx)
}

func (store *Store) ValidateBinding(
	ctx context.Context,
	id BindingID,
	expectedVersion int32,
	actor ActorID,
	requestKey string,
) (Binding, error) {
	request, err := store.idempotencyRequest(actor, requestKey, "discord.binding.validate", map[string]any{
		"expected_version": expectedVersion, "id": id.String(),
	})
	if err != nil {
		return Binding{}, err
	}
	return store.executeBinding(ctx, request, func(ctx context.Context, tx pgx.Tx) (idempotency.Result, error) {
		current, innerErr := readBinding(ctx, tx, id, true)
		if innerErr != nil {
			return idempotency.Result{}, innerErr
		}
		if expectedVersion <= 0 || current.Version != expectedVersion {
			return idempotency.Result{}, ErrConflict
		}
		if innerErr = validateBindingPolicyTx(ctx, tx, current); innerErr != nil {
			return idempotency.Result{}, innerErr
		}
		now, innerErr := discordClock(ctx, tx)
		if innerErr != nil {
			return idempotency.Result{}, innerErr
		}
		if _, innerErr = tx.Exec(ctx, `
			UPDATE channel_bindings
			SET health='HEALTHY', sanitized_error=NULL, validated_at=$2,
			    version=version+1, updated_at=$2
			WHERE id=$1
		`, pgDiscordUUID([16]byte(id)), now); innerErr != nil {
			return idempotency.Result{}, innerErr
		}
		updated, innerErr := readBinding(ctx, tx, id, false)
		if innerErr != nil {
			return idempotency.Result{}, innerErr
		}
		if innerErr = recordBindingChange(ctx, tx, updated, &actor,
			"discord.binding.validate", "discord.binding.validated"); innerErr != nil {
			return idempotency.Result{}, innerErr
		}
		return idempotency.Result{Type: "discord_binding", ID: [16]byte(id)}, nil
	})
}

func (store *Store) RequestTestMessage(
	ctx context.Context,
	id BindingID,
	expectedVersion int32,
	actor ActorID,
	requestKey string,
) (jobs.JobID, error) {
	request, err := store.idempotencyRequest(actor, requestKey, "discord.binding.test", map[string]any{
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
		binding, innerErr := readBinding(ctx, tx, id, true)
		if innerErr != nil {
			return idempotency.Result{}, innerErr
		}
		if expectedVersion <= 0 || binding.Version != expectedVersion {
			return idempotency.Result{}, ErrConflict
		}
		if innerErr = validateBindingPolicyTx(ctx, tx, binding); innerErr != nil {
			return idempotency.Result{}, innerErr
		}
		connection, innerErr := readConnection(ctx, tx, binding.ConnectionID, false)
		if innerErr != nil {
			return idempotency.Result{}, innerErr
		}
		jobID, innerErr := jobs.NewStore(store.pool, nil).EnqueueTx(ctx, tx, jobs.Command{
			Type: jobs.RefreshDiscord, TargetType: "discord_binding", TargetID: jobs.UUID(id),
			Payload:      bindingJobPayload("test_message", binding, connection),
			OperationKey: fmt.Sprintf("discord:test:%s:%d:%d", id.String(), binding.Version, connection.CredentialVersion),
			MaxAttempts:  1,
		})
		if innerErr != nil {
			return idempotency.Result{}, innerErr
		}
		if innerErr = recordDiscordChange(ctx, tx, &actor,
			"discord.binding.test", "discord.binding.test_requested", "discord_binding", [16]byte(id),
			map[string]any{"id": id.String(), "job_id": jobID.String()},
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

func (store *Store) InstallationURL(ctx context.Context, id ConnectionID, threads bool) (string, error) {
	value, err := store.GetConnection(ctx, id)
	if err != nil {
		return "", err
	}
	if value.ApplicationID == nil {
		return "", ErrConflict
	}
	return InstallationURL(*value.ApplicationID, threads)
}

func (store *Store) executeBinding(
	ctx context.Context,
	request idempotency.Request,
	operation idempotency.Operation,
) (Binding, error) {
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return Binding{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	result, err := idempotency.Execute(ctx, tx, request, operation)
	if err != nil {
		return Binding{}, err
	}
	if result.Type != "discord_binding" {
		return Binding{}, idempotency.ErrConflict
	}
	value, err := readBinding(ctx, tx, BindingID(result.ID), false)
	if err != nil {
		return Binding{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Binding{}, err
	}
	return value, nil
}

func insertBinding(ctx context.Context, tx pgx.Tx, id BindingID, config BindingConfiguration, now time.Time) error {
	roles, err := json.Marshal(snowflakeStrings(config.AllowedRoleIDs))
	if err != nil {
		return err
	}
	users, err := json.Marshal(snowflakeStrings(config.AllowedUserIDs))
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO channel_bindings(
			id, connection_id, server_id, listen_channel_id, agent_id,
			reply_policy, reply_channel_id, allowed_role_ids,
			allowed_user_ids, rate_requests, rate_window_seconds, enabled,
			health, created_at, updated_at
		) VALUES($1,$2,$3,$4,$5,$6,$7,$8::jsonb,$9::jsonb,$10,$11,false,'DRAFT',$12,$12)
	`, pgDiscordUUID([16]byte(id)), pgDiscordUUID([16]byte(config.ConnectionID)),
		string(config.ServerID), string(config.ListenChannelID),
		pgDiscordUUID([16]byte(config.AgentID)), string(config.ReplyPolicy),
		optionalSnowflake(config.ReplyChannelID), string(roles), string(users),
		config.RatePolicy.Requests, config.RatePolicy.WindowSeconds, now)
	return err
}

func updateBindingConfiguration(ctx context.Context, tx pgx.Tx, command UpdateBinding, now time.Time) error {
	config := command.Configuration
	roles, err := json.Marshal(snowflakeStrings(config.AllowedRoleIDs))
	if err != nil {
		return err
	}
	users, err := json.Marshal(snowflakeStrings(config.AllowedUserIDs))
	if err != nil {
		return err
	}
	var currentAgent pgtype.UUID
	if err = tx.QueryRow(ctx, `SELECT agent_id FROM channel_bindings WHERE id=$1 FOR UPDATE`,
		pgDiscordUUID([16]byte(command.BindingID))).Scan(&currentAgent); err != nil {
		return err
	}
	if currentAgent.Bytes != [16]byte(config.AgentID) {
		if _, err = tx.Exec(ctx, `DELETE FROM discord_conversations WHERE binding_id=$1`,
			pgDiscordUUID([16]byte(command.BindingID))); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `
		UPDATE channel_bindings SET
			connection_id=$2, server_id=$3, listen_channel_id=$4,
			agent_id=$5, reply_policy=$6,
			reply_channel_id=$7, allowed_role_ids=$8::jsonb,
			allowed_user_ids=$9::jsonb, rate_requests=$10,
			rate_window_seconds=$11, enabled=false, health='DRAFT',
			sanitized_error=NULL, validated_at=NULL,
			version=version+1, updated_at=$12
		WHERE id=$1
	`, pgDiscordUUID([16]byte(command.BindingID)), pgDiscordUUID([16]byte(config.ConnectionID)),
		string(config.ServerID), string(config.ListenChannelID),
		pgDiscordUUID([16]byte(config.AgentID)), string(config.ReplyPolicy),
		optionalSnowflake(config.ReplyChannelID), string(roles), string(users),
		config.RatePolicy.Requests, config.RatePolicy.WindowSeconds, now)
	return err
}

func replaceBindingTriggers(
	ctx context.Context,
	tx pgx.Tx,
	id BindingID,
	config BindingConfiguration,
	now time.Time,
) error {
	if _, err := tx.Exec(ctx, `DELETE FROM channel_binding_triggers WHERE binding_id=$1`,
		pgDiscordUUID([16]byte(id))); err != nil {
		return err
	}
	triggers := append([]TriggerType(nil), config.Triggers...)
	slices.Sort(triggers)
	for _, trigger := range triggers {
		if _, err := tx.Exec(ctx, `
			INSERT INTO channel_binding_triggers(
				binding_id,connection_id,server_id,listen_channel_id,enabled,trigger_type,created_at
			) VALUES($1,$2,$3,$4,false,$5,$6)
		`, pgDiscordUUID([16]byte(id)), pgDiscordUUID([16]byte(config.ConnectionID)),
			string(config.ServerID), string(config.ListenChannelID), string(trigger), now); err != nil {
			return err
		}
	}
	return nil
}

func applyBindingConfiguration(value *Binding, config BindingConfiguration) {
	value.ConnectionID = config.ConnectionID
	value.ServerID = config.ServerID
	value.ListenChannelID = config.ListenChannelID
	value.AgentID = config.AgentID
	value.Triggers = append([]TriggerType(nil), config.Triggers...)
	value.ReplyPolicy = config.ReplyPolicy
	value.ReplyChannelID = config.ReplyChannelID
	value.AllowedRoleIDs = append([]Snowflake(nil), config.AllowedRoleIDs...)
	value.AllowedUserIDs = append([]Snowflake(nil), config.AllowedUserIDs...)
	value.RatePolicy = config.RatePolicy
}

func (store *Store) scheduleRegistrationTx(ctx context.Context, tx pgx.Tx, binding Binding) (jobs.JobID, error) {
	connection, err := readConnection(ctx, tx, binding.ConnectionID, false)
	if err != nil {
		return jobs.JobID{}, err
	}
	return jobs.NewStore(store.pool, nil).EnqueueTx(ctx, tx, jobs.Command{
		Type: jobs.RefreshDiscord, TargetType: "discord_binding", TargetID: jobs.UUID(binding.ID),
		Payload:      bindingJobPayload("register_command", binding, connection),
		OperationKey: fmt.Sprintf("discord:register:%s:%d:%d", binding.ID.String(), binding.Version, connection.CredentialVersion),
		MaxAttempts:  1,
	})
}

func bindingJobPayload(action string, binding Binding, connection Connection) map[string]any {
	return map[string]any{
		"action": action, "binding_id": binding.ID.String(), "binding_version": binding.Version,
		"connection_id": connection.ID.String(), "connection_version": connection.Version,
		"credential_id": connection.CredentialID.String(), "credential_version": connection.CredentialVersion,
	}
}

func bindingPayload(config BindingConfiguration) map[string]any {
	return map[string]any{
		"connection_id": config.ConnectionID.String(), "server_id": string(config.ServerID),
		"listen_channel_id": config.ListenChannelID, "agent_id": config.AgentID.String(),
		"triggers": triggerStrings(config.Triggers), "reply_policy": string(config.ReplyPolicy),
		"reply_channel_id": optionalSnowflake(config.ReplyChannelID),
		"allowed_role_ids": snowflakeStrings(config.AllowedRoleIDs),
		"allowed_user_ids": snowflakeStrings(config.AllowedUserIDs),
		"rate_requests":    config.RatePolicy.Requests, "rate_window_seconds": config.RatePolicy.WindowSeconds,
	}
}

func triggerStrings(values []TriggerType) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = string(value)
	}
	slices.Sort(result)
	return result
}

func optionalSnowflake(value *Snowflake) any {
	if value == nil {
		return nil
	}
	return string(*value)
}

func snowflakeStrings(values []Snowflake) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = string(value)
	}
	return result
}

func recordBindingChange(
	ctx context.Context,
	tx pgx.Tx,
	value Binding,
	actor *ActorID,
	action, eventType string,
) error {
	snapshot := map[string]any{
		"id": value.ID.String(), "connection_id": value.ConnectionID.String(),
		"server_id": string(value.ServerID), "listen_channel_id": string(value.ListenChannelID),
		"agent_id": value.AgentID.String(), "triggers": triggerStrings(value.Triggers),
		"reply_policy":     string(value.ReplyPolicy),
		"reply_channel_id": optionalSnowflake(value.ReplyChannelID),
		"allowed_role_ids": snowflakeStrings(value.AllowedRoleIDs),
		"allowed_user_ids": snowflakeStrings(value.AllowedUserIDs),
		"enabled":          value.Enabled, "health": string(value.Health), "version": value.Version,
		"sanitized_error": value.SanitizedError,
	}
	return recordDiscordChange(ctx, tx, actor, action, eventType,
		"discord_binding", [16]byte(value.ID), snapshot)
}
