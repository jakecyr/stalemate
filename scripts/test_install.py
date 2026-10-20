"""Offline release mirror exercising the actual compiled CLI and POSIX installer."""
import functools
import hashlib
import http.server
import json
import os
from pathlib import Path
import platform
import shutil
import socket
import subprocess
import tarfile
import tempfile
import threading
import unittest
import urllib.error
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
BINARY = ROOT / "bin/stalemate"
INSTALLER = ROOT / "install.sh"


class QuietHandler(http.server.SimpleHTTPRequestHandler):
    def log_message(self, *args):
        pass


class InstallerTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        if not BINARY.exists():
            raise RuntimeError("Run make build before installer tests")
        cls.mirror = tempfile.TemporaryDirectory()
        system = {"Darwin": "darwin", "Linux": "linux"}[platform.system()]
        arch = {"x86_64": "amd64", "arm64": "arm64", "aarch64": "arm64"}[platform.machine()]
        cls.archive = f"stalemate_{system}_{arch}.tar.gz"
        path = Path(cls.mirror.name) / cls.archive
        with tarfile.open(path, "w:gz") as archive:
            archive.add(BINARY, "stalemate")
        cls.checksum = hashlib.sha256(path.read_bytes()).hexdigest()
        cls.checksums = Path(cls.mirror.name) / "checksums.txt"
        cls.checksums.write_text(f"{cls.checksum}  {cls.archive}\n")
        cls.server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), functools.partial(QuietHandler, directory=cls.mirror.name))
        cls.thread = threading.Thread(target=cls.server.serve_forever, daemon=True)
        cls.thread.start()

    @classmethod
    def tearDownClass(cls):
        cls.server.shutdown()
        cls.server.server_close()
        cls.thread.join()
        cls.mirror.cleanup()

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="stalemate test ' ")
        self.home = Path(self.temp.name)
        self.cwd = self.home / "project"
        self.cwd.mkdir()
        self.env = {k: v for k, v in os.environ.items() if not k.startswith(("STALEMATE_", "OPENAI_", "CODEX_"))}
        self.env.update(HOME=str(self.home), SHELL="/bin/zsh", STALEMATE_CONFIG_DIR=str(self.home / "settings"),
                        STALEMATE_DATA_DIR=str(self.home / "data"), STALEMATE_DOWNLOAD_BASE_URL=f"http://127.0.0.1:{self.server.server_port}")
        self.binary = self.home / ".local/bin/stalemate"
        self.config = self.home / "settings/config.json"

    def tearDown(self):
        if self.binary.exists() and self.config.exists():
            self.cli("stop", check=False)
        self.temp.cleanup()

    def install(self, *args, check=True):
        result = subprocess.run(["sh", str(INSTALLER), *args], env=self.env, cwd=self.cwd, capture_output=True, text=True, timeout=60)
        if check and result.returncode:
            self.fail(f"Installer failed ({result.returncode}): {result.stdout} {result.stderr}")
        return result

    def cli(self, *args, check=True):
        return subprocess.run([str(self.binary), *args], env=self.env, cwd=self.cwd, capture_output=True, text=True, check=check, timeout=30)

    def free_port(self):
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            return sock.getsockname()[1]

    def test_install_preserves_settings_and_quotes_path(self):
        self.install("--no-start", "--version", "v0.1.0")
        self.assertEqual(self.config.stat().st_mode & 0o777, 0o600)
        environment = self.config.parent / "environment"
        self.assertEqual(environment.stat().st_mode & 0o777, 0o600)
        original = self.config.read_bytes()
        self.assertEqual(json.loads(original)["ai"]["provider"], "disabled")
        self.install("--no-start")
        self.assertEqual(original, self.config.read_bytes())
        profile = (self.home / ".zshrc").read_text()
        self.assertEqual(profile.count("# stalemate installer"), 1)
        # A profile must remain valid even when HOME contains spaces/apostrophes.
        found = subprocess.check_output(["sh", "-c", '. "$HOME/.zshrc"; command -v stalemate'], env=self.env, text=True).strip()
        self.assertEqual(found, str(self.binary))
        self.assertFalse(json.loads(self.cli("status").stdout)["running"])
        self.assertEqual(len(json.loads(self.cli("doctor", "--json").stdout)), 4)

    def test_checksum_failure_leaves_previous_binary_untouched(self):
        self.install("--no-start", "--no-path")
        original = self.binary.read_bytes()
        self.checksums.write_text("0" * 64 + f"  {self.archive}\n")
        try:
            result = self.install("--no-start", check=False)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("Checksum mismatch", result.stderr)
            self.assertEqual(original, self.binary.read_bytes())
        finally:
            self.checksums.write_text(f"{self.checksum}  {self.archive}\n")

    def test_real_service_start_restart_upgrade_and_stop(self):
        port = self.free_port()
        self.install("--no-path", "--listen", f"127.0.0.1:{port}")
        state = json.loads(self.cli("status").stdout)
        self.assertTrue(state["running"])
        with urllib.request.urlopen(f"http://127.0.0.1:{port}/healthz") as response:
            self.assertEqual(response.status, 200)
        self.cli("start")  # idempotent
        self.assertEqual(json.loads(self.cli("status").stdout)["pid"], state["pid"])
        self.install("--no-path")  # real running upgrade, settings remain intact
        upgraded = json.loads(self.cli("status").stdout)
        self.assertTrue(upgraded["running"])
        self.assertNotEqual(upgraded["pid"], state["pid"])
        self.assertEqual(upgraded["url"], state["url"])
        self.assertTrue((self.home / "data/stalemate.sqlite").exists())
        # Changing the port must still stop the previous process safely.
        new_port = self.free_port()
        self.cli("setup", "--listen", f"127.0.0.1:{new_port}")
        self.assertEqual(json.loads(self.cli("status").stdout)["url"], f"http://127.0.0.1:{new_port}")
        self.cli("stop")
        self.assertFalse(json.loads(self.cli("status").stdout)["running"])
        self.cli("stop")

    def test_secret_is_private_and_not_in_config_or_output(self):
        self.env["OPENAI_API_KEY"] = "fixture-secret-never-real"
        result = self.install("--no-start", "--openai")
        self.assertNotIn(self.env["OPENAI_API_KEY"], result.stdout + result.stderr + self.config.read_text())
        secret = self.config.parent / "environment"
        self.assertIn(self.env["OPENAI_API_KEY"], secret.read_text())
        self.assertEqual(secret.stat().st_mode & 0o777, 0o600)
        self.assertEqual(json.loads(self.config.read_text())["ai"]["provider"], "openai")

    def test_custom_config_discovers_its_private_environment(self):
        self.install("--no-start")
        custom = self.home / "custom/config.json"
        self.env["OPENAI_API_KEY"] = "fixture-custom-secret"
        self.cli("--config", str(custom), "setup", "--openai", "--no-start")
        del self.env["OPENAI_API_KEY"]
        # Provider initialization requires the key but listing projects makes no
        # model requests. This must work from a different working directory.
        result = self.cli("--config", str(custom), "project", "list")
        self.assertNotIn("fixture-custom-secret", result.stdout + result.stderr)

    def test_occupied_port_does_not_stop_other_service(self):
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            sock.listen()
            result = self.install("--no-path", "--listen", f"127.0.0.1:{sock.getsockname()[1]}", check=False)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("port is already in use", result.stderr)

    def test_download_policy_rejects_insecure_remote(self):
        self.env["STALEMATE_DOWNLOAD_BASE_URL"] = "http://example.invalid"
        self.assertIn("require HTTPS", self.install(check=False).stderr)
        self.assertFalse(self.binary.exists())

    def test_missing_git_has_actionable_error_without_package_changes(self):
        tools = self.home / "tools"
        tools.mkdir()
        git = tools / "git"
        git.write_text("#!/bin/sh\nexit 1\n")
        git.chmod(0o755)
        self.env["PATH"] = str(tools) + ":" + self.env["PATH"]
        result = self.install("--no-deps", check=False)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("Git is required", result.stderr)
        self.assertFalse(self.binary.exists())
