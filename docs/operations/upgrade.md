# Upgrade

Apply the database schema before starting a new application version. Use the
same immutable image for `migrate`, `api`, `worker`, and `discord`.

## Procedure

1. Read the release notes and confirm its supported PostgreSQL version.
2. Create and restore-test a coordinated backup by following
   [backup and restore](backup-and-restore.md).
3. Pull the target image by immutable digest.
4. Stop `api`, `worker`, and `discord`; leave PostgreSQL healthy.
5. Run migrations once:

   ```sh
   REF0_IMAGE=registry.example/ref0@sha256:... docker compose run --rm migrate up
   ```

6. Start `api` and verify `/health/live`, `/health/ready`, operator login, the
   current wiki, Agent readiness, and scoped `/v1/models` discovery.
7. Start `worker` and `discord`. Verify queue progress, source health, provider
   health, Agent-run history, and connection health in the dashboard.
8. Keep the prior image digest and backup until the observation window ends.

Do not run different application schema versions concurrently unless the
release notes declare that combination compatible. Credential rotation and
master-key rotation are separate resumable jobs. Do not combine either job with
an image upgrade.

## Rollback

The current unreleased schema is one embedded Goose baseline. Running
`ref0 migrate down` removes that baseline and is not a deployment rollback.
Restore the coordinated pre-upgrade database and data-volume backup with its
matching master key. Then restart the prior image digest. Never pair a restored
database with artifacts from a later publication state.
