#!/usr/bin/env python3
"""Check Codex's real hook parser without starting a model task or editing user config."""
import argparse
import json
import os
from pathlib import Path
import select
import subprocess
import tempfile
import time


def discover(codex, project, config):
    env = os.environ.copy()
    # CODEX_HOME retains its normal meaning: an isolated Codex config directory.
    env["CODEX_HOME"] = str(config)
    process = subprocess.Popen(
        [codex, "app-server", "--stdio"], cwd=project, env=env,
        stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
        text=True, bufsize=1,
    )
    def send(message):
        process.stdin.write(json.dumps(message) + "\n")
        process.stdin.flush()
    try:
        send({"id": 1, "method": "initialize", "params": {
            "clientInfo": {"name": "sparestep-discovery-check", "version": "0.1.0"},
            "capabilities": {"experimentalApi": True},
        }})
        deadline = time.monotonic() + 15
        while time.monotonic() < deadline:
            if not select.select([process.stdout], [], [], 1)[0]:
                continue
            line = process.stdout.readline()
            if not line:
                raise RuntimeError("Codex exited before returning its hook catalog")
            response = json.loads(line)
            if response.get("id") == 1:
                if "error" in response:
                    raise RuntimeError("Codex initialization failed")
                send({"method": "initialized"})
                send({"id": 2, "method": "hooks/list", "params": {"cwds": [str(project)]}})
            if response.get("id") == 2:
                if "error" in response:
                    raise RuntimeError("Installed Codex does not support hooks/list")
                entry = response["result"]["data"][0]
                assert not entry.get("errors"), entry["errors"]
                assert not entry.get("warnings"), entry["warnings"]
                return entry["hooks"]
        raise RuntimeError("Timed out waiting for Codex hook discovery")
    finally:
        process.terminate()
        try:
            process.wait(timeout=3)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait()


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--binary", default="dist/sparestep-linux-amd64")
    parser.add_argument("--codex", default="codex")
    args = parser.parse_args()
    binary = str(Path(args.binary).resolve())
    with tempfile.TemporaryDirectory(prefix="sparestep-discovery-") as temp:
        root = Path(temp)
        project, config, state = root / "project with spaces", root / "codex-config", root / "state"
        project.mkdir()
        config.mkdir()
        subprocess.run(["git", "init", "-q", str(project)], check=True)
        (config / "config.toml").write_text(
            "[features]\nhooks = true\n[projects." + json.dumps(str(project)) + "]\ntrust_level = \"trusted\"\n"
        )
        assert discover(args.codex, project, config) == []
        subprocess.run([binary, "connect", "--project", str(project), "--state-dir", str(state)], check=True, stdout=subprocess.DEVNULL)
        configured = discover(args.codex, project, config)
        required = {"sessionStart", "userPromptSubmit", "preToolUse", "postToolUse", "stop", "postCompact"}
        assert {hook["eventName"] for hook in configured} == required, configured
        assert all(hook["enabled"] and hook["trustStatus"] == "untrusted" for hook in configured)
        subprocess.run([binary, "disconnect", "--project", str(project), "--state-dir", str(state)], check=True, stdout=subprocess.DEVNULL)
        assert discover(args.codex, project, config) == []
        print("PASS: Codex discovers all six hooks; native trust remains required; disconnect removes them.")
        print("No model task or user configuration was changed.")


if __name__ == "__main__":
    main()
