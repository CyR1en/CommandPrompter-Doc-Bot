# Backup and restore

A recoverable deployment needs a PostgreSQL dump, the application-data volume,
and the exact master key set used at backup time. Store the key separately from
the other two artifacts.

This procedure supports a database managed by the embedded ref0 Goose baseline.
It does not convert or adopt an Alembic-managed development database from the
retired runtime.

## Create a backup

1. Record the running image digest and migration revision.
2. Pause `api`, `worker`, and `discord` so the database and artifact pointer cannot advance during the snapshot.
3. Dump PostgreSQL in custom format:

   ```sh
   docker compose exec -T postgres sh -c 'pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB" --format=custom' > ref0-db.dump
   ```

   The single quotes are intentional. `POSTGRES_USER` and `POSTGRES_DB` are
   expanded inside the container. Compose interpolation does not export values
   from `.env` into the host shell.

4. Resolve the actual project-scoped application volume from the API container,
   then archive it with numeric ownership and permissions intact:

   ```sh
   api_container_id="$(docker compose ps --all --quiet api)"
   app_data_volume="$(docker inspect --format '{{range .Mounts}}{{if eq .Destination "/app/data"}}{{.Name}}{{end}}{{end}}' "$api_container_id")"
   test -n "$app_data_volume"
   docker run --rm -v "$app_data_volume:/data:ro" -v "$PWD:/backup" alpine:3.22 tar -C /data -czf /backup/ref0-data.tar.gz .
   ```

5. Copy `APP_MASTER_KEY` and any `APP_PREVIOUS_MASTER_KEYS` from the deployment secret manager into the separate recovery record. Do not place keys in the archive.
6. Restart the paused services and verify `/health/ready`.
7. Encrypt, checksum, date, and retention-label both backup artifacts.

Git mirrors can be rebuilt, but published wiki bundles, encrypted configuration,
Agent versions, and retained execution receipts in this backup are not disposable.

## Restore into an empty deployment

1. Stop application processes. Configure the saved database settings and exact
   master key set, then create the deployment containers and their empty volumes
   with `docker compose create`.
2. Resolve the application volume from the created API container and restore the
   archive before starting the worker:

   ```sh
   api_container_id="$(docker compose ps --all --quiet api)"
   app_data_volume="$(docker inspect --format '{{range .Mounts}}{{if eq .Destination "/app/data"}}{{.Name}}{{end}}{{end}}' "$api_container_id")"
   test -n "$app_data_volume"
   docker run --rm -v "$app_data_volume:/data" -v "$PWD:/backup:ro" alpine:3.22 tar -C /data -xzf /backup/ref0-data.tar.gz
   ```
3. Restore the database:

   ```sh
   docker compose up --detach --wait postgres
   docker compose exec -T postgres sh -c 'pg_restore -U "$POSTGRES_USER" -d "$POSTGRES_DB" --clean --if-exists --exit-on-error' < ref0-db.dump
   ```

4. Keep `api`, `worker`, and `discord` stopped while the restore is checked.
5. Use the image digest recorded with the backup and run
   `docker compose run --rm migrate status`. A dump from an older release must
   first be restored with that release. Then follow the
   [upgrade procedure](upgrade.md).
6. Start `api`, then verify readiness and operator login. Confirm a credential
   remains masked, a retained wiki opens, an Agent shows its expected current
   version and knowledge-base membership, and its retained run shows the exact
   captured wiki scope.
7. Start `worker` and `discord`. Confirm jobs resume and enabled Discord connections reconnect.

Restore tests run against two disposable PostgreSQL 18 containers:

```sh
REF0_RUN_DOCKER_TESTS=1 go test ./verification -run TestDatabaseAndArtifactBackupRestore
```

The proof migrates and seeds the source, dumps and archives it, then restores
both halves. It compares the complete seeded Agent root and immutable
configuration, ordered knowledge-base membership, and the complete Agent-run
receipt including captured model, endpoint, credential identity and version,
origin, effective policies, usage, tool audit, citations, and sanitized failure
fields. It also compares every captured scope tuple, including non-empty source
revision IDs and scope digests, plus encrypted configuration, publication
pointers, hashes, and artifact bytes. The credential is created through the
application vault, and the restored key ID, nonce, ciphertext, and secret
version must decrypt to the original plaintext with the saved master key. Both
seeded wikis must restore exact `.page-manifest.json` and `index.md` bytes whose
manifest digests match their publication rows; both captured scope digests are
also compared exactly. A database-only restore is incomplete because artifact
paths can point into `/app/data`. A volume-only restore is incomplete because
publication pointers and encryption metadata live in PostgreSQL.
