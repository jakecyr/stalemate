#!/usr/bin/env python3
"""Plan deterministic stable releases from published versions and conventional commits."""
import argparse
import json
import re
import subprocess
from pathlib import Path

SEMVER = re.compile(r"^v(\d+)\.(\d+)\.(\d+)$")
HEADER = re.compile(r"^(?P<type>[a-z]+)(?:\([^\r\n)]+\))?(?P<breaking>!)?: .+", re.M)


def git(*args):
    return subprocess.check_output(["git", *args], text=True).strip()


def plan(releases):
    versions = [(tuple(map(int, m.groups())), r["tagName"])
                for r in releases if not r["isDraft"] and not r["isPrerelease"]
                and (m := SEMVER.fullmatch(r["tagName"]))]
    previous, tag = max(versions, default=((0, 0, 0), None))
    if tag:
        # A retry of an older workflow must never release over a newer version.
        if subprocess.run(["git", "merge-base", "--is-ancestor", tag, "HEAD"]).returncode:
            if subprocess.run(["git", "merge-base", "--is-ancestor", "HEAD", tag]).returncode == 0:
                return {"release": False, "notes": "Already included in a newer release."}
            raise ValueError("Latest release is not an ancestor of this commit")
    commits = git("log", "--format=%H%x00%B%x00", f"{tag}..HEAD" if tag else "HEAD")
    fields = commits.split("\0")
    bump, entries = 0, []
    for index in range(0, len(fields) - 1, 2):
        sha, message = fields[index].strip(), fields[index + 1].strip()
        header = HEADER.match(message)
        if not header:
            continue
        breaking = header["breaking"] or re.search(r"^BREAKING[ -]CHANGE: .+", message, re.M)
        level = 3 if breaking else 2 if header["type"] == "feat" else 1 if header["type"] in {"fix", "perf", "revert"} else 0
        bump = max(bump, level)
        if level:
            # Commit text stays data; never interpolated into a shell command.
            entries.append(f"- {message.splitlines()[0]} ({sha[:7]})")
    if not bump:
        return {"release": False, "notes": "No releasable conventional commits."}
    major, minor, patch = previous
    version = (major + 1, 0, 0) if bump == 3 else (major, minor + 1, 0) if bump == 2 else (major, minor, patch + 1)
    name = "v" + ".".join(map(str, version))
    notes = f"Changes since {tag or 'the beginning'}:\n\n" + "\n".join(entries) + "\n\n"
    notes += "Install or upgrade:\n\n```sh\ncurl -fsSL https://raw.githubusercontent.com/jakecyr/stalemate/master/install.sh | sh\n```\n\n"
    notes += f"Container: `ghcr.io/jakecyr/stalemate:{name}`. See the repository for Docker Compose and configuration.\n"
    return {"release": True, "version": name, "notes": notes}


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--releases", required=True)
    parser.add_argument("--notes", required=True)
    parser.add_argument("--github-output")
    args = parser.parse_args()
    result = plan(json.loads(Path(args.releases).read_text()))
    Path(args.notes).write_text(result.pop("notes"))
    if args.github_output:
        with open(args.github_output, "a") as output:
            output.write(f"release={str(result['release']).lower()}\nversion={result.get('version', '')}\n")
    print(json.dumps(result))


if __name__ == "__main__":
    main()
