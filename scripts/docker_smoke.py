#!/usr/bin/env python3
"""Exercise the real image, volume persistence, non-root runtime and authentication."""
import json
import subprocess
import time
import urllib.error
import urllib.request
import uuid


def run(*args):
    return subprocess.check_output(["docker", *args], text=True).strip()


def main():
    name = "stalemate-test-" + uuid.uuid4().hex[:8]
    volume = name + "-data"
    run("volume", "create", volume)
    try:
        run("run", "--detach", "--name", name, "--publish", "127.0.0.1::7433", "--volume", volume + ":/data", "stalemate:test")
        port = run("port", name, "7433/tcp").split(":")[-1]
        url = "http://127.0.0.1:" + port
        for _ in range(60):
            try:
                with urllib.request.urlopen(url + "/healthz", timeout=1) as response:
                    assert json.load(response)["status"] == "ok"
                break
            except (OSError, urllib.error.URLError):
                time.sleep(1)
        else:
            raise AssertionError("Container never became healthy")
        try:
            urllib.request.urlopen(url + "/api/status")
            raise AssertionError("API must require a token")
        except urllib.error.HTTPError as error:
            assert error.code == 401
        token = run("exec", name, "cat", "/data/api-token")
        assert len(token) == 64
        request = urllib.request.Request(url + "/api/status", headers={"Authorization": "Bearer " + token})
        with urllib.request.urlopen(request) as response:
            assert json.load(response)["provider"] == "disabled"
        # PID 1 becomes the unprivileged Go service, with no root supervisor.
        status = run("exec", name, "cat", "/proc/1/status")
        assert "Uid:\t10001\t10001\t10001\t10001" in status
        run("restart", name)
        for _ in range(30):
            try:
                with urllib.request.urlopen(request, timeout=1) as response:
                    assert response.status == 200
                break
            except OSError:
                time.sleep(1)
        else:
            raise AssertionError("Container did not restart")
        assert run("exec", name, "cat", "/data/api-token") == token
        assert "stalemate.sqlite" in run("exec", name, "ls", "/data")
        print("Docker smoke test passed: health, auth, non-root process, durable volume, restart")
    finally:
        subprocess.run(["docker", "rm", "--force", name], stdout=subprocess.DEVNULL, check=False)
        subprocess.run(["docker", "volume", "rm", volume], stdout=subprocess.DEVNULL, check=False)


if __name__ == "__main__":
    main()
