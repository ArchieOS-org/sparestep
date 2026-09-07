#!/usr/bin/env python3
"""Exercise the real OpenSSH configuration parser without opening connections."""

import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("installer", ROOT / "scripts/install-fleet-ssh-guard.py")
installer = importlib.util.module_from_spec(spec)
spec.loader.exec_module(installer)


class InstallationTest(unittest.TestCase):
    def test_install_preserves_other_hosts_and_is_idempotent(self):
        home = Path(tempfile.mkdtemp(prefix="fleet ssh install "))
        config = home / ".ssh/config"
        config.parent.mkdir()
        original = ("Host unrelated.example\n  Port 2222\n  User retained\n"
                    "Host boompay-control-01\n  HostName 142.93.158.204\n  User boompay\n"
                    "Host cisl2-control-01\n  HostName 146.190.242.198\n  User cisl2\n"
                    "Host *\n  ConnectTimeout 10\n")
        config.write_text(original)
        before = installer.settings(config, "unrelated.example")
        installer.install(home)
        first = config.read_bytes()
        installer.install(home)
        self.assertEqual(config.read_bytes(), first)
        self.assertEqual((config.parent / "config.before-fleet-ssh-guard").read_text(), original)
        after = installer.settings(config, "unrelated.example")
        for key in ("hostname", "port", "user", "connecttimeout", "proxycommand", "localcommand"):
            self.assertEqual(before.get(key), after.get(key), key)
        policy = json.loads((ROOT / "fleet-ssh-policy.json").read_text())
        for host in list(policy["targets"]) + [name for aliases in policy["targets"].values() for name in aliases]:
            effective = installer.settings(config, host)
            self.assertIn("fleet-ssh-guard.py", effective["proxycommand"])
            self.assertIn("authenticated %h %p", effective["localcommand"])
            self.assertEqual(effective["connectionattempts"], "1")
            self.assertEqual(effective["preferredauthentications"], "publickey")

    def test_earlier_includes_cannot_override_guard(self):
        home = Path(tempfile.mkdtemp(prefix="fleet-ssh-order-"))
        config = home / ".ssh/config"
        config.parent.mkdir()
        other = home / ".ssh/other.conf"
        other.write_text("Host *\n  ConnectionAttempts 8\n  ProxyCommand false\n")
        config.write_text(f'Include "{other}"\n')
        installer.install(home)
        effective = installer.settings(config, "142.93.158.204")
        self.assertEqual(effective["connectionattempts"], "1")
        self.assertIn("fleet-ssh-guard.py", effective["proxycommand"])
        self.assertEqual(installer.settings(config, "unrelated.example")["proxycommand"], "false")

    def test_invalid_config_does_not_replace_installed_files(self):
        home = Path(tempfile.mkdtemp(prefix="fleet-ssh-invalid-"))
        installer.install(home)
        files = [home / ".local/libexec/fleet-ssh-guard.py",
                 home / ".config/fleet-ssh-guard/policy.json",
                 home / ".ssh/fleet-ssh-guard.conf"]
        before = [path.read_bytes() for path in files]
        config = home / ".ssh/config"
        config.write_text(config.read_text() + "UnknownBrokenSSHOption yes\n")
        with self.assertRaises(Exception):
            installer.install(home)
        self.assertEqual([path.read_bytes() for path in files], before)

    def test_symlink_config_is_not_replaced(self):
        home = Path(tempfile.mkdtemp(prefix="fleet-ssh-link-"))
        (home / ".ssh").mkdir()
        original = home / "original"
        original.write_text("Host *\n")
        (home / ".ssh/config").symlink_to(original)
        with self.assertRaisesRegex(RuntimeError, "owned and regular"):
            installer.install(home)
        self.assertTrue((home / ".ssh/config").is_symlink())


if __name__ == "__main__":
    unittest.main()
