package providers

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/cyr1en/ref0/internal/jobs"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestTerminalCallbackClosesProviderCaptures(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	migrateProviderTestDatabase(t, ctx, databaseURL)
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	tests := []struct {
		name      string
		jobType   jobs.Type
		jobStatus jobs.Status
		wantError string
	}{
		{"discovery failure", jobs.DiscoverEndpoint, jobs.Failed, "provider_discovery:job_failed"},
		{"probe cancellation", jobs.ProbeModel, jobs.Cancelled, "provider_probe:cancelled"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err = pool.Exec(ctx, `TRUNCATE operators,provider_endpoints,jobs,event_log,audit_events RESTART IDENTITY CASCADE`); err != nil {
				t.Fatal(err)
			}
			operatorID := terminalProviderID(t, "11111111-1111-4111-8111-111111111111")
			endpointID := terminalProviderID(t, "22222222-2222-4222-8222-222222222222")
			jobID := terminalProviderID(t, "33333333-3333-4333-8333-333333333333")
			runID := terminalProviderID(t, "44444444-4444-4444-8444-444444444444")
			profileID := terminalProviderID(t, "55555555-5555-4555-8555-555555555555")
			versionID := terminalProviderID(t, "66666666-6666-4666-8666-666666666666")
			now := time.Now().UTC().Truncate(time.Microsecond)
			fixture, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer fixture.Rollback(ctx)
			statements := []struct {
				query string
				args  []any
			}{
				{`INSERT INTO operators(id,username,username_key,password_hash) VALUES($1,'Provider operator','provider operator','unused')`, []any{uuid(operatorID)}},
				{`INSERT INTO provider_endpoints(id,display_name,display_key,base_url,headers,lifecycle,health,version,configuration_version) VALUES($1,'Provider','provider','https://provider.example/v1','{}'::jsonb,'ACTIVE','UNKNOWN',1,1)`, []any{uuid(endpointID)}},
				{`INSERT INTO jobs(id,job_type,target_type,target_id,payload,operation_key,status,attempt_count,max_attempts,created_at,updated_at,finished_at) VALUES($1,$2,$3,$4,'{}'::jsonb,$5,$6,1,3,$7,$7,$7)`, []any{
					uuid(jobID), string(test.jobType), terminalProviderTarget(test.jobType), uuid(terminalProviderTargetID(test.jobType, endpointID, profileID)), "terminal-provider:" + runID.String(), string(test.jobStatus), now,
				}},
			}
			for _, statement := range statements {
				if _, err = fixture.Exec(ctx, statement.query, statement.args...); err != nil {
					_ = fixture.Rollback(ctx)
					t.Fatal(err)
				}
			}
			if test.jobType == jobs.DiscoverEndpoint {
				_, err = fixture.Exec(ctx, `
					INSERT INTO discovery_runs(
						id,endpoint_id,job_id,captured_configuration_version,tls_required,
						requested_by_operator_id,status,model_ids,created_at
					) VALUES($1,$2,$3,1,true,$4,'PENDING','[]'::jsonb,$5)
				`, uuid(runID), uuid(endpointID), uuid(jobID), uuid(operatorID), now)
			} else {
				if _, err = fixture.Exec(ctx, `
					INSERT INTO model_profiles(id,endpoint_id,model_id,availability,current_version_id,version)
					VALUES($1,$2,'model','MANUAL',$3,1)
				`, uuid(profileID), uuid(endpointID), uuid(versionID)); err == nil {
					_, err = fixture.Exec(ctx, `
						INSERT INTO model_profile_versions(
							id,profile_id,version_number,configuration_version,transport,
							reasoning_transport,timeout_seconds,max_retries,extra_body,
							metadata_origin,source
						) VALUES($1,$2,1,1,'CHAT_COMPLETIONS','NONE',30,0,
						         '{}'::jsonb,'{}'::jsonb,'OPERATOR')
					`, uuid(versionID), uuid(profileID))
				}
				if err == nil {
					_, err = fixture.Exec(ctx, `
						INSERT INTO probe_runs(
							id,model_profile_id,job_id,captured_configuration_version,
							captured_profile_version_id,requested_by_operator_id,
							selected_checks,acknowledge_cost,status,created_at
						) VALUES($1,$2,$3,1,$4,$5,'["CHAT"]'::jsonb,true,'PENDING',$6)
					`, uuid(runID), uuid(profileID), uuid(jobID), uuid(versionID), uuid(operatorID), now)
				}
			}
			if err != nil {
				_ = fixture.Rollback(ctx)
				t.Fatal(err)
			}
			if err = fixture.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			targetID := terminalProviderTargetID(test.jobType, endpointID, profileID)
			callbackErr := TerminalCallback(ctx, tx, jobs.Snapshot{
				ID: jobs.JobID(jobID), Type: test.jobType,
				TargetType: terminalProviderTarget(test.jobType), TargetID: jobs.UUID(targetID),
				Status: test.jobStatus, UpdatedAt: now, FinishedAt: &now,
			})
			if callbackErr != nil {
				_ = tx.Rollback(ctx)
				t.Fatal(callbackErr)
			}
			if err = tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			table := "discovery_runs"
			if test.jobType == jobs.ProbeModel {
				table = "probe_runs"
			}
			var status, sanitized string
			var started, completed *time.Time
			if err = pool.QueryRow(ctx, `SELECT status,sanitized_error,started_at,completed_at FROM `+table+` WHERE id=$1`, uuid(runID)).Scan(&status, &sanitized, &started, &completed); err != nil {
				t.Fatal(err)
			}
			if CaptureStatus(status) != CaptureFailed || sanitized != test.wantError || started == nil || completed == nil {
				t.Fatalf("capture=%s error=%q started=%v completed=%v", status, sanitized, started, completed)
			}
		})
	}
}

func terminalProviderID(t *testing.T, raw string) ID {
	t.Helper()
	id, err := ParseID(raw)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func terminalProviderTarget(jobType jobs.Type) string {
	if jobType == jobs.DiscoverEndpoint {
		return "provider_endpoint"
	}
	return "model_profile"
}

func terminalProviderTargetID(jobType jobs.Type, endpointID, profileID ID) ID {
	if jobType == jobs.DiscoverEndpoint {
		return endpointID
	}
	return profileID
}
