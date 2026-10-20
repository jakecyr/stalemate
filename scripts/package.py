#!/usr/bin/env python3
"""Build a release archive without a compiler or database on the target machine."""
import argparse
import os
from pathlib import Path
import subprocess
import tarfile
import tempfile
import zipfile


def main():
    parser = argparse.ArgumentParser()
    for name in ("os", "arch", "version", "commit", "date"):
        parser.add_argument("--" + name, required=True)
    parser.add_argument("--output", default="dist")
    args = parser.parse_args()
    output = Path(args.output).resolve()
    output.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory() as temp:
        binary = Path(temp) / ("stalemate.exe" if args.os == "windows" else "stalemate")
        flags = "-s -w " + " ".join(f"-X github.com/jakecyr/stalemate/internal/buildinfo.{key}={value}"
                                   for key, value in {"Version": args.version, "Commit": args.commit, "Date": args.date}.items())
        subprocess.run(["go", "build", "-trimpath", "-ldflags", flags, "-o", str(binary), "./cmd/stalemate"],
                       env={**os.environ, "CGO_ENABLED": "0", "GOOS": args.os, "GOARCH": args.arch}, check=True)
        stem = output / f"stalemate_{args.os}_{args.arch}"
        files = [binary, Path("README.md")]
        if args.os == "windows":
            with zipfile.ZipFile(str(stem) + ".zip", "w", zipfile.ZIP_DEFLATED) as archive:
                for path in files:
                    archive.write(path, path.name)
        else:
            with tarfile.open(str(stem) + ".tar.gz", "w:gz") as archive:
                for path in files:
                    archive.add(path, path.name)


if __name__ == "__main__":
    main()
