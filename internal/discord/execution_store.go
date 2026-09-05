package discord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"unicode/utf8"

	"github.com/cyr1en/ref0/internal/jobs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func (store *Store) AssertExecution(ctx context.Context, command jobs.Command, permit jobs.Permit) (Connection, *Binding, error) {
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return Connection{}, nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = jobs.NewStore(store.pool, nil).AssertPermit(ctx, tx, permit); err != nil {
		return Connection{}, nil, err
	}
	connection, binding, err := assertExecutionCapture(ctx, tx, command)
	if err != nil {
		return Connection{}, nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Connection{}, nil, err
	}
	return connection, binding, nil
}

func assertExecutionCapture(ctx context.Context, tx pgx.Tx, command jobs.Command) (Connection, *Binding, error) {
	if command.Type != jobs.RefreshDiscord {
		return Connection{}, nil, errors.New("Discord job capture is invalid")
	}
	action, ok := command.Payload["action"].(string)
	if !ok || !validDiscordAction(action) {
		return Connection{}, nil, errors.New("Discord job capture is invalid")
	}
	connectionID, err := commandConnectionID(command)
	if err != nil {
		return Connection{}, nil, err
	}
	connection, err := readConnection(ctx, tx, connectionID, true)
	if err != nil {
		return Connection{}, nil, err
	}
	connectionVersion, versionOK := commandInt32(command.Payload["connection_version"])
	credentialVersion, credentialVersionOK := commandInt32(command.Payload["credential_version"])
	credentialID, credentialOK := command.Payload["credential_id"].(string)
	if !versionOK || !credentialVersionOK || !credentialOK ||
		connection.Version != connectionVersion ||
		connection.CredentialVersion != credentialVersion ||
		connection.CredentialID.String() != credentialID {
		return Connection{}, nil, fmt.Errorf("%w: Discord job capture is stale", ErrConflict)
	}
	var binding *Binding
	if rawBindingID, exists := command.Payload["binding_id"]; exists && rawBindingID != nil {
		if command.TargetType != "discord_binding" || action != "register_command" && action != "test_message" {
			return Connection{}, nil, errors.New("Discord job capture is invalid")
		}
		text, ok := rawBindingID.(string)
		if !ok {
			return Connection{}, nil, errors.New("Discord job capture is invalid")
		}
		parsed, parseErr := ParseBindingID(text)
		if parseErr != nil {
			return Connection{}, nil, errors.New("Discord job capture is invalid")
		}
		value, readErr := readBinding(ctx, tx, parsed, true)
		if readErr != nil {
			return Connection{}, nil, readErr
		}
		bindingVersion, ok := commandInt32(command.Payload["binding_version"])
		if !ok || bindingVersion != value.Version {
			return Connection{}, nil, fmt.Errorf("%w: Discord binding capture is stale", ErrConflict)
		}
		if value.ConnectionID != connection.ID {
			return Connection{}, nil, errors.New("Discord job capture is invalid")
		}
		binding = &value
	} else if command.TargetType != "discord_connection" || action != "validate" && action != "refresh" {
		return Connection{}, nil, errors.New("Discord job capture is invalid")
	}
	expectedTarget := jobs.UUID(connection.ID)
	if binding != nil {
		expectedTarget = jobs.UUID(binding.ID)
	}
	if command.TargetID != expectedTarget {
		return Connection{}, nil, fmt.Errorf("%w: Discord job capture is stale", ErrConflict)
	}
	return connection, binding, nil
}

func (store *Store) CompleteIdentity(ctx context.Context, connectionID ConnectionID, identity Identity, permit jobs.Permit) error {
	if err := validateIdentity(identity); err != nil {
		return err
	}
	err := store.executionTransaction(ctx, permit, executionExpectation{
		connectionID: connectionID,
		actions:      []string{"validate"},
	}, func(ctx context.Context, tx pgx.Tx) error {
		result, err := tx.Exec(ctx, `
			UPDATE discord_connections SET
				application_id=$2, bot_user_id=$3, bot_username=$4, avatar_hash=$5,
				state='CONNECTING', sanitized_error=NULL, updated_at=clock_timestamp()
			WHERE id=$1
		`, pgDiscordUUID([16]byte(connectionID)), string(identity.ApplicationID), string(identity.BotUserID),
			identity.Username, identity.AvatarHash)
		if err != nil {
			return err
		}
		if result.RowsAffected() != 1 {
			return ErrNotFound
		}
		updated, err := readConnection(ctx, tx, connectionID, false)
		if err != nil {
			return err
		}
		return recordConnectionChange(ctx, tx, updated, nil,
			"discord.connection.identity_refresh", "discord.connection.updated")
	})
	if discordIntegrityConflict(err) {
		return ErrConflict
	}
	return err
}

func (store *Store) CompleteRefresh(
	ctx context.Context,
	connectionID ConnectionID,
	identity Identity,
	snapshots []ServerSnapshot,
	permit jobs.Permit,
) error {
	if err := validateIdentity(identity); err != nil {
		return err
	}
	err := store.executionTransaction(ctx, permit, executionExpectation{
		connectionID: connectionID,
		actions:      []string{"refresh"},
	}, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := readConnection(ctx, tx, connectionID, true); err != nil {
			return err
		}
		now, err := discordClock(ctx, tx)
		if err != nil {
			return err
		}
		for _, table := range []string{"discord_channels", "discord_roles", "discord_servers"} {
			if _, err = tx.Exec(ctx, "UPDATE "+table+" SET refreshed_at=$2 WHERE connection_id=$1",
				pgDiscordUUID([16]byte(connectionID)), directoryStaleAt); err != nil {
				return err
			}
		}
		type directoryKey struct{ server, resource Snowflake }
		presentChannels := make(map[directoryKey]struct{})
		presentRoles := make(map[directoryKey]struct{})
		for _, snapshot := range snapshots {
			if _, err = tx.Exec(ctx, `
				INSERT INTO discord_servers(connection_id,server_id,name,icon_hash,owner,refreshed_at)
				VALUES($1,$2,$3,$4,$5,$6)
				ON CONFLICT(connection_id,server_id) DO UPDATE SET
					name=EXCLUDED.name, icon_hash=EXCLUDED.icon_hash,
					owner=EXCLUDED.owner, refreshed_at=EXCLUDED.refreshed_at
			`, pgDiscordUUID([16]byte(connectionID)), string(snapshot.Server.ID), snapshot.Server.Name,
				snapshot.Server.IconHash, snapshot.Server.Owner, now); err != nil {
				return err
			}
			for _, channel := range snapshot.Channels {
				roles, marshalErr := json.Marshal(snowflakeStrings(channel.ViewerRoleIDs))
				if marshalErr != nil {
					return marshalErr
				}
				users, marshalErr := json.Marshal(snowflakeStrings(channel.ViewerUserIDs))
				if marshalErr != nil {
					return marshalErr
				}
				if _, err = tx.Exec(ctx, `
					INSERT INTO discord_channels(
						connection_id,server_id,channel_id,parent_id,name,channel_type,
						position,effective_bot_permissions,everyone_can_view,
						viewer_role_ids,viewer_user_ids,audience_overwrite_sha256,refreshed_at
					) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::jsonb,$11::jsonb,$12,$13)
					ON CONFLICT(connection_id,server_id,channel_id) DO UPDATE SET
						parent_id=EXCLUDED.parent_id, name=EXCLUDED.name,
						channel_type=EXCLUDED.channel_type, position=EXCLUDED.position,
						effective_bot_permissions=EXCLUDED.effective_bot_permissions,
						everyone_can_view=EXCLUDED.everyone_can_view,
						viewer_role_ids=EXCLUDED.viewer_role_ids,
						viewer_user_ids=EXCLUDED.viewer_user_ids,
						audience_overwrite_sha256=EXCLUDED.audience_overwrite_sha256,
						refreshed_at=EXCLUDED.refreshed_at
				`, pgDiscordUUID([16]byte(connectionID)), string(channel.ServerID), string(channel.ID),
					optionalSnowflake(channel.ParentID), channel.Name, channel.ChannelType, channel.Position,
					int64(channel.EffectiveBotPermissions), channel.EveryoneCanView, string(roles), string(users),
					channel.AudienceOverwriteSHA256[:], now); err != nil {
					return err
				}
				presentChannels[directoryKey{channel.ServerID, channel.ID}] = struct{}{}
			}
			for _, role := range snapshot.Roles {
				if _, err = tx.Exec(ctx, `
					INSERT INTO discord_roles(connection_id,server_id,role_id,name,position,refreshed_at)
					VALUES($1,$2,$3,$4,$5,$6)
					ON CONFLICT(connection_id,server_id,role_id) DO UPDATE SET
						name=EXCLUDED.name, position=EXCLUDED.position,
						refreshed_at=EXCLUDED.refreshed_at
				`, pgDiscordUUID([16]byte(connectionID)), string(snapshot.Server.ID), string(role.ID),
					role.Name, role.Position, now); err != nil {
					return err
				}
				presentRoles[directoryKey{snapshot.Server.ID, role.ID}] = struct{}{}
			}
		}
		if _, err = tx.Exec(ctx, `
			UPDATE discord_connections SET
				application_id=$2, bot_user_id=$3, bot_username=$4, avatar_hash=$5,
				sanitized_error=NULL, updated_at=$6
			WHERE id=$1
		`, pgDiscordUUID([16]byte(connectionID)), string(identity.ApplicationID), string(identity.BotUserID),
			identity.Username, identity.AvatarHash, now); err != nil {
			return err
		}
		updatedConnection, err := readConnection(ctx, tx, connectionID, false)
		if err != nil {
			return err
		}
		if err = recordConnectionChange(ctx, tx, updatedConnection, nil,
			"discord.connection.refresh", "discord.connection.updated"); err != nil {
			return err
		}
		channelCount, roleCount := 0, 0
		for _, snapshot := range snapshots {
			channelCount += len(snapshot.Channels)
			roleCount += len(snapshot.Roles)
		}
		if err = recordDiscordChange(ctx, tx, nil,
			"discord.directory.refresh", "discord.directory.refreshed",
			"discord_connection", [16]byte(connectionID), map[string]any{
				"id": connectionID.String(), "server_count": len(snapshots),
				"channel_count": channelCount, "role_count": roleCount,
			}); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, bindingSelect+`
			WHERE cb.connection_id=$1 AND cb.deleted_at IS NULL
			FOR UPDATE OF cb
		`, pgDiscordUUID([16]byte(connectionID)))
		if err != nil {
			return err
		}
		bindings := []Binding{}
		for rows.Next() {
			row, scanErr := scanBinding(rows)
			if scanErr != nil {
				rows.Close()
				return scanErr
			}
			binding, valueErr := row.value()
			if valueErr != nil {
				rows.Close()
				return valueErr
			}
			bindings = append(bindings, binding)
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return err
		}
		for _, binding := range bindings {
			replyID := binding.ListenChannelID
			if binding.ReplyChannelID != nil {
				replyID = *binding.ReplyChannelID
			}
			_, listenPresent := presentChannels[directoryKey{binding.ServerID, binding.ListenChannelID}]
			_, replyPresent := presentChannels[directoryKey{binding.ServerID, replyID}]
			rolesPresent := true
			for _, roleID := range binding.AllowedRoleIDs {
				if _, exists := presentRoles[directoryKey{binding.ServerID, roleID}]; !exists {
					rolesPresent = false
					break
				}
			}
			policyErr := error(nil)
			if listenPresent && replyPresent && rolesPresent {
				policyErr = validateBindingPolicyTx(ctx, tx, binding)
			} else {
				policyErr = ErrConflict
			}
			if policyErr != nil {
				if _, err = tx.Exec(ctx, `
					UPDATE channel_bindings SET
						enabled=false, health='UNHEALTHY',
						sanitized_error='Discord binding permissions or audience changed.',
						validated_at=NULL, version=version+1, updated_at=$2
					WHERE id=$1
				`, pgDiscordUUID([16]byte(binding.ID)), now); err != nil {
					return err
				}
				updated, readErr := readBinding(ctx, tx, binding.ID, false)
				if readErr != nil {
					return readErr
				}
				if err = recordBindingChange(ctx, tx, updated, nil,
					"discord.binding.refresh_health", "discord.binding.unhealthy"); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if discordIntegrityConflict(err) {
		return ErrConflict
	}
	return err
}

func (store *Store) FailExecution(
	ctx context.Context,
	connectionID ConnectionID,
	sanitizedError string,
	permit jobs.Permit,
	bindingID *BindingID,
) error {
	safe := truncateRunes(sanitizedError, 1_000)
	return store.executionTransaction(ctx, permit, executionExpectation{
		connectionID: connectionID,
		bindingID:    bindingID,
		actions:      []string{"validate", "refresh", "register_command", "test_message"},
	}, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			UPDATE discord_connections SET state='DEGRADED', sanitized_error=$2,
			updated_at=clock_timestamp() WHERE id=$1
		`, pgDiscordUUID([16]byte(connectionID)), safe); err != nil {
			return err
		}
		connection, err := readConnection(ctx, tx, connectionID, false)
		if err != nil {
			return err
		}
		if err = recordConnectionChange(ctx, tx, connection, nil,
			"discord.connection.execution_fail", "discord.connection.state_changed"); err != nil {
			return err
		}
		if bindingID != nil {
			if _, err := tx.Exec(ctx, `
				UPDATE channel_bindings SET enabled=false, health='UNHEALTHY',
				sanitized_error=$2, validated_at=NULL, version=version+1,
				updated_at=clock_timestamp() WHERE id=$1
			`, pgDiscordUUID([16]byte(*bindingID)), safe); err != nil {
				return err
			}
			binding, readErr := readBinding(ctx, tx, *bindingID, false)
			if readErr != nil {
				return readErr
			}
			if err = recordBindingChange(ctx, tx, binding, nil,
				"discord.binding.execution_fail", "discord.binding.unhealthy"); err != nil {
				return err
			}
		}
		return nil
	})
}

func (store *Store) FailCommandRegistration(
	ctx context.Context,
	connectionID ConnectionID,
	serverID Snowflake,
	permit jobs.Permit,
	bindingID *BindingID,
	captures []BindingCapture,
) error {
	if len(captures) == 0 || len(captures) > 10_000 {
		return ErrConflict
	}
	ids := make([]pgtype.UUID, len(captures))
	versions := make([]int32, len(captures))
	unique := make(map[BindingID]struct{}, len(captures))
	for index, capture := range captures {
		if capture.ID == (BindingID{}) || capture.Version <= 0 {
			return ErrConflict
		}
		if _, exists := unique[capture.ID]; exists {
			return ErrConflict
		}
		unique[capture.ID] = struct{}{}
		ids[index] = pgDiscordUUID([16]byte(capture.ID))
		versions[index] = capture.Version
	}
	if bindingID != nil && (len(captures) != 1 || captures[0].ID != *bindingID) {
		return ErrConflict
	}
	return store.executionTransaction(ctx, permit, executionExpectation{
		connectionID: connectionID,
		bindingID:    bindingID,
		actions:      []string{"refresh", "register_command"},
	}, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			WITH captured(id,version) AS (
				SELECT * FROM unnest($3::uuid[],$4::integer[])
			)
			UPDATE channel_bindings AS binding SET enabled=false, health='UNHEALTHY',
				sanitized_error='Discord slash-command registration failed.',
				validated_at=NULL, version=binding.version+1, updated_at=clock_timestamp()
			FROM captured
			WHERE binding.id=captured.id AND binding.version=captured.version
			  AND binding.connection_id=$1 AND binding.server_id=$2
			  AND binding.enabled=true AND binding.deleted_at IS NULL
			  AND EXISTS (
				SELECT 1 FROM channel_binding_triggers AS trigger
				WHERE trigger.binding_id=binding.id
				  AND trigger.trigger_type='SLASH_COMMAND'
			  )
			RETURNING binding.id
		`, pgDiscordUUID([16]byte(connectionID)), string(serverID), ids, versions)
		if err != nil {
			return err
		}
		ids := []BindingID{}
		for rows.Next() {
			var scannedUUID pgtype.UUID
			if err = rows.Scan(&scannedUUID); err != nil || !scannedUUID.Valid {
				rows.Close()
				if err == nil {
					err = errors.New("stored Discord binding ID is invalid")
				}
				return err
			}
			ids = append(ids, BindingID(scannedUUID.Bytes))
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return err
		}
		for _, id := range ids {
			binding, readErr := readBinding(ctx, tx, id, false)
			if readErr != nil {
				return readErr
			}
			if err = recordBindingChange(ctx, tx, binding, nil,
				"discord.binding.registration_fail", "discord.binding.unhealthy"); err != nil {
				return err
			}
		}
		return nil
	})
}

func (store *Store) executionTransaction(
	ctx context.Context,
	permit jobs.Permit,
	expected executionExpectation,
	operation func(context.Context, pgx.Tx) error,
) error {
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err = jobs.NewStore(store.pool, nil).AssertPermit(ctx, tx, permit); err != nil {
		return err
	}
	command, err := readExecutionCommand(ctx, tx, permit.JobID)
	if err != nil {
		return err
	}
	connection, binding, err := assertExecutionCapture(ctx, tx, command)
	if err != nil {
		return err
	}
	if connection.ID != expected.connectionID || !sameBindingCapture(binding, expected.bindingID) ||
		!containsDiscordAction(expected.actions, command.Payload["action"].(string)) {
		return fmt.Errorf("%w: Discord job capture is stale", ErrConflict)
	}
	if err = operation(ctx, tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type executionExpectation struct {
	connectionID ConnectionID
	bindingID    *BindingID
	actions      []string
}

func readExecutionCommand(ctx context.Context, tx pgx.Tx, jobID jobs.JobID) (jobs.Command, error) {
	var jobType, targetType string
	var targetID pgtype.UUID
	var rawPayload []byte
	if err := tx.QueryRow(ctx, `
		SELECT job_type,target_type,target_id,payload
		FROM jobs
		WHERE id=$1
	`, pgDiscordUUID([16]byte(jobID))).Scan(&jobType, &targetType, &targetID, &rawPayload); err != nil {
		return jobs.Command{}, err
	}
	if !targetID.Valid {
		return jobs.Command{}, errors.New("Discord job capture is invalid")
	}
	payload := map[string]any{}
	if err := json.Unmarshal(rawPayload, &payload); err != nil {
		return jobs.Command{}, errors.New("Discord job capture is invalid")
	}
	return jobs.Command{
		Type:       jobs.Type(jobType),
		TargetType: targetType,
		TargetID:   jobs.UUID(targetID.Bytes),
		Payload:    payload,
	}, nil
}

func sameBindingCapture(binding *Binding, expected *BindingID) bool {
	if binding == nil || expected == nil {
		return binding == nil && expected == nil
	}
	return binding.ID == *expected
}

func containsDiscordAction(actions []string, wanted string) bool {
	for _, action := range actions {
		if action == wanted {
			return true
		}
	}
	return false
}

func validateIdentity(identity Identity) error {
	if _, err := ParseSnowflake(string(identity.ApplicationID)); err != nil {
		return errors.New("Discord identity is invalid")
	}
	if _, err := ParseSnowflake(string(identity.BotUserID)); err != nil {
		return errors.New("Discord identity is invalid")
	}
	if identity.Username == "" || !utf8.ValidString(identity.Username) || utf8.RuneCountInString(identity.Username) > 255 {
		return errors.New("Discord identity is invalid")
	}
	if identity.AvatarHash != nil && (!utf8.ValidString(*identity.AvatarHash) || utf8.RuneCountInString(*identity.AvatarHash) > 255) {
		return errors.New("Discord identity is invalid")
	}
	return nil
}

func commandConnectionID(command jobs.Command) (ConnectionID, error) {
	text, ok := command.Payload["connection_id"].(string)
	if !ok {
		return ConnectionID{}, errors.New("Discord job capture is invalid")
	}
	id, err := ParseConnectionID(text)
	if err != nil {
		return ConnectionID{}, errors.New("Discord job capture is invalid")
	}
	return id, nil
}

func commandInt32(value any) (int32, bool) {
	switch typed := value.(type) {
	case float64:
		if typed < math.MinInt32 || typed > math.MaxInt32 || typed != math.Trunc(typed) {
			return 0, false
		}
		return int32(typed), true
	case int:
		if int64(typed) < math.MinInt32 || int64(typed) > math.MaxInt32 {
			return 0, false
		}
		return int32(typed), true
	case int32:
		return typed, true
	case int64:
		if typed < math.MinInt32 || typed > math.MaxInt32 {
			return 0, false
		}
		return int32(typed), true
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil || parsed < math.MinInt32 || parsed > math.MaxInt32 {
			return 0, false
		}
		return int32(parsed), true
	default:
		return 0, false
	}
}

func truncateRunes(value string, maximum int) string {
	if utf8.RuneCountInString(value) <= maximum {
		return value
	}
	return string([]rune(value)[:maximum])
}
