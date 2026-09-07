#!/usr/bin/env python3
"""Exercise the ship-it to one-shot-tally native delivery handoff locally."""

import json
import os
import pathlib
import shutil
import subprocess
import sys
import tempfile


def run(command, cwd, env):
    result = subprocess.run(command, cwd=cwd, env=env, text=True, capture_output=True)
    if result.returncode != 0:
        raise AssertionError(f"command failed ({result.returncode}): {' '.join(command)}\n{result.stdout}\n{result.stderr}")
    return result.stdout


def hook(ship, event, cwd, env, expected_exit=0):
    result = subprocess.run([ship], cwd=cwd, env=env, input=json.dumps(event) + "\n", text=True, capture_output=True)
    if result.returncode != expected_exit:
        raise AssertionError(f"ship-it exit={result.returncode}, want {expected_exit}\n{result.stdout}\n{result.stderr}")
    lines = [line for line in result.stdout.splitlines() if line.strip()]
    if len(lines) != 1:
        raise AssertionError(f"expected one JSON stdout line, got {result.stdout!r}")
    try:
        return json.loads(lines[0]), result
    except json.JSONDecodeError as error:
        raise AssertionError(f"invalid hook JSON: {result.stdout!r}") from error


def main():
    if len(sys.argv) != 3:
        raise SystemExit("usage: test-native-delivery.py SHIP_IT ONE_SHOT_TALLY")
    ship_source = pathlib.Path(sys.argv[1]).resolve()
    tally_source = pathlib.Path(sys.argv[2]).resolve()
    for path in (ship_source, tally_source):
        if not path.is_file() or not os.access(path, os.X_OK):
            raise SystemExit(f"executable not found: {path}")

    keep = os.environ.get("SHIP_IT_KEEP_TEST_DIRS")
    temp = pathlib.Path(tempfile.mkdtemp(prefix="one-shot-native-delivery-"))
    if not keep:
        cleanup = True
    else:
        cleanup = False
    try:
        bin_dir = temp / "bin"
        home = temp / "home"
        remote = temp / "remote.git"
        work = temp / "work"
        state = temp / "state"
        deploy_log = temp / "deploy.log"
        for directory in (bin_dir, home, state):
            directory.mkdir(parents=True)
        shutil.copy2(ship_source, bin_dir / "ship-it")
        shutil.copy2(tally_source, bin_dir / "one-shot-tally")
        deploy = bin_dir / "deploy-it"
        deploy.write_text(
            "#!/usr/bin/env python3\n"
            "import pathlib, sys\n"
            f"log = pathlib.Path({str(deploy_log)!r})\n"
            "args = sys.argv[1:]\n"
            "commit = args[args.index('--commit') + 1]\n"
            "if __import__('os').environ.get('MULTI_ROOT_MODE'):\n"
            "    name = pathlib.Path.cwd().name\n"
            "    count_file = log.parent / (name + '.count')\n"
            "    count = int(count_file.read_text()) if count_file.exists() else 0\n"
            "    count_file.write_text(str(count + 1))\n"
            "    with log.open('a') as stream: stream.write(name + ' ' + commit + '\\n')\n"
            "    if name == 'repo-b' and count == 0: raise SystemExit(23)\n"
            "    raise SystemExit(0)\n"
            "with log.open('a') as stream: stream.write(commit + '\\n')\n"
            "if len(log.read_text().splitlines()) == 1: raise SystemExit(23)\n",
            encoding="utf-8",
        )
        deploy.chmod(0o755)
        env = os.environ.copy()
        env.update({
            "HOME": str(home),
            "ONE_SHOT_STATE_DIR": str(state),
            "GIT_CONFIG_GLOBAL": "/dev/null",
            "GIT_CONFIG_SYSTEM": "/dev/null",
            "PATH": str(bin_dir) + os.pathsep + env.get("PATH", ""),
        })
        run(["git", "init", "--bare", "-q", str(remote)], str(temp), env)
        run(["git", "init", "-b", "main", "-q", str(work)], str(temp), env)
        run(["git", "-C", str(work), "config", "user.name", "Native Delivery Test"], str(temp), env)
        run(["git", "-C", str(work), "config", "user.email", "native-delivery@example.test"], str(temp), env)
        (work / ".deploy-it.json").write_text("{}\n", encoding="utf-8")
        (work / "tracked.txt").write_text("base\n", encoding="utf-8")
        run(["git", "-C", str(work), "add", "."], str(temp), env)
        run(["git", "-C", str(work), "commit", "-qm", "base"], str(temp), env)
        run(["git", "-C", str(work), "remote", "add", "origin", str(remote)], str(temp), env)
        run(["git", "-C", str(work), "push", "-qu", "origin", "main"], str(temp), env)

        event = {"hook_event_name": "Stop", "status": "completed", "session_id": "native-session", "turn_id": "one", "cwd": str(work)}
        (work / "tracked.txt").write_text("first delivery\n", encoding="utf-8")
        first, first_process = hook(bin_dir / "ship-it", event, str(work), env)
        assert first_process.returncode == 0
        assert first.get("decision") == "block", first
        assert "FAILED" in first.get("systemMessage", ""), first
        logs = list((home / ".local" / "share" / "ship-it" / "hooks").glob("*.log"))
        assert logs, "ship-it did not leave a persistent failure log"
        pushed = run(["git", "-C", str(work), "rev-parse", "HEAD"], str(temp), env).strip()
        assert deploy_log.read_text(encoding="utf-8").strip() == pushed


        event["stop_hook_active"] = True
        retry, _ = hook(bin_dir / "ship-it", event, str(work), env)
        assert "RECOVERED" in retry.get("systemMessage", ""), retry
        assert "INCREDIBLE WIN" in retry.get("systemMessage", ""), retry
        assert "100" in retry.get("systemMessage", ""), retry
        assert len(deploy_log.read_text(encoding="utf-8").splitlines()) == 2

        before = deploy_log.read_text(encoding="utf-8")
        event["turn_id"] = "repeat"
        hook(bin_dir / "ship-it", event, str(work), env)
        assert deploy_log.read_text(encoding="utf-8") == before

        event.update({"turn_id": "two", "stop_hook_active": False})
        (work / "tracked.txt").write_text("verified delivery\n", encoding="utf-8")
        verified, _ = hook(bin_dir / "ship-it", event, str(work), env)
        assert "VERIFIED" in verified.get("systemMessage", ""), verified
        assert len(deploy_log.read_text(encoding="utf-8").splitlines()) == 3

        event["turn_id"] = "third"
        (work / "tracked.txt").write_text("continued delivery\n", encoding="utf-8")
        hook(bin_dir / "ship-it", event, str(work), env)
        assert len(deploy_log.read_text(encoding="utf-8").splitlines()) == 4
        latest = run(["git", "-C", str(work), "rev-parse", "HEAD"], str(temp), env).strip()
        assert deploy_log.read_text(encoding="utf-8").splitlines()[-1] == latest

        multi = temp / "multi"
        multi.mkdir()
        roots = []
        for name in ("repo-a", "repo-b"):
            repo = multi / name
            remote_repo = multi / (name + ".git")
            run(["git", "init", "--bare", "-q", str(remote_repo)], str(temp), env)
            run(["git", "init", "-b", "main", "-q", str(repo)], str(temp), env)
            run(["git", "-C", str(repo), "config", "user.name", "Native Delivery Test"], str(temp), env)
            run(["git", "-C", str(repo), "config", "user.email", "native-delivery@example.test"], str(temp), env)
            (repo / ".deploy-it.json").write_text("{}\n", encoding="utf-8")
            (repo / "tracked.txt").write_text("base\n", encoding="utf-8")
            run(["git", "-C", str(repo), "add", "."], str(temp), env)
            run(["git", "-C", str(repo), "commit", "-qm", "base"], str(temp), env)
            run(["git", "-C", str(repo), "remote", "add", "origin", str(remote_repo)], str(temp), env)
            run(["git", "-C", str(repo), "push", "-qu", "origin", "main"], str(temp), env)
            (repo / "tracked.txt").write_text(name + " edit\n", encoding="utf-8")
            roots.append(repo)
        env["MULTI_ROOT_MODE"] = "1"
        multi_event = {"hook_event_name": "Stop", "status": "completed", "session_id": "multi-session", "turn_id": "multi-one", "cwd": str(roots[0]), "workspace_roots": [str(root) for root in roots]}
        hook(bin_dir / "ship-it", multi_event, str(roots[0]), env)
        multi_event["stop_hook_active"] = True
        hook(bin_dir / "ship-it", multi_event, str(roots[0]), env)
        multi_lines = [line.split()[0] for line in deploy_log.read_text(encoding="utf-8").splitlines() if line.startswith("repo-")]
        assert multi_lines.count("repo-a") == 1, multi_lines
        assert multi_lines.count("repo-b") == 2, multi_lines
        del env["MULTI_ROOT_MODE"]

        (bin_dir / "one-shot-tally").write_text("#!/bin/sh\nprintf '%s' 'not-json'\n", encoding="utf-8")
        (bin_dir / "one-shot-tally").chmod(0o755)
        event.update({"turn_id": "reporter-unavailable", "stop_hook_active": False})
        (work / "tracked.txt").write_text("reporter unavailable\n", encoding="utf-8")
        before_unavailable = len(deploy_log.read_text(encoding="utf-8").splitlines())
        unavailable, _ = hook(bin_dir / "ship-it", event, str(work), env)
        assert "Delivery evidence unavailable" in unavailable.get("systemMessage", ""), unavailable
        assert len(deploy_log.read_text(encoding="utf-8").splitlines()) == before_unavailable + 1

        deploy.write_text("#!/bin/sh\nexit 23\n", encoding="utf-8")
        deploy.chmod(0o755)
        (bin_dir / "one-shot-tally").write_text("#!/bin/sh\nexit 19\n", encoding="utf-8")
        (bin_dir / "one-shot-tally").chmod(0o755)
        event["turn_id"] = "reporter-failure"
        (work / "tracked.txt").write_text("reporter failure\n", encoding="utf-8")
        _, failed_process = hook(bin_dir / "ship-it", event, str(work), env, expected_exit=1)
        assert failed_process.returncode != 0
        print(f"native delivery integration passed; fixture={temp}")
    finally:
        if cleanup:
            shutil.rmtree(temp, ignore_errors=True)


if __name__ == "__main__":
    main()
