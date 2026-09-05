package discord

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/cyr1en/ref0/internal/jobs"
	"github.com/jackc/pgx/v5"
)

// TerminalCallback records a terminal Discord control-plane operation on the
// captured connection or binding. Version checks prevent an obsolete job from
// degrading a resource that an operator has since changed.
func TerminalCallback(ctx context.Context, tx pgx.Tx, job jobs.Snapshot) error {
	if job.Type != jobs.RefreshDiscord {
		return nil
	}
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT payload FROM jobs WHERE id=$1`, pgDiscordUUID([16]byte(job.ID))).Scan(&raw); err != nil {
		return err
	}
	payload := map[string]any{}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return errors.New("Discord job capture is invalid")
	}
	action, ok := payload["action"].(string)
	if !ok || !validDiscordAction(action) {
		return errors.New("Discord job action is invalid")
	}
	connectionText, ok := payload["connection_id"].(string)
	if !ok {
		return errors.New("Discord job capture is invalid")
	}
	connectionID, err := ParseConnectionID(connectionText)
	if err != nil {
		return errors.New("Discord job capture is invalid")
	}
	connectionVersion, versionOK := commandInt32(payload["connection_version"])
	credentialVersion, credentialOK := commandInt32(payload["credential_version"])
	credentialID, credentialIDOK := payload["credential_id"].(string)
	if !versionOK || !credentialOK || !credentialIDOK {
		return errors.New("Discord job capture is invalid")
	}
	connection, err := readConnection(ctx, tx, connectionID, true)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if connection.Version != connectionVersion || connection.CredentialVersion != credentialVersion ||
		connection.CredentialID.String() != credentialID {
		return nil
	}

	var binding *Binding
	if job.TargetType == "discord_binding" {
		bindingText, ok := payload["binding_id"].(string)
		bindingVersion, bindingVersionOK := commandInt32(payload["binding_version"])
		if !ok || !bindingVersionOK {
			return errors.New("Discord binding job capture is invalid")
		}
		bindingID, parseErr := ParseBindingID(bindingText)
		if parseErr != nil || jobs.UUID(bindingID) != job.TargetID {
			return errors.New("Discord binding job target is invalid")
		}
		value, readErr := readBinding(ctx, tx, bindingID, true)
		if errors.Is(readErr, ErrNotFound) {
			return nil
		}
		if readErr != nil {
			return readErr
		}
		if value.ConnectionID != connectionID || value.Version != bindingVersion {
			return nil
		}
		binding = &value
	} else if job.TargetType != "discord_connection" || jobs.UUID(connectionID) != job.TargetID {
		return errors.New("Discord connection job target is invalid")
	}

	message := "Discord operation failed."
	suffix := "failed"
	if job.Status == jobs.Cancelled {
		message = "Discord operation was cancelled."
		suffix = "cancelled"
	}
	if binding == nil || action == "test_message" {
		if connection.State != StateDegraded || connection.SanitizedError == nil {
			if _, err = tx.Exec(ctx, `
				UPDATE discord_connections
				SET state='DEGRADED', sanitized_error=$2, updated_at=$3
				WHERE id=$1
			`, pgDiscordUUID([16]byte(connectionID)), message, discordTerminalTime(job)); err != nil {
				return err
			}
			connection, err = readConnection(ctx, tx, connectionID, false)
			if err != nil {
				return err
			}
			if err = recordConnectionChange(ctx, tx, connection, nil,
				"discord.connection.job_"+suffix, "discord.connection.job_"+suffix); err != nil {
				return err
			}
		}
	}
	if binding == nil || binding.Health == BindingUnhealthy && binding.SanitizedError != nil {
		return nil
	}
	if action == "register_command" {
		message = "Discord slash-command registration failed."
		if job.Status == jobs.Cancelled {
			message = "Discord slash-command registration was cancelled."
		}
	}
	if _, err = tx.Exec(ctx, `
		UPDATE channel_bindings
		SET enabled=false, health='UNHEALTHY', sanitized_error=$2,
		    validated_at=NULL, version=version+1, updated_at=$3
		WHERE id=$1
	`, pgDiscordUUID([16]byte(binding.ID)), message, discordTerminalTime(job)); err != nil {
		return err
	}
	updated, err := readBinding(ctx, tx, binding.ID, false)
	if err != nil {
		return err
	}
	return recordBindingChange(ctx, tx, updated, nil,
		"discord.binding.job_"+suffix, "discord.binding.job_"+suffix)
}

func discordTerminalTime(job jobs.Snapshot) time.Time {
	if job.FinishedAt != nil {
		return *job.FinishedAt
	}
	return job.UpdatedAt
}
