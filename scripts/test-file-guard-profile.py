import importlib.util
import unittest
import tomllib
from pathlib import Path

spec = importlib.util.spec_from_file_location("guard_profile", Path(__file__).with_name("install-file-guard.py"))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class ProfileTests(unittest.TestCase):
    def test_migration_preserves_settings_and_is_idempotent(self):
        before = 'approval_policy = "never"\nsandbox_mode = "danger-full-access"\n[features]\nexample = true\n'
        after = module.profile_config(before)
        self.assertEqual(module.profile_config(after), after)
        config = tomllib.loads(after)
        self.assertEqual(config["approval_policy"], "never")
        self.assertTrue(config["features"]["example"])
        self.assertNotIn("sandbox_mode", config)
        permissions = config["permissions"]["agent-file-guard"]
        self.assertEqual(permissions["filesystem"][":root"], "write")
        self.assertEqual(permissions["filesystem"]["~/.Trash"], "deny")
        self.assertEqual(permissions["filesystem"]["/**/.[Tt][Rr][Aa][Ss][Hh]/**"], "deny")
        self.assertTrue(permissions["network"]["enabled"])

    def test_existing_workspace_policy_is_not_silently_discarded(self):
        with self.assertRaises(ValueError):
            module.profile_config('[sandbox_workspace_write]\nnetwork_access = true\n')


if __name__ == "__main__":
    unittest.main()
