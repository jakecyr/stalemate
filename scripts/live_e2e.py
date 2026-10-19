#!/usr/bin/env python3
"""Opt-in real Codex/OpenAI test. No mocks. Uses a disposable pair of Git repos.

Requires bin/stalemate, authenticated Codex, and OPENAI_API_KEY (or repo .env).
Evidence stays in .stalemate/live-*; only a sanitized report is suitable to commit.
API spending is capped at $2 by default, far below the authorized $20 test limit.
"""
from __future__ import annotations
import argparse
import datetime as dt
import json
import os
from pathlib import Path
import sqlite3
import subprocess
import sys
import time
import urllib.request

ROOT = Path(__file__).resolve().parents[1]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--budget", type=float, default=2.0)
    parser.add_argument("--output", type=Path)
    args = parser.parse_args()
    if args.budget <= 0 or args.budget > 20:
        parser.error("budget must be greater than zero and at most $20")
    stamp = dt.datetime.now(dt.timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    root = (args.output or ROOT / ".stalemate" / ("live-" + stamp)).resolve()
    root.mkdir(parents=True, mode=0o700)
    binary = ROOT / "bin" / "stalemate"
    cfg = root / "config.json"
    env_file = ROOT / ".env"
    prefix = [str(binary), "--config", str(cfg)]
    if env_file.exists():
        prefix += ["--env-file", str(env_file)]
    report = {"started": stamp, "checks": [], "models": {}, "api_usage": {}}

    def command(argv, cwd=ROOT, *, expected=0, log=None, timeout=300, extra_env=None):
        env = os.environ.copy()
        if extra_env:
            env.update(dict(entry.split("=", 1) for entry in extra_env))
        result = subprocess.run([str(x) for x in argv], cwd=cwd, capture_output=True, text=True, timeout=timeout, env=env)
        if log:
            (root / log).write_text(result.stdout + "\n" + result.stderr)
        if result.returncode != expected:
            raise RuntimeError(f"Command {argv[0:3]} exited {result.returncode}; inspect {root / (log or 'logs')}.\n{result.stderr[-2000:]}")
        return result.stdout

    def cli(*argv, **kwargs):
        return command(prefix + list(argv), **kwargs)

    def passed(label):
        report["checks"].append({"check": label, "passed": True})
        print("PASS:", label, flush=True)

    def commit(path, message):
        command(["git", "add", "."], cwd=path)
        command(["git", "commit", "-m", message], cwd=path)

    def agent(path, prompt, log):
        print("Running real Codex:", log, flush=True)
        cli("run", "--project", path, "--model", "gpt-6-luna", "--timeout", "240", "--", prompt,
            log=log, timeout=600)

    cli("init")
    config = json.loads(cfg.read_text())
    config["data_dir"] = str(root / "data")
    config["database"]["dsn"] = str(root / "data" / "stalemate.sqlite")
    config["capture"]["codex_home"] = str(root / "empty-codex-home")
    config["capture"]["poll_seconds"] = 1
    config["ai"]["provider"] = "openai"
    config["ai"]["budget_usd"] = args.budget
    config["ai"]["timeout_seconds"] = 120
    config["automation"]["mode"] = "manual"
    config["automation"]["timeout_seconds"] = 240
    config["automation"]["max_runs"] = 1
    config["http"]["listen"] = "127.0.0.1:17433"
    cfg.write_text(json.dumps(config, indent=2))
    report["models"] = {k: config["ai"][k] for k in ("extraction_model", "reasoning_model", "embedding_model")}

    lib, app = root / "tokenlib", root / "portal"
    for path, module in [(lib, "example.com/stalemate/tokenlib"), (app, "example.com/stalemate/portal")]:
        path.mkdir()
        (path / "go.mod").write_text(f"module {module}\n\ngo 1.25.3\n")
        command(["git", "init", "-b", "master"], cwd=path)
        command(["git", "config", "user.name", "Stalemate E2E"], cwd=path)
        command(["git", "config", "user.email", "stalemate-test@localhost"], cwd=path)
        commit(path, "test: initialize fixture")
    (app / "go.mod").write_text("module example.com/stalemate/portal\n\ngo 1.25.3\n\nrequire example.com/stalemate/tokenlib v0.0.0\nreplace example.com/stalemate/tokenlib => ../tokenlib\n")
    commit(app, "test: declare local dependency")
    libp = json.loads(cli("project", "add", "--cloud", lib))
    appp = json.loads(cli("project", "add", "--cloud", app))
    cli("scan")
    agent(lib, 'Create token.go package tokenlib with exported function Encode(user string) string returning "v1:" + user, and Unused() string returning "stable". Add token_test.go proving Encode("alice") returns "v1:alice". Run go test ./... . Do not commit or use any external dependencies.', "01-library-codex.jsonl")
    command(["go", "test", "./..."], cwd=lib)
    commit(lib, "feat: implement token library")
    agent(app, 'Create portal.go package portal with exported Greeting(user string) string calling example.com/stalemate/tokenlib.Encode(user). Add portal_test.go verifying Greeting("alice") returns "v1:alice". The local dependency and replace are already in go.mod. Run go test ./... . Do not commit or modify the library.', "02-consumer-codex.jsonl")
    command(["go", "test", "./..."], cwd=app)
    commit(app, "feat: consume token API")
    cli("scan")
    db = sqlite3.connect(root / "data" / "stalemate.sqlite")
    edges = [json.loads(row[0]) for row in db.execute("SELECT body FROM relationships WHERE valid_to=0 AND source='static_analysis'")]
    assert len(edges) == 1 and edges[0]["from"] == appp["id"] and edges[0]["to"] == libp["id"], edges
    assert "example.com/stalemate/tokenlib.Encode" in edges[0]["apis"], edges
    passed("Exact dependency and consumed API discovered from actual Codex-generated code")
    vectors = [json.loads(row[0]) for row in db.execute("SELECT body FROM memories WHERE model!='disabled'")]
    assert len(vectors) >= 2 and all(len(v["embedding"]) == 512 for v in vectors), vectors
    passed("Real OpenAI extraction and 512-dimensional embeddings persisted locally")
    before = db.execute("SELECT COUNT(*) FROM usage_ledger").fetchone()[0]
    cli("ingest", "--force")
    cli("worker")
    assert db.execute("SELECT COUNT(*) FROM usage_ledger").fetchone()[0] == before
    passed("Replay of completed sessions makes zero additional model calls")
    # Precision control: change an exported API the consumer does not use.
    text = (lib / "token.go").read_text()
    assert '"stable"' in text
    (lib / "token.go").write_text(text.replace('"stable"', '"still-compatible"'))
    cli("scan")
    assert not json.loads(cli("impacts"))
    commit(lib, "test: change unused API implementation")
    passed("Unused library API change creates no false alert")
    agent(lib, 'Make an intentionally breaking next version: rename Encode(user string) to EncodeV2(user string), return "v2:" + user, and remove the old Encode API completely. Update only this library and its tests, verifying EncodeV2("alice") is "v2:alice". Keep Unused unchanged. Run go test ./... . Do not commit or change any other project.', "03-breaking-change-codex.jsonl")
    command(["go", "test", "./..."], cwd=lib)
    failed = subprocess.run(["go", "test", "./..."], cwd=app, capture_output=True, text=True)
    assert failed.returncode != 0 and "Encode" in failed.stdout + failed.stderr
    impacts = json.loads(cli("impacts"))
    confirmed = [i for i in impacts if i["affected_project"] == appp["id"] and i["severity"] == "confirmed"]
    assert len(confirmed) == 1, impacts
    passed("Breaking provider change alerts the real consumer; its actual test fails as predicted")
    context = json.loads(cli("context", "--project", app, "--task", "Update token library integration"))
    assert any(i["id"] == confirmed[0]["id"] for i in context["impacts"])
    passed("Resumed consumer receives the actionable impact in task context")
    # Exercise strong-model reasoning on an actual, uncertain assumption change.
    old = json.loads(cli("record", "--project", lib, "--key", "release-compatibility", "--text", "The v1 token prefix remains supported through the next release.", "--evidence", "token.go original Encode returned v1:", "--kind", "assumption"))
    cli("assume", "--project", app, "--claim", old["id"], "--evidence", "Portal tests rely on the v1 prefix contract")
    cli("record", "--project", lib, "--key", "release-compatibility", "--text", "The next release removes v1 and requires v2 tokens.", "--evidence", "token.go EncodeV2 now emits v2:", "--previous", old["id"], "--kind", "assumption")
    cli("worker", log="04-reasoning.log", timeout=300)
    assert db.execute("SELECT COUNT(*) FROM usage_ledger WHERE operation='impact_assessment' AND status='complete'").fetchone()[0] >= 1
    passed("Versioned assumption invalidation triggers real GPT-6.1 Sol assessment")
    # Keep origin clean; optional repair must leave its original consumer untouched.
    commit(lib, "feat: release breaking v2 API")
    print("Running real Codex worktree remediation", flush=True)
    run = json.loads(cli("remediate", "--id", confirmed[0]["id"], log="05-remediation.json", timeout=400))
    assert run["status"] == "review_required", run
    worktree = Path(run["worktree"])
    command(["go", "test", "./..."], cwd=worktree, extra_env=run.get("verification_env"))
    assert "../tokenlib" in (worktree / "go.mod").read_text()
    assert "Encode(" in (app / "portal.go").read_text()
    passed("Actual Codex repairs an isolated worktree and passes tests without modifying the original project")
    assert not run["pr"]
    passed("PR publication remains off")
    # Test the native transcript fallback and a real CLI notify invocation.
    config["capture"]["codex_home"] = str(Path(os.environ.get("CODEX_HOME", str(Path.home() / ".codex"))))
    cfg.write_text(json.dumps(config, indent=2))
    native = command(["codex", "exec", "--ignore-user-config", "-c", 'approval_policy="never"',
                      "-c", "notify=" + json.dumps(prefix + ["hook"]),
                      "--sandbox", "read-only", "--model", "gpt-6-luna", "-c", 'model_reasoning_effort="low"',
                      "--json", "-C", lib,
                      "Read token.go and briefly report the exported token API and supported prefix. Do not change files."],
                     log="06-native-codex.jsonl", timeout=240)
    cli("ingest", "--force")
    cli("worker", timeout=300)
    sessions = [json.loads(row[0]) for row in db.execute("SELECT body FROM sessions")]
    assert any("/spool/" not in s["path"] and s["cursor"] > 0 for s in sessions)
    passed("Native Codex session is captured incrementally from its real home transcript")
    hook_count = db.execute("SELECT COUNT(*) FROM jobs WHERE kind='capture'").fetchone()[0]
    report["native_notify_events"] = hook_count
    if hook_count:
        passed("Real Codex notify hook creates and processes a durable trigger")
    else:
        report.setdefault("notes", []).append("Installed Codex exec did not emit notify; native transcript fallback was verified.")
    mcp_output = command(["codex", "exec", "--ignore-user-config", "--ephemeral", "-c", 'approval_policy="never"',
                         "--sandbox", "read-only", "--model", "gpt-6-luna", "-c", 'model_reasoning_effort="low"',
                         "-c", "mcp_servers.stalemate.command=" + json.dumps(str(binary)),
                         "-c", "mcp_servers.stalemate.args=" + json.dumps(prefix[1:] + ["mcp"]),
                         "-c", 'mcp_servers.stalemate.enabled_tools=["context_get"]',
                         "-c", 'mcp_servers.stalemate.tools.context_get.approval_mode="approve"',
                         "--json", "-C", app,
                         f"Call the stalemate context_get MCP tool for project {appp['id']} with task 'Revalidate token integration'. Report the open impact IDs. Do not modify any files."],
                        log="07-mcp-codex.jsonl", timeout=240)
    events = [json.loads(line) for line in mcp_output.splitlines() if line.strip()]
    assert any(e.get("item", {}).get("type") == "mcp_tool_call" and e["item"].get("tool") == "context_get" for e in events), events
    assert confirmed[0]["id"] in mcp_output
    passed("A real Codex subprocess connects to MCP and retrieves the consumer's open impact")
    log = (root / "08-daemon.log").open("w")
    daemon = subprocess.Popen(prefix + ["serve"], cwd=ROOT, stdout=log, stderr=log)
    try:
        base = "http://127.0.0.1:17433"
        for _ in range(60):
            try:
                with urllib.request.urlopen(base + "/healthz", timeout=1) as response:
                    assert json.load(response)["status"] == "ok"
                break
            except OSError:
                if daemon.poll() is not None:
                    raise RuntimeError("daemon failed; inspect 08-daemon.log")
                time.sleep(0.1)
        else:
            raise RuntimeError("daemon health check timed out")
        with urllib.request.urlopen(base + "/", timeout=5) as response:
            assert "Stalemate" in response.read().decode()
        with urllib.request.urlopen(base + "/api/impacts", timeout=5) as response:
            assert any(i["id"] == confirmed[0]["id"] for i in json.load(response))
        request = urllib.request.Request(base + "/api/impacts/" + confirmed[0]["id"],
                                         data=json.dumps({"status": "acknowledged"}).encode(),
                                         headers={"Content-Type": "application/json"}, method="POST")
        with urllib.request.urlopen(request, timeout=5) as response:
            assert json.load(response)["status"] == "acknowledged"
        passed("Real daemon serves the dashboard and API; HTTP acknowledgement persists")
    finally:
        daemon.terminate()
        daemon.wait(timeout=20)
        log.close()
    cli("acknowledge", "--id", confirmed[0]["id"])
    assert not any(i["id"] == confirmed[0]["id"] for i in json.loads(cli("impacts")))
    passed("Acknowledgement persists and removes the alert from open work")
    row = db.execute("SELECT COALESCE(SUM(CASE WHEN status='complete' THEN cost ELSE 0 END),0),COALESCE(SUM(CASE WHEN status='reserved' THEN reserved ELSE 0 END),0),COUNT(*),SUM(input_tokens),SUM(output_tokens) FROM usage_ledger").fetchone()
    report["api_usage"] = dict(zip(("spent_usd", "reserved_usd", "calls", "input_tokens", "output_tokens"), row))
    assert row[0] + row[1] <= args.budget
    report["checks_passed"] = len(report["checks"])
    report["finished"] = dt.datetime.now(dt.timezone.utc).isoformat()
    report["codex_version"] = command(["codex", "--version"]).strip()
    report.setdefault("notes", []).extend(["Codex subprocesses use existing CLI authentication; subscription usage is separate from API ledger.", "No Slack message sent; no PR published; fixture worktrees retained for inspection."])
    (root / "report.json").write_text(json.dumps(report, indent=2) + "\n")
    print(json.dumps(report, indent=2), flush=True)
    print("Live evidence:", root, flush=True)


if __name__ == "__main__":
    main()
