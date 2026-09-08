#!/usr/bin/env python3
"""Black-box focus/guard check using only the built Sparestep CLI.

The check creates a temporary Git worktree with spaces in its path, uses a
private state directory, and never configures or contacts an external issue
tracker. It intentionally exercises the public CLI and native hook JSON.
"""

from __future__ import annotations

import json
import os
from pathlib import Path
import argparse
import shutil
import subprocess
import sys
import tempfile


REPO = Path(__file__).resolve().parents[1]


def fail(message: str) -> None:
    raise AssertionError(message)


def main(binary_arg: str | None) -> None:
    with tempfile.TemporaryDirectory(prefix="sparestep-focus-") as tmp:
        root = Path(tmp)
        project = root / "project with spaces"
        project.mkdir()
        (project / "src").mkdir()
        (project / "docs").mkdir()
        (project / "src" / "main.go").write_text("package main\n")
        subprocess.run(["git", "init", "-q", str(project)], check=True)
        subprocess.run(["git", "-C", str(project), "config", "user.name", "Sparestep check"], check=True)
        subprocess.run(["git", "-C", str(project), "config", "user.email", "check@example.invalid"], check=True)
        subprocess.run(["git", "-C", str(project), "add", "."], check=True)
        subprocess.run(["git", "-C", str(project), "commit", "-qm", "fixture"], check=True)
        state = root / "state with spaces"
        codex_home = root / "codex home"
        if binary_arg:
            binary = Path(binary_arg).resolve()
            if not binary.is_file():
                fail(f"binary does not exist: {binary}")
        else:
            go = os.environ.get("SPARESTEP_GO") or shutil.which("go")
            if not go:
                pinned = Path("/home/noah/.local/share/sparestep-build/go/bin/go")
                if pinned.is_file():
                    go = str(pinned)
            if not go:
                fail("no Go compiler found; pass --binary or set SPARESTEP_GO")
            binary = root / "sparestep-focus-check"
            subprocess.run([go, "build", "-o", str(binary), "."], cwd=REPO, check=True, timeout=60)

        base_env = os.environ.copy()
        base_env.update({
            "CODEX_HOME": str(codex_home),
            "CODEX_SESSION_ID": "session-amend",
            "CODEX_THREAD_ID": "thread-root",
        })

        def run(args: list[str], *, payload: object | None = None, env: dict[str, str] | None = None,
                expected: int = 0) -> subprocess.CompletedProcess[str]:
            command = [str(binary), *args]
            result = subprocess.run(command, cwd=project, env=env or base_env, text=True,
                                    input=None if payload is None else json.dumps(payload),
                                    capture_output=True, timeout=20)
            if result.returncode != expected:
                fail(f"{command!r} exited {result.returncode}, expected {expected}\nstdout={result.stdout}\nstderr={result.stderr}")
            return result

        def focus(action: str, *, session: str = "session-amend", thread: str = "thread-root",
                  payload: object | None = None, extra: list[str] | None = None,
                  expected: int = 0) -> subprocess.CompletedProcess[str]:
            args = ["focus", action, "--project", str(project), "--state-dir", str(state),
                    "--session-id", session, "--thread-id", thread]
            if extra:
                args.extend(extra)
            return run(args, payload=payload, expected=expected)

        def hook(event: str, *, session: str = "session-amend", cwd: Path = project,
                 tool: str | None = None, tool_input: dict[str, object] | None = None,
                 permission: str | None = None, source: str | None = None,
                 stop_active: bool | None = None) -> dict[str, object]:
            payload: dict[str, object] = {"hook_event_name": event, "session_id": session, "cwd": str(cwd)}
            if tool is not None:
                payload["tool_name"] = tool
            if tool_input is not None:
                payload["tool_input"] = tool_input
            if permission is not None:
                payload["permission_mode"] = permission
                if event == "UserPromptSubmit":
                    # Native permission_mode is approval policy, not Plan.
                    # Use the real typed collaboration mode adapter here.
                    rollout = root / "mode.jsonl"
                    turn_id = "turn-" + permission
                    rollout.write_text(json.dumps({"type":"turn_context", "payload":{"turn_id":turn_id, "collaboration_mode":{"mode":"plan" if permission == "plan" else "default"}}}) + "\n")
                    payload.update({"turn_id":turn_id,"transcript_path":str(rollout),"permission_mode":"default"})
            if source is not None:
                payload["source"] = source
            if stop_active is not None:
                payload["stop_hook_active"] = stop_active
            result = run(["hook", "--project", str(project), "--state-dir", str(state)], payload=payload)
            try:
                return json.loads(result.stdout)
            except json.JSONDecodeError as exc:
                fail(f"hook returned invalid JSON: {result.stdout!r}: {exc}")

        begin_payload = {
            "goal": "finish the focused fixture",
            "mode": "implementing",
            "paths": ["src"],
            "excluded_paths": ["src/generated"],
            "criteria": [
                {"id": "check", "description": "the command check passes", "kind": "check"},
                {"id": "second-check", "description": "the second command check passes", "kind": "check"},
                {"id": "review", "description": "the result is reviewed", "kind": "review"},
            ],
        }

        # The first invocation installs the local connection and reports the
        # setup-only handoff. The second invocation persists the task.
        first = focus("begin", payload=begin_payload)
        first_json = json.loads(first.stdout)
        if first_json.get("setup_only") is not True:
            fail(f"expected setup-only handoff, got {first_json}")
        started = focus("begin", payload=begin_payload)
        task = json.loads(started.stdout)["focus"]
        if task["status"] != "active" or task.get("hook_observed"):
            fail(f"unexpected started focus: {task}")
        task_id = task["id"]

        # Supported native edits are quiet and flip the observable guard bit;
        # an off-scope destination receives the native deny envelope.
        if hook("PreToolUse", tool="apply_patch", tool_input={"command": "*** Update File: src/main.go\n"}):
            fail("supported in-scope edit was not quiet")
        status = json.loads(focus("status").stdout)["focus"]
        if not status.get("hook_observed"):
            fail(f"native guard did not move waiting -> observed: {status}")
        denied = hook("PreToolUse", tool="apply_patch", tool_input={"command": "*** Update File: docs/out.md\n"})
        deny = denied.get("hookSpecificOutput", {})
        if deny.get("permissionDecision") != "deny":
            fail(f"off-scope edit was not denied: {denied}")

        # An evidenced amendment makes the necessary component available and
        # announces the expansion through the CLI.
        amended = focus("amend", payload={"criterion_id": "review", "reason": "the completion review needs docs", "evidence": "review request", "paths": ["docs"]})
        amended_json = json.loads(amended.stdout)
        if amended_json.get("announce") is not True:
            fail(f"amendment was not announced: {amended_json}")
        amendment_notice = hook("PostToolUse", tool="apply_patch", tool_input={"command": "*** Update File: docs/out.md\n"})
        notice_output = amendment_notice.get("hookSpecificOutput", {})
        notice_text = notice_output.get("additionalContext", amendment_notice.get("systemMessage"))
        if not isinstance(notice_text, str) or not notice_text:
            fail(f"native amendment notice missing: {amendment_notice}")
        if hook("PostToolUse", tool="apply_patch", tool_input={"command": "*** Update File: docs/out.md\n"}):
            fail("native amendment notice repeated")
        if hook("PreToolUse", tool="apply_patch", tool_input={"command": "*** Update File: docs/out.md\n"}):
            fail("amended in-scope edit was not quiet")

        # A failed required check cannot be completed away.
        focus("begin", session="session-failed", payload=begin_payload)
        failed = focus("check", session="session-failed", extra=["--criterion", "check", "--", "false"], expected=1)
        if "required check failed" not in failed.stderr:
            fail(f"failed check did not explain failure: {failed.stderr}")
        failed_status = json.loads(focus("status", session="session-failed").stdout)["focus"]
        if failed_status["criteria"][0]["status"] != "fail":
            fail(f"failed check status missing: {failed_status}")

        # Stop is quiet until explicit completion intent, then blocks exactly
        # once for unmet criteria; stop_hook_active prevents a continuation loop.
        ordinary = hook("Stop", session="session-failed")
        if ordinary:
            fail(f"ordinary stop was treated as a completion request: {ordinary}")
        if focus("finish", session="session-failed", extra=["--result", "completed"], expected=1).returncode != 1:
            fail("failed check was incorrectly completed")
        blocked = hook("Stop", session="session-failed")
        if blocked.get("decision") != "block":
            fail(f"requested incomplete stop did not block: {blocked}")
        if hook("Stop", session="session-failed", stop_active=True):
            fail("active stop hook loop was not quiet")
        focus("cancel", session="session-failed")

        # A real check plus agent review satisfies both criteria and survives a
        # process restart before finish.
        focus("begin", session="session-good", payload=begin_payload)
        focus("check", session="session-good", extra=["--criterion", "check", "--", "true"])
        focus("check", session="session-good", extra=["--criterion", "second-check", "--", "true"])
        two_checks = json.loads(focus("status", session="session-good").stdout)["focus"]["criteria"]
        statuses = {criterion["id"]: criterion["status"] for criterion in two_checks}
        if statuses.get("check") != "pass" or statuses.get("second-check") != "pass":
            fail(f"second check did not preserve first check: {two_checks}")
        focus("review", session="session-good", payload={"criterion_id": "review", "evidence": "reviewed fixture output"})
        finished = focus("finish", session="session-good")
        finished_task = json.loads(finished.stdout)["focus"]
        if finished_task["status"] != "completed":
            fail(f"valid check/review did not finish: {finished_task}")
        reopened = json.loads(focus("status", session="session-good").stdout)["focus"]
        if reopened["status"] != "completed":
            fail(f"completed task was not durable: {reopened}")

        # Side work is queued locally and deduplicated without credentials or
        # a network call. Keep this before the deliberate Plan-mode transition
        # on the amendment task.
        defer_payload = {"component": "docs", "problem": "fixture setup", "title": "Fix fixture setup", "description": "Make setup deterministic", "criteria": ["fresh setup passes"], "evidence": ["black-box check"], "type_label": "bug", "area_label": "tooling"}
        first_defer = json.loads(focus("defer", session="session-amend", payload=defer_payload).stdout)
        second_defer = json.loads(focus("defer", session="session-amend", payload=defer_payload).stdout)
        if first_defer["item"]["id"] != second_defer["item"]["id"]:
            fail(f"deferred work was not deduplicated: {first_defer} / {second_defer}")
        if (state / "linear-oauth.json").exists():
            fail("black-box defer unexpectedly created credentials")

        # Plan mode is read-only at the native boundary, and the CLI rejects a
        # plan-mode begin before creating a task.
        hook("UserPromptSubmit", permission="plan")
        stale_default = hook("PreToolUse", permission="dontAsk", tool="apply_patch", tool_input={"command": "*** Update File: src/main.go\n"})
        if stale_default.get("hookSpecificOutput", {}).get("permissionDecision") != "deny":
            fail(f"stale PreToolUse default escaped persisted plan mode: {stale_default}")
        hook("UserPromptSubmit", permission="dontAsk")
        focus("begin", session="session-plan", payload={**begin_payload, "mode": "planning"}, expected=1)
        planned = hook("PreToolUse", tool="apply_patch", tool_input={"command": "*** Update File: src/main.go\n"}, permission="plan")
        if planned.get("hookSpecificOutput", {}).get("permissionDecision") != "deny":
            fail(f"plan-mode mutation was not denied: {planned}")
        hook("UserPromptSubmit", permission="dontAsk")

        # Foreign sessions and sibling worktrees never resolve this task.
        if hook("PreToolUse", session="foreign", tool="apply_patch", tool_input={"command": "*** Update File: src/main.go\n"}):
            fail("foreign session received a focus decision")
        sibling = root / "sibling worktree"
        sibling.mkdir()
        if hook("PreToolUse", cwd=sibling, tool="apply_patch", tool_input={"command": "*** Update File: src/main.go\n"}):
            fail("sibling cwd received a focus decision")

        print("PASS: setup handoff, native scope guards, observed state, evidenced amendment, failed/valid checks, one-shot stop gate, plan mode, session/worktree isolation, durable completion, and credential-free deferred dedupe.")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", help="use an existing Sparestep binary instead of building one")
    options = parser.parse_args()
    try:
        main(options.binary)
    except (AssertionError, subprocess.CalledProcessError) as exc:
        print(f"FAIL: {exc}", file=sys.stderr)
        raise SystemExit(1)
