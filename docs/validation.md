# Validation evidence

Executed on October 19, 2026 on macOS arm64 using Go 1.25.3 and authenticated `codex-cli 0.163.0`.

## Real cross-project scenario

The final live run passed **14 checks** using actual Codex subprocesses and hosted OpenAI requests. The complete sanitized result is [live-test-report.json](live-test-report.json). Private transcripts, fixture repositories, database and the reviewed repair worktree remain under the ignored `.stalemate/live-20261019T124823Z/` directory on the development machine; they are not published to this repository.

1. Codex implemented `tokenlib.Encode` and tests in one Git project.
2. Codex implemented a separate portal consuming that API through a **relative** local Go replacement, with a real passing consumer test.
3. Static analysis discovered the exact directional dependency and consumed selector; real extraction and 512-dimensional cloud embeddings were persisted in SQLite.
4. Replaying the sessions generated zero additional model calls.
5. Changing an unused exported implementation generated no downstream alert.
6. A third Codex session removed `Encode` and introduced `EncodeV2`. Library tests passed; the original consumer actually failed to compile. Stalemate produced the expected confirmed direct impact.
7. Retrieving consumer task context included that impact.
8. An explicitly versioned compatibility assumption was superseded, creating an additional possible impact and a real GPT-6.1 Sol assessment.
9. Codex repaired the consumer in an isolated worktree. Real consumer tests passed against the changed library using the retained verification workspace. The original consumer and its relative `go.mod` replacement remained unchanged.
10. Draft PR publishing remained disabled for the real external scenario.
11. A native Codex session was consumed incrementally from the actual Codex home transcript.
12. Codex's actual `notify` invocation created a durable capture trigger.
13. A real Codex subprocess connected to the MCP server and called `context_get`, retrieving the open impact ID. Only that read tool was enabled and explicitly approved in the test client's configuration.
14. The actual daemon served its dashboard and API, and HTTP acknowledgement persisted and removed the alert from open work.

That run made **24 direct service API calls**, using 7,698 input and 3,517 output tokens. At the configured rates its ledger recorded **$0.0043613**, with zero outstanding reservations. All development live iterations together recorded **$0.01680112** across 85 calls, including unsuccessful iterations used to exercise failure handling. These are ledger estimates using configured model prices. Codex subprocesses used the existing CLI login; their subscription usage is separate.

The real scenario was deliberately rerun while hardening worktree verification and MCP setup. Verification failures stayed failed and unpublished. The final successful run covers the corrected dependency overlay, writable cache, and explicit read-tool client policy.

## Automated checks

`make check` passes formatting, `go vet` and race-enabled tests. The suites cover:

- Direct/transitive impact recall, suppression of unconsumed API warnings, replay deduplication, private-helper implementation propagation, conservative receiver-method exposure and npm manifest exposure.
- Claim history, optimistic supersession, dependency invalidation, evidence quote validation, inferred contradiction handling without replacing active facts, and exclusion of semantic candidates from propagation.
- Partial/truncated/unknown transcript records, exclusion of hidden reasoning/system instructions, allowlisted project admission and committed cursors.
- Persistent budget reservations, provider failure/timeout accounting, output validation, vector dimensions, stable request caching and redaction.
- Durable job deduplication, lease recovery/ownership and restart behavior.
- HTTP authentication, Origin/Host checks, JSON mutation boundaries, and MCP stdio negotiation/errors.
- Signed webhook payloads, idempotency keys and per-project disclosure boundaries.
- Unix descendant timeout handling, restricted inherited credentials and bounded process output.
- Relocated relative Go replacements without changing manifests, real verification before publication, and isolated branch publication against a disposable **local bare Git remote**. The `gh` boundary is a controlled test double; no real test PR or Slack message is sent.

The dashboard has a functional DOM test against its own real isolated Go daemon. It validates rendered project/count/graph state, local-only consent labeling, search behavior, form interaction, default unchecked cloud consent, and absence of JavaScript runtime errors. It does not require model credentials. Run `npm ci --ignore-scripts && npm test` after `make build`.

GitHub Actions runs the Go race suite with a disposable PostgreSQL 17 service, builds the application, and runs the dashboard test on Node 22. See [CI results](https://github.com/jakecyr/stalemate/actions). Local Docker was unavailable, so PostgreSQL verification is supplied by CI rather than claimed as a local execution.

## Scope

This validates a local Codex-first service, Go API impact analysis, npm manifest exposure, SQLite persistence, configured model calls, and isolated repair. Claude remains a future source adapter. Real Slack delivery and real external draft PR creation were intentionally not enabled; their webhook/Git/CLI boundaries are tested locally. Multi-tenant authorization, distributed repository execution and ANN vector indexing remain enterprise extensions, as described in [architecture](architecture.md).
