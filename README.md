# Stalemate

Stalemate is a local engineering memory service for independent coding agents. It captures evidence from Codex sessions and Git, stores searchable memories and versioned assumptions, and identifies downstream work that needs revalidation when a dependency changes.

The service includes a Go CLI and daemon, SQLite storage, local vector search, an explicit dependency graph, OpenAI extraction and conditional reasoning, a dashboard, a JSON API, MCP tools, and optional Codex remediation in isolated Git worktrees. PostgreSQL is also supported through the same persistence layer.

## Start

Requires Go 1.25.3 or newer, Git, and an authenticated Codex CLI for agent execution. Go's automatic toolchain selection downloads the pinned toolchain when necessary. OpenAI is optional for passive capture and static analysis.

```sh
make build
# Put OPENAI_API_KEY in .env, or export it in your shell. Never commit it.
./bin/stalemate init
./bin/stalemate project add --cloud /absolute/path/to/library
./bin/stalemate project add --cloud /absolute/path/to/application
./bin/stalemate serve
```

Open **http://127.0.0.1:7433**. `init` creates an ignored `stalemate.json`, enables OpenAI if the key is present, and selects GPT-6 Luna for extraction, GPT-6.1 Sol for conditional reasoning, and `text-embedding-3-small` with 512 dimensions. API keys remain in the environment. Model IDs, endpoints, prices, dimensions, limits, and secrets' environment variable names are configurable.

Omit `--cloud` to keep a project's processing local. Local capture still stores redacted evidence and performs static dependency analysis; summaries use an explicitly labeled local excerpt and search uses text matching. Re-registering the same path with or without `--cloud` changes its disclosure setting. The daemon reads that setting before each job.

Only registered project paths and their subdirectories are admitted. Stalemate reads session headers to determine project membership; it does not import all your historical conversations or scan arbitrary home directories for code.

## Use with Codex

The most reliable integration is the wrapper. It retrieves current context, injects open alerts, runs the actual CLI, records redacted JSONL evidence, and processes the result even without a daemon:

```sh
./bin/stalemate run --project /absolute/path/to/application -- \
  'Implement the new token integration and run its tests'
```

The wrapper uses `codex exec --json --ephemeral` and a workspace-write sandbox, retaining its own spool instead of duplicating a native Codex transcript. It uses your existing Codex authentication, a bounded subprocess lifetime, and a restricted inherited environment. A successful turn is recorded as a completed **turn**, not proof that the user's entire task was completed.

For normal Codex sessions, the daemon polls `$CODEX_HOME/sessions` and `archived_sessions` (default `~/.codex`), commits byte cursors after complete JSONL records, and resumes incrementally. Completed turns are eligible immediately; incomplete sessions become eligible after ten minutes of inactivity. Unknown record types are ignored, system messages and private reasoning are excluded, and malformed or partial records are deferred rather than silently skipped. Both existing and new sessions in registered projects can be captured.

For a cheap event trigger and MCP setup:

```sh
./bin/stalemate --config /absolute/path/stalemate.json \
  --env-file /absolute/path/.env integrations
```

Follow the printed user-level `notify` and `codex mcp add` instructions. Existing Codex configuration is never overwritten. Preserve any existing notifier by chaining it through your own wrapper. The hook only persists a deduplicated capture job; it performs no network/model calls and works while the daemon is offline. Polling provides recovery when a hook is missing, a terminal is closed, or a process crashes. Hook support varies across Codex surfaces; the wrapper and native transcript polling work independently.

MCP tools:

| Tool | Use |
| --- | --- |
| `context_get(project, task)` | Read active claims, relevant memories, dependencies and open alerts at start/resume |
| `context_record(project, key, decision, evidence, kind?, previous?)` | Add a claim or explicitly supersede its current version |
| `context_dependencies(entity)` | Read typed edges and their provenance |
| `context_impact(changed_entity)` | Read detected downstream impacts |
| `context_acknowledge(impact_id, status?)` | Acknowledge, resolve or dismiss a reviewed alert |
| `context_assume(project, claim_id, evidence)` | Record work's reliance on an active assumption |

An MCP connection exposes tools; an ordinary Codex client must call `context_get` at start/resume. The wrapper injects context automatically. Native polling does not modify existing conversations or their instructions.

Tools carry read/write hints. Noninteractive Codex runs using `approval_policy="never"` may require an explicit per-tool client approval setting for `context_get`; `integrations` prints that setting. The live test enables only that read tool and approves its invocation explicitly. Keep mutation tools under your normal client approval policy.

## Evidence, association and impact

1. **Capture:** Commit redacted transcript batches and their processing cursors atomically. Queue immutable evidence IDs. Independently scan registered Git worktrees, including uncommitted permitted files, so changes made outside an agent session are detected.
2. **Extract:** Parse Go source and dependency manifests locally. A small model extracts bounded JSON summaries, proposed claims and candidate relationships. Nonempty supporting quotes must occur verbatim in the captured evidence. Explicit assertions remain distinct from model inferences.
3. **Associate:** Establish project identity by canonical path and exact module/package identity. Preserve dependency version, binding, consumed APIs, code revision, confidence and provenance. Ambiguous identities are left unmerged. Vector similarity proposes `RELATED_TO` candidates; it never proves `DEPENDS_ON` or triggers graph propagation.
4. **React:** Compare Go exported API signatures and implementation fingerprints, including reachable local helpers. Match changes against consumed APIs, then traverse reverse verified edges with cycle and size bounds. An exact removed API consumed through a local replacement is a confirmed broken reference. Signature/behavior changes, remote version-pinned dependencies, receiver methods inferred through factories and transitive exposure remain possible impact. Unused API changes do not alert unrelated consumers.
5. **Revalidate:** Persist an actionable alert and outbox job in the same transaction. Strong-model analysis is conditional on uncertain impacts or proposed contradictions to assumptions with known dependents. It advises on verification; it cannot silently confirm, dismiss or resolve deterministic alerts.

Claims preserve history. Explicit revisions require the current claim ID in `previous`, supersede that version atomically, and invalidate work relying on the stable assumption entity. Extracted revisions stay proposed, with addition/refinement/contradiction classification; even a model-supported contradiction does not overwrite the active assertion. Relationships retain validity intervals, and acknowledging an alert never erases the original evidence.

See [architecture](docs/architecture.md) for data flow, invariants, extension points and current limits.

## Notifications and remediation

Alerts are durable records, visible in the dashboard, `impacts` CLI, `context_get`, and `GET /api/impacts`. The dashboard polls every five seconds. The daemon also logs new impacts. Optional macOS/Linux desktop notifications and signed JSON webhooks are configurable. Webhook jobs retry with backoff, use the impact ID as an idempotency key, and include an HMAC signature when a secret is configured. Delivery is at least once; consumers must deduplicate. No Slack connection or unsolicited external message is configured. A future Slack bridge can consume the webhook/API.

`automation.mode` defaults to `off`. Set it to `manual` to enable explicit dashboard/CLI repair, or `automatic` to queue repairs for confirmed removed local API references only. `automation.max_runs` and `timeout_seconds` limit execution independently of the API ledger. An automation lease prevents concurrent agents from duplicating the same work.

```sh
./bin/stalemate impacts
./bin/stalemate remediate --id IMPACT_ID
```

Repair requires clean committed consumer work and cloud consent for both the affected project and the evidence's origin. The service creates a new branch/worktree, runs Codex, and runs the configured verification argv. Passing work remains `review_required` in that worktree. Failures are retained for inspection and are not automatically re-executed. `automation.publish_pr: true` separately permits staging permitted files, committing, pushing the new branch, and creating a **draft** PR with `gh`; verification must pass. There is no automatic merge. Configure a meaningful test command for your repositories before enabling repair. The sandbox protects the write boundary; review generated changes and tests before integrating them.

For Go local replacements, a retained verification `go.work` preserves the original dependency bindings after relocation, without modifying `go.mod`. Each run returns `verification_env` with the `GOWORK` assignment needed to repeat its tests. A writable temporary Go cache is supplied to the agent.

## Configuration and cost

`stalemate [--config FILE] [--env-file FILE] COMMAND` accepts global flags **before** the command. `.env` in the working directory is loaded if present; existing environment values win. `--env-file` selects an explicit file for use from any directory. Environment files contain literal assignments, not executable shell code.

Run `init` to obtain the complete documented-by-field JSON configuration. Relevant settings include:

| Setting | Default / purpose |
| --- | --- |
| `data_dir` | `~/.local/share/stalemate`; private SQLite, spools and worktrees |
| `database.driver` | `sqlite` or `postgres` |
| `database.dsn_env` | `STALEMATE_DATABASE_URL`; keeps database credentials outside JSON |
| `http.listen` | `127.0.0.1:7433`; non-loopback requires a token |
| `http.token_env` | `STALEMATE_API_TOKEN`; protects API when set |
| `capture.poll_seconds` / `idle_seconds` | 15 / 600 |
| `capture.exclude` | Environment files, credentials, private keys; plus built-in vendor/generated exclusions |
| `ai.provider` | `disabled` or `openai`, including Responses-compatible endpoints |
| `ai.api_key_env` / `base_url` | Secret reference / configurable endpoint |
| `ai.extraction_model` / `reasoning_model` | GPT-6 Luna / GPT-6.1 Sol |
| `ai.embedding_model` / `dimensions` | `text-embedding-3-small` / 512 |
| `ai.reasoning_effort` | `low` |
| `ai.budget_usd` | $20 cumulative per database; no automatic reset |
| `ai.*usd_per_million` | Explicit cost estimates for the selected models |
| `notifications.webhook_url` / `secret_env` | Optional outbound channel and signing key reference |
| `automation.mode` / `publish_pr` | `off` / `false` |

Each uncached request reserves a conservative input/output cost **before** sending. Actual usage settles the reservation; ambiguous failures retain it across restarts. Strict outputs, output-token caps, bounded input, request caching and incremental capture limit costs. Changing model names requires updating price estimates: the ledger is an estimate using your configured rates, not an account billing API. Codex CLI subscription/API usage is separate from this ledger and is bounded by agent timeouts/run counts. Service data retention and backup are explicit operational choices; no background process deletes your evidence.

OpenAI-compatible endpoints must support Responses, strict JSON schema output, usage reporting and the configured embedding dimensions. A fully local provider can implement `ai.Provider`; Ollama is not required or downloaded. Cloud processing is opt-in per project, HTTPS is required outside loopback, and redirects are rejected for model and webhook requests.

For PostgreSQL:

```sh
export STALEMATE_DATABASE_URL='postgres://USER:PASSWORD@HOST/stalemate?sslmode=require'
# Set database.driver to "postgres" in stalemate.json, then run normally.
```

PostgreSQL provides the same schema, transactions, unique constraints, durable jobs, leases, and budget locking. The daemon remains a local-process service: paths and Codex authentication must be available on its machine. It is not a multi-tenant enterprise control plane. See [operations](docs/operations.md) for backup, failed jobs, service management and migration considerations.

## API

All mutations require JSON. Set `Authorization: Bearer TOKEN` when configured. Cross-origin requests and untrusted loopback Host headers are rejected.

| Method | Endpoint | Purpose |
| --- | --- | --- |
| GET | `/healthz`, `/api/status` | Liveness/database health and model budget |
| GET / POST | `/api/projects` | List/register paths (`path`, `name`, `cloud_allowed`) |
| GET | `/api/context?project=ID&task=TEXT` | Task context |
| GET | `/api/relationships?history=true` | Graph and historical edges |
| GET / POST | `/api/claims` | Read/write versioned assertions |
| POST | `/api/assumptions/depend` | Link work to a claim (`project`, `claim`, `evidence`) |
| GET | `/api/impacts?project=ID&status=open` | Read alerts |
| POST | `/api/impacts/ID` | Set `status`: acknowledged, resolved, dismissed or open |
| GET | `/api/search?q=TEXT&project=ID` | Hybrid similarity/text search; cross-project search stays local |
| GET | `/api/evidence/ID`, `/api/sessions?project=ID` | Inspect provenance |
| GET | `/api/jobs`, `/api/runs` | Inspect processing/remediation |
| POST | `/api/jobs/retry` | Retry a failed job (`id`) |
| POST | `/api/scan` | Queue repository analysis (`{}`) |
| POST | `/api/remediate` | Queue opted-in agent repair (`impact`) |

## Verification

```sh
make check              # formatting, vet, race-enabled unit/integration tests
make live-test          # opt-in: real Codex and OpenAI, $2 API budget
npm ci --ignore-scripts && npm test  # functional dashboard test against its own daemon
```

The live test creates two disposable Git repositories, asks real Codex subprocesses to implement a library and consumer, verifies persisted cloud embeddings, checks replay costs, changes an unused API, removes the consumed API, verifies an actual test failure and alert, revises an assumption, runs stronger reasoning, and repairs the consumer in an isolated worktree. It also tests native transcript capture, CLI notifications when supported, a real Codex MCP call, and the running HTTP daemon. Artifacts remain under ignored `.stalemate/live-*` directories; original projects and global agent configuration are not modified.

CI runs race-enabled tests against SQLite and a disposable PostgreSQL service. Live tests never run automatically in CI. See [validation evidence](docs/validation.md) for the actual execution report and scoped limitations.

Node.js is only needed for the development-time dashboard test; the deployed service is a single Go binary with embedded assets.

Official references: [Codex configuration](https://learn.chatgpt.com/docs/config-file/config-reference), [non-interactive execution](https://learn.chatgpt.com/docs/non-interactive-mode), [structured outputs](https://developers.openai.com/api/docs/guides/structured-outputs), [embeddings](https://developers.openai.com/api/docs/guides/embeddings), and [MCP stdio](https://modelcontextprotocol.io/specification/2025-11-25/basic/transports).
