#!/usr/bin/env python3
"""Test the maintenance installer without loading or running a job."""

import importlib.util
import json
import os
from pathlib import Path
import plistlib
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

sys.dont_write_bytecode = True


ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location("trash_maintenance_installer", ROOT / "scripts" / "install-trash-maintenance.py")
INSTALLER = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(INSTALLER)


class InstallerTests(unittest.TestCase):
    def temp_dir(self):
        if os.environ.get("SHIP_IT_KEEP_TEST_DIRS"):
            return Path(tempfile.mkdtemp(prefix="trash-maintenance-test-"))
        handle = tempfile.TemporaryDirectory(prefix="trash-maintenance-test-")
        self.addCleanup(handle.cleanup)
        return Path(handle.name)

    def test_job_is_weekly_and_host_owned(self):
        home = Path("/Users/example")
        definition = INSTALLER.job(home)
        self.assertEqual(definition["ProgramArguments"], ["/Users/example/.local/libexec/trash-maintenance", "--run"])
        self.assertEqual(definition["StartCalendarInterval"], {"Weekday": 0, "Hour": 3, "Minute": 15})
        self.assertTrue(definition["RunAtLoad"])
        self.assertEqual(definition["WatchPaths"], ["/Users/example/.Trash"])
        self.assertEqual(definition["ThrottleInterval"], 300)
        self.assertNotIn("KeepAlive", definition)
        self.assertNotIn("QueueDirectories", definition)

    def test_generated_plist_lints(self):
        fixture = self.temp_dir()
        definition = fixture / "job.plist"
        definition.write_bytes(plistlib.dumps(INSTALLER.job(fixture / "home")))
        result = subprocess.run(["plutil", "-lint", str(definition)], capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_prepare_never_calls_launchctl_or_cleanup(self):
        fixture = self.temp_dir()
        calls = []

        def fake_run(command, **kwargs):
            calls.append(command)
            return mock.Mock(returncode=0)

        with mock.patch.object(INSTALLER.subprocess, "run", side_effect=fake_run):
            binary, definition = INSTALLER.prepare(fixture, fixture / "home")
        self.assertEqual(binary, fixture / "trash-maintenance")
        self.assertTrue(definition.exists())
        self.assertFalse(any(Path(command[0]).name == "launchctl" for command in calls))
        self.assertFalse(any("rmtree" in json.dumps(command) for command in calls))

    def test_install_is_rejected_in_codex_session_before_effects(self):
        env = os.environ.copy()
        env["CODEX_THREAD_ID"] = "test-thread"
        result = subprocess.run([sys.executable, str(ROOT / "scripts" / "install-trash-maintenance.py"), "--install"], env=env, capture_output=True, text=True)
        self.assertEqual(result.returncode, 2)
        self.assertIn("normal macOS Terminal", result.stderr)
        self.assertNotIn("launchctl", result.stderr)


if __name__ == "__main__":
    unittest.main()
