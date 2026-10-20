# Operations

## Installed service

The one-command installer needs curl, tar, SHA-256 tooling (`sha256sum` or `shasum`) and standard Unix tools. It installs a prebuilt binary and checks Git; supported package managers install missing Git. No external SQLite/vector database or Go toolchain is needed. `stalemate doctor` distinguishes required Git from optional Codex and `gh`. It does not authenticate those CLIs or install subscriptions.

`setup` creates user settings independently of the working directory. It defaults to local-only processing even when an OpenAI key happens to be in the environment. `setup --openai` explicitly enables the provider and privately stores its key. Existing config is preserved on subsequent installs. `setup --listen 127.0.0.1:7434` changes ports and restarts an owned service. Port conflicts fail without stopping unrelated processes.

`start`, `stop`, `restart`, and `status` manage a detached process on macOS/Linux. A file lock serializes lifecycle changes; a private PID/instance record and authenticated status response identify the owned process before signalling it. Startup waits for readiness. Logs append to `data_dir/logs/service.log`; rotate them according to your retention policy. This process survives terminal closure but does not restart after a reboot or crash. Run `start` after login, use Docker's restart policy, or configure a service manager below. Windows uses foreground `serve`.

Configuration precedence: explicit `--config`, then `STALEMATE_CONFIG`, then working-directory `stalemate.json`, then installed user `config.json`, then built-in defaults. A config loads its adjacent `environment` file when present; otherwise the working-directory `.env` is used for compatibility. `--env-file` always overrides that selection. Existing environment values take precedence. `setup` targets user settings unless `--config`/`STALEMATE_CONFIG` selects another file.

Relative data, Codex-home and SQLite paths resolve against the configuration's directory. `~/` is expanded. Secrets should use configured environment variable names rather than literal JSON. Changing settings requires restarting. Stop the service before changing its data directory or switching databases.

The installer adds `~/.local/bin` (or the chosen install directory) to the current shell's startup profile unless `--no-path` is used. It never edits `~/.codex/config.toml` or agent instructions. `integrations` prints reviewable Codex settings. Preserve an existing notifier by explicitly chaining it. Polling and the wrapper work independently of hook delivery.

For boot-time operation, use a per-user launchd agent on macOS or a user systemd service on Linux with absolute executable/config/environment paths:

```sh
/absolute/path/stalemate --config /absolute/path/config.json \
  --env-file /absolute/path/environment serve
```

Run under the account that owns the repositories and Codex authentication. Let the service manager own `serve`; do not combine that manager with `start`/`stop`. SIGINT/SIGTERM cancels capture, workers, subprocesses and model requests, and drains HTTP shutdown with bounded waits.

To uninstall: `stalemate stop`, remove the installed binary and the line marked `# stalemate installer` in your shell profile. User settings and data are retained; delete those directories separately only when you intend to discard evidence.

## Docker deployment

The image includes Git, CA certificates, SQLite and the dashboard. It initializes the volume root, drops to UID/GID 10001, and executes the Go service directly. `/data` is persistent; do not recursively chown mounted source repositories. Compose publishes only on loopback, requires a generated persistent API token, uses an init process and restarts unless stopped. For remote deployment use TLS and authenticated reverse-proxy controls.

```sh
docker compose up -d
docker compose exec stalemate cat /data/api-token
```

Supply `STALEMATE_API_TOKEN`/`STALEMATE_API_TOKEN_FILE` to choose a token, or use the generated `/data/api-token`. Enter it in the dashboard; do not place it in a URL. Mount custom JSON read-only and set `STALEMATE_CONFIG` to its container path. Docker secret files are supported through `OPENAI_API_KEY_FILE`. An empty token secret is rejected. Compose defaults to disabled AI; set `STALEMATE_AI_PROVIDER=openai` and provide `OPENAI_API_KEY` to opt in, then register individual projects with cloud consent. A plain `docker run -e OPENAI_API_KEY` also enables the provider unless explicitly disabled.

For host capture, bind mount repositories at the **same absolute paths** found in Codex transcripts, and mount sessions read-only. For example, add `/home/alice/code:/home/alice/code:ro` and `/home/alice/.codex/sessions:/capture/sessions:ro`, then set `STALEMATE_CODEX_HOME=/capture`. Optionally mount `archived_sessions` similarly. Prefer mounting only session directories rather than the entire Codex home, which can contain credentials. Files must be readable by UID 10001; on Linux use appropriate group/ACL permissions. Git also checks repository ownership: either run with `--user "$(id -u):$(id -g)"` and a bind-mounted data directory owned by that user, or explicitly configure Git safe-directory entries for the specific trusted registered roots. Do not disable ownership checks globally. Static scanning works with read-only repositories. Agent remediation needs writable worktrees, a separately installed/authenticated Codex CLI, build tools and an appropriate user identity; the default image is a capture/API deployment, with automation off.

Environment overrides: `STALEMATE_DATA_DIR`, `STALEMATE_LISTEN`, `STALEMATE_CODEX_HOME`, `STALEMATE_DATABASE_DRIVER`, `STALEMATE_DATABASE_URL`, `STALEMATE_AI_PROVIDER`, `STALEMATE_AI_BASE_URL`, `STALEMATE_EXTRACTION_MODEL`, `STALEMATE_REASONING_MODEL`, `STALEMATE_EMBEDDING_MODEL`, `STALEMATE_BUDGET_USD`. JSON handles all other settings. To use remote PostgreSQL, set driver `postgres` and supply its DSN via `STALEMATE_DATABASE_URL`; no database server is bundled or provisioned. Switching drivers selects a separate ledger and does not migrate data.

For a local image build: `docker build -t stalemate:local .`, then set `STALEMATE_IMAGE=stalemate:local` for Compose. Published GHCR packages may require a one-time maintainer visibility change to public. Public release assets also contain `stalemate_docker_linux_amd64.tar.gz` and `stalemate_docker_linux_arm64.tar.gz`. Download the archive for your host and `checksums.txt` from the same release, verify its SHA-256, then `docker load -i ARCHIVE`. Set `STALEMATE_IMAGE=ghcr.io/jakecyr/stalemate:vX.Y.Z` to the loaded version and use `docker compose up -d --pull never`. These archives support offline deployments without registry credentials.

## Release automation

A successful push-to-master CI run triggers the serialized Release workflow. Pull requests cannot publish. Binaries are built for macOS/Linux AMD64/ARM64 and Windows AMD64 with embedded version/commit/date and no CGO runtime. Docker images target Linux AMD64/ARM64. Release artifacts include checksums and offline Docker images; a release remains a draft until assets are uploaded. Reruns preserve already published release assets.

Versioning uses conventional commits since the latest published stable release: `fix`, `perf` and `revert` increment patch; `feat` increments minor; `!` or a `BREAKING CHANGE:` footer increments major. Other types produce no release on their own. The largest bump wins; an initial feature release is `v0.1.0`. This repository does not yet have a stable 1.x API. Tags and release notes are generated by CI, with `contents:write` and `packages:write` scoped to publishing jobs. A manual Release run on master can retry a failed publication. Maintainers must make the GHCR package public in its settings after its first publication if GitHub defaults it to private.

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
