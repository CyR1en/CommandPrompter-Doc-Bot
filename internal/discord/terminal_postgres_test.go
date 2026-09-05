package discord

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/cyr1en/ref0/internal/jobs"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestTerminalCallbackClosesDiscordOperations(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	migrateDiscordTestDatabase(t, ctx, databaseURL)
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	tests := []struct {
		name                  string
		action                string
		status                jobs.Status
		binding               bool
		wantConnectionFailure bool
		wantMessage           string
	}{
		{"validation failure", "validate", jobs.Failed, false, true, "Discord operation failed."},
		{"refresh cancellation", "refresh", jobs.Cancelled, false, true, "Discord operation was cancelled."},
		{"test message failure", "test_message", jobs.Failed, true, true, "Discord operation failed."},
		{"registration cancellation", "register_command", jobs.Cancelled, true, false, "Discord slash-command registration was cancelled."},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err = pool.Exec(ctx, `TRUNCATE credentials,knowledge_bases,discord_connections,jobs,event_log,audit_events RESTART IDENTITY CASCADE`); err != nil {
				t.Fatal(err)
			}
			credentialID := terminalDiscordUUID(t, "11111111-1111-4111-8111-111111111111")
			connectionID := ConnectionID(terminalDiscordUUID(t, "22222222-2222-4222-8222-222222222222"))
			bindingID := BindingID(terminalDiscordUUID(t, "33333333-3333-4333-8333-333333333333"))
			kbID := terminalDiscordUUID(t, "44444444-4444-4444-8444-444444444444")
			jobID := terminalDiscordUUID(t, "55555555-5555-4555-8555-555555555555")
			now := time.Now().UTC().Truncate(time.Microsecond)
			if _, err = pool.Exec(ctx, `
				INSERT INTO credentials(id,kind,label,masked_value,key_id,nonce,ciphertext,secret_version)
				VALUES($1,'DISCORD_BOT_TOKEN','Discord','********','test',$2,$3,1)
			`, pgDiscordUUID(credentialID), make([]byte, 12), []byte("ciphertext")); err != nil {
				t.Fatal(err)
			}
			if _, err = pool.Exec(ctx, `
				INSERT INTO discord_connections(
					id,display_name,display_key,credential_id,credential_version,
					lifecycle,state,version,created_at,updated_at
				) VALUES($1,'Discord','discord',$2,1,'ENABLED','READY',1,$3,$3)
			`, pgDiscordUUID([16]byte(connectionID)), pgDiscordUUID(credentialID), now); err != nil {
				t.Fatal(err)
			}
			payload := map[string]any{
				"action": test.action, "connection_id": connectionID.String(),
				"connection_version": 1, "credential_id": jobs.UUID(credentialID).String(),
				"credential_version": 1,
			}
			targetType := "discord_connection"
			targetID := jobs.UUID(connectionID)
			if test.binding {
				if _, err = pool.Exec(ctx, `INSERT INTO knowledge_bases(id,name,name_key) VALUES($1,'Discord KB','discord kb')`, pgDiscordUUID(kbID)); err != nil {
					t.Fatal(err)
				}
				agentTx, beginErr := pool.Begin(ctx)
				if beginErr != nil {
					t.Fatal(beginErr)
				}
				agentID := seedDiscordAgent(t, ctx, agentTx, kbID)
				if err = agentTx.Commit(ctx); err != nil {
					t.Fatal(err)
				}
				if _, err = pool.Exec(ctx, `
					INSERT INTO discord_servers(connection_id,server_id,name,refreshed_at)
					VALUES($1,'100','Server',$2)
				`, pgDiscordUUID([16]byte(connectionID)), now); err != nil {
					t.Fatal(err)
				}
				if _, err = pool.Exec(ctx, `
					INSERT INTO discord_channels(
						connection_id,server_id,channel_id,name,channel_type,position,
						effective_bot_permissions,everyone_can_view,refreshed_at
					) VALUES($1,'100','200','docs',0,0,0,true,$2)
				`, pgDiscordUUID([16]byte(connectionID)), now); err != nil {
					t.Fatal(err)
				}
				if _, err = pool.Exec(ctx, `
					INSERT INTO channel_bindings(
						id,connection_id,server_id,listen_channel_id,agent_id,
						reply_policy,enabled,health,validated_at,version,
						created_at,updated_at
					) VALUES($1,$2,'100','200',$3,'SAME_CHANNEL',true,'HEALTHY',$4,1,$4,$4)
				`, pgDiscordUUID([16]byte(bindingID)), pgDiscordUUID([16]byte(connectionID)),
					pgDiscordUUID([16]byte(agentID)), now); err != nil {
					t.Fatal(err)
				}
				if _, err = pool.Exec(ctx, `
					INSERT INTO channel_binding_triggers(
						binding_id,connection_id,server_id,listen_channel_id,enabled,trigger_type
					) VALUES($1,$2,'100','200',true,'SLASH_COMMAND')
				`, pgDiscordUUID([16]byte(bindingID)), pgDiscordUUID([16]byte(connectionID))); err != nil {
					t.Fatal(err)
				}
				payload["binding_id"] = bindingID.String()
				payload["binding_version"] = 1
				targetType, targetID = "discord_binding", jobs.UUID(bindingID)
			}
			encoded, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = pool.Exec(ctx, `
				INSERT INTO jobs(
					id,job_type,target_type,target_id,payload,operation_key,status,
					attempt_count,max_attempts,created_at,updated_at,finished_at
				) VALUES($1,'REFRESH_DISCORD',$2,$3,$4::jsonb,$5,$6,1,3,$7,$7,$7)
			`, pgDiscordUUID(jobID), targetType, pgDiscordUUID([16]byte(targetID)), string(encoded),
				"terminal-discord:"+test.action, string(test.status), now); err != nil {
				t.Fatal(err)
			}
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			callbackErr := TerminalCallback(ctx, tx, jobs.Snapshot{
				ID: jobs.JobID(jobID), Type: jobs.RefreshDiscord, TargetType: targetType,
				TargetID: targetID, Status: test.status, UpdatedAt: now, FinishedAt: &now,
			})
			if callbackErr != nil {
				_ = tx.Rollback(ctx)
				t.Fatal(callbackErr)
			}
			if err = tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			var connectionState string
			var connectionError *string
			if err = pool.QueryRow(ctx, `SELECT state,sanitized_error FROM discord_connections WHERE id=$1`, pgDiscordUUID([16]byte(connectionID))).Scan(&connectionState, &connectionError); err != nil {
				t.Fatal(err)
			}
			if test.wantConnectionFailure {
				if ConnectionState(connectionState) != StateDegraded || connectionError == nil || *connectionError != test.wantMessage {
					t.Fatalf("connection state=%s error=%v", connectionState, connectionError)
				}
			} else if ConnectionState(connectionState) != StateReady || connectionError != nil {
				t.Fatalf("connection changed: state=%s error=%v", connectionState, connectionError)
			}
			if test.binding {
				var enabled bool
				var health, sanitized string
				if err = pool.QueryRow(ctx, `SELECT enabled,health,sanitized_error FROM channel_bindings WHERE id=$1`, pgDiscordUUID([16]byte(bindingID))).Scan(&enabled, &health, &sanitized); err != nil {
					t.Fatal(err)
				}
				if enabled || BindingHealth(health) != BindingUnhealthy || sanitized != test.wantMessage {
					t.Fatalf("binding enabled=%v health=%s error=%q", enabled, health, sanitized)
				}
			}
			suffix := "failed"
			if test.status == jobs.Cancelled {
				suffix = "cancelled"
			}
			var connectionEvents, bindingEvents int
			if err = pool.QueryRow(ctx, `
				SELECT
					count(*) FILTER (WHERE event_type=$1 AND resource_id=$2 AND snapshot->>'id'=$3),
					count(*) FILTER (WHERE event_type=$4 AND resource_id=$5 AND snapshot->>'id'=$6)
				FROM event_log
			`,
				"discord.connection.job_"+suffix, pgDiscordUUID([16]byte(connectionID)), connectionID.String(),
				"discord.binding.job_"+suffix, pgDiscordUUID([16]byte(bindingID)), bindingID.String(),
			).Scan(&connectionEvents, &bindingEvents); err != nil {
				t.Fatal(err)
			}
			wantConnectionEvents, wantBindingEvents := 0, 0
			if test.wantConnectionFailure {
				wantConnectionEvents = 1
			}
			if test.binding {
				wantBindingEvents = 1
			}
			if connectionEvents != wantConnectionEvents || bindingEvents != wantBindingEvents {
				t.Fatalf("terminal events connection=%d/%d binding=%d/%d",
					connectionEvents, wantConnectionEvents, bindingEvents, wantBindingEvents)
			}
		})
	}
}

func terminalDiscordUUID(t *testing.T, raw string) [16]byte {
	t.Helper()
	id, err := jobs.ParseUUID(raw)
	if err != nil {
		t.Fatal(err)
	}
	return [16]byte(id)
}
