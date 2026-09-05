# Metrics and retention

The API exposes Prometheus 0.0.4 text at `GET /metrics`. The Compose deployment
binds the API to `127.0.0.1` by default and requires the bearer token in
`METRICS_BEARER_TOKEN`. Keep the endpoint on a private monitoring network even
when the API listener is published.

Verify the exporter from the deployment host:

```sh
curl --fail --silent --show-error \
  --header "Authorization: Bearer ${METRICS_BEARER_TOKEN:?}" \
  http://127.0.0.1:8000/metrics
```

The API caches the database aggregation for 15 seconds and coalesces concurrent
scrapes. Authentication runs before collection, so rejected requests do not
use a database connection.

The exporter uses fixed status, phase, role, and health labels. It does not
export resource IDs, source URLs, prompts, error messages, credentials, or user
content. It covers:

- queue depth and retained lease recoveries;
- source sync duration and failures;
- documentation phase duration, page outcomes, and retries;
- model calls, retained tokens, and bounded error class for planner and writer;
- current retained-window Agent-run outcome, model-call, token, bounded-error,
  and latency count/sum gauges (`ref0_agent_retained_*`), plus API request
  duration;
- published wiki age and source revision lag;
- Discord connection state, reconnect transitions, gateway latency,
  permission failures, and binding health.

Job-log retention bounds metrics derived from job attempts and events. Those
metrics describe the retained window rather than lifetime totals. Model token
metrics use durable provider-reported input, output, and total counters; the
exporter does not infer tokens from text or logs. Agent gauges can decrease when
retention removes old receipts and must not be interpreted as lifetime
counters. They are grouped in bounded SQL aggregation, so a scrape does not
read or decode every retained run receipt in the API process.

## Configure retention

Set retention values in `.env` before starting the worker. All durations are
positive days and apply only after the item reaches the corresponding terminal
or expired state.

| Variable | Default | Eligible data |
| --- | ---: | --- |
| `SOURCE_SNAPSHOT_RETENTION_DAYS` | 30 | Snapshot artifacts for non-current revisions with no retained run, evidence, or website page references |
| `FAILED_DRAFT_RETENTION_DAYS` | 14 | Accepted page snapshots from failed or interrupted documentation runs |
| `JOB_LOG_RETENTION_DAYS` | 30 | Attempt and event detail for terminal jobs; the job summary remains |
| `EVENT_LOG_RETENTION_DAYS` | 30 | Old dashboard events, pruned only as a contiguous cursor prefix |
| `AGENT_RUN_RETENTION_DAYS` | 90 | Completed Agent-run receipts and captured knowledge-base scope; no HTTP transcript is stored |
| `DISCORD_CONTEXT_RETENTION_DAYS` | 7 | Discord Agent context after idle expiry or seven days of total lifetime; messages cascade with the context |
| `OLD_WIKI_RETENTION_DAYS` | 90 | Non-current wiki versions with no retained Agent-run scope |
| `RETENTION_BATCH_SIZE` | 100 | Maximum items removed from each category by one job |
| `RETENTION_SCAN_SECONDS` | 3600 | Delay between attempts to enqueue the singleton retention job |

The worker enqueues `APPLY_RETENTION` through the same durable, leased queue as
other background work. The active-operation key prevents concurrent cleanup.
Each deletion is fenced by the worker lease and can be replayed safely. The
worker first commits an `artifact_deletion_intents` row. Failed-draft and source
snapshot files are then removed idempotently before the terminal database state
is recorded. Old-wiki finalization instead holds the wiki database fence while
it revalidates eligibility, removes the artifact, deletes the row, and clears
the intent. This prevents a captured Agent scope from losing its immutable wiki
between capture and receipt settlement. Readers exclude pending artifacts, and
a later worker finishes any intent left by a crash. Current wikis and source
revisions are never retention candidates.

Each pass deletes eligible Agent-run receipts before evaluating old wiki
versions. Captured scope rows cascade with the receipt. Capture creates a
database reservation for every selected wiki before execution begins. The
reservation remains through settlement and expires after 24 hours so an
abandoned process cannot retain a wiki forever. The engine enforces a 20-hour
capture-and-execution deadline derived from that lease, leaving four hours for
detached receipt settlement and clock variance; source walks also observe that
deadline between their bounded file operations. Retention clears expired
reservations in bounded batches and shares the wiki lock order used by capture
settlement. A wiki stays protected while a live reservation or retained Agent
run references it; after the last protection is removed, the same fenced pass
may stage that non-current wiki for deletion.
Discord context follows its separate idle and lifetime policy and is not an
HTTP session store.

Retention keeps `jobs` and `audit_events`, and bounds `event_log` with a durable
pruning watermark so a reconnect either resumes exactly or receives an explicit
reset instead of silently skipping a gap. It writes an audit event
for every removed resource plus a count-only summary for the completed pass.
The audit records contain the policy age and resource ID, not deleted content.
Source revision rows also remain as immutable sync history after their snapshot
artifact is removed. The row receives an `artifact_purged_at` tombstone. If a
later sync observes the same native version and fingerprint, it creates a fresh
active revision and artifact rather than reusing the tombstone.

The unreleased application uses one embedded Goose baseline. Running
`ref0 migrate down` removes that baseline and is not a retention rollback. After
a retention pass creates source tombstones or removes artifacts, restore the
coordinated pre-change backup instead.

After changing a policy, restart the worker and inspect the Jobs page for an
`apply_retention` job. Back up PostgreSQL and the application-data volume before
shortening a policy. Retention deletion is not reversible without a coordinated
backup.
