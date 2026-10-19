# Operations

## Run persistently

Build with `make build`, generate configuration with `init`, and use absolute paths for your executable, configuration and environment file in a service manager:

```sh
/absolute/path/bin/stalemate --config /absolute/path/stalemate.json \
  --env-file /absolute/path/.env serve
```

Use a per-user launchd agent on macOS or a user systemd service on Linux. Run under the same account that owns the repositories and Codex authentication. Direct stderr to your service manager's log. Startup immediately polls eligible transcripts and scans projects; a separate worker processes queued jobs. SIGINT/SIGTERM stops capture, cancels subprocesses/model requests, and drains HTTP shutdown with bounded waits. Do not run as root.

The generated config contains absolute default paths. The CLI's default `.env` lookup is relative to its working directory, so service managers should pass `--env-file`. Existing process environment values override the file. Database credentials should use `database.dsn_env` rather than literal JSON; the same applies to model/API and webhook signing keys.

Relative data, Codex-home and SQLite paths in a configuration file resolve against that file's directory. `~/` is expanded. PostgreSQL requires an explicit DSN or DSN environment variable.

No installer edits `~/.codex/config.toml`, agent instructions or shell startup files. `integrations` prints reviewable settings. Global notify configuration can conflict with an existing notifier: preserve it with an explicit chain. The transcript fallback does not depend on hook delivery. The wrapper works synchronously even when the daemon is stopped.

## Inspect and recover

- `GET /api/status`: settled/reserved API usage, model names and automation mode.
- `GET /api/jobs`: errors, attempts and state; the dashboard exposes the same history.
- `POST /api/jobs/retry {"id":"..."}`: retry a terminal failed job after addressing the cause.
- `worker --max 100`: process ready jobs once; not-yet-available retries remain scheduled.
- `ingest --force`: capture active registered sessions now, without doing model work.
- `scan`: rescan repositories and process ready work synchronously.
- `GET /api/runs`: retained worktree paths, run output and remediation state.

Acknowledgement means a user saw the alert. Resolution means verification or a fix is complete. Dismissal records a reviewed false alarm. None of these statuses delete evidence or change the code. There is no automatic resolution based merely on an agent saying "done."

Malformed source can occur while an agent is editing a file. That project's previous snapshot and dependency edges remain intact; other repositories continue to be analyzed. Repair the file and the next poll will retry. A failed model request leaves raw redacted evidence and jobs available. Disabling cloud processing for a project switches future extractions to local excerpts; previously stored embeddings remain local and usable.

Budget reservations from ambiguous requests remain charged against the local budget after restart. Do not casually release them: inspect actual provider billing before increasing the configured budget. Changing budget/model settings requires restarting the process. Ledger limits are per database, not per provider account. Agent CLI use has separate subscription/billing semantics.

Interrupted remediations retain the worktree and run record. Inspect these before any retry. Stalemate will not automatically launch another agent for the same impact. Use Git's standard worktree review, diff, merge/cherry-pick and removal commands when deciding how to integrate or discard the changes. Run limits count persisted attempts, including failed ones. Automatic PR publishing uses a new branch, configured tests and a draft PR; it never merges.

Local Go replacements are resolved against the original consumer root and preserved in a retained `data_dir/verification/RUN_ID/go.work`. Repeat verification with the returned `verification_env` assignments. Temporary agent build caches are removed after the attempt, while the verification workspace and reviewed source worktree remain. This prevents relocated `../library` bindings from requiring machine-specific `go.mod` edits in the generated patch.

## Backup and retention

SQLite uses WAL and a busy timeout. Stop the daemon and all CLI writers before copying the data directory, or use SQLite's online backup facility. Copying only a live `.sqlite` file without its WAL can omit committed evidence. Keep backups private: artifacts can contain proprietary code even after credential redaction. Data directories and created SQLite files use restrictive permissions.

For PostgreSQL use your normal `pg_dump`/restore and access-control policies. No credentials are returned by the API. Schema creation is transactional and records a migration version; binaries reject databases from newer incompatible schemas. This first release has one schema version; future changes need additive numbered migrations and backup/restore tests.

Evidence, historical claims/edges, usage and job outcomes are retained. No automatic destructive retention policy is enabled. Spools, captured artifacts and remediation worktrees can grow; monitor the data directory. Preserve audit history before applying an organization-specific retention policy.

Switching `database.driver` and DSN selects a different ledger. It does **not** migrate existing data. Export/transform/import tooling and tenant-aware migrations should accompany an enterprise rollout. Existing memory vectors must keep their model/dimension namespace; re-embed into a new namespace when changing embedding models.

## Network boundary

The default HTTP listener is loopback. Set `STALEMATE_API_TOKEN` to require bearer authentication; a token is mandatory on non-loopback addresses. Browser access can enter it in the dashboard's API-token form, which keeps it in that tab's session storage. Use TLS/reverse-proxy controls before any remote exposure. The service rejects browser cross-origin requests, cross-site fetches, untrusted loopback Host headers and non-JSON mutations.

Model and webhook URLs require HTTPS except explicit loopback development servers. Redirects are rejected. Outbound webhook delivery requires consent from both involved projects and carries `Idempotency-Key` plus optional `X-Stalemate-Signature: sha256=...` computed over the exact request bytes. Verify the HMAC with a constant-time comparison and deduplicate by impact ID.

The MCP transport is local stdio, with protocol messages exclusively on stdout and logs on stderr. It shares the local user's trust boundary. A hosted MCP server would require separate authentication/authorization and transport work.
