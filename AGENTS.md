# Repository guidance

Stalemate is a local evidence ledger, not an authoritative source of task completion.

- Keep capture, identity association and consequence reasoning separate. Model work belongs in durable jobs, never hooks.
- Only registered canonical project paths may be captured. Preserve per-project cloud consent on every outbound path.
- Redact before persistence/disclosure. Never commit `.env`, runtime databases, spools, credentials or live fixture logs.
- Semantic similarity and model links are candidates. Only static/explicit dependency edges participate in impact propagation.
- Keep historical claims and relationship versions. Proposed extractions must not silently supersede active assertions.
- Preserve durable reservations, cursor/job atomicity, lease ownership and idempotent impact IDs.
- Default automated repair and PR publishing to off. Generated fixes require verification in an isolated worktree.
- Run `make check` and `make build` for code changes. PostgreSQL integration is enabled by `STALEMATE_TEST_POSTGRES` against a disposable database; CI supplies one.
- `make live-test` spends real API/CLI usage. It is opt-in, defaults to a $2 API limit, and retains private artifacts under ignored `.stalemate/`. Do not run it in normal CI or against user projects.
- Update architecture/operations documentation when changing guarantees or analysis limits. Keep provider/source/storage boundaries explicit.
