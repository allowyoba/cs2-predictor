"""Exercise the notify_admins role directly; curl is stubbed."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest


class NotifyAdminsTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        self.app = self.root / "app"
        self.app.mkdir()
        self.bin = self.root / "bin"
        self.bin.mkdir()
        self.log = self.root / "curl.log"
        curl = self.bin / "curl"
        curl.write_text('''#!/bin/sh
printf '%s\\n' "$*" >> "$MOCK_CURL_LOG"
''')
        curl.chmod(0o755)
        self.env = dict(os.environ, PATH=str(self.bin) + ":" + os.environ["PATH"],
                        ANSIBLE_ROLES_PATH="/src/ansible/roles",
                        MOCK_CURL_LOG=str(self.log))
        self.env.pop("DEPLOY_NOTIFY_CHAT_IDS", None)
        self.inventory = self.root / "inventory.json"
        self.inventory.write_text(json.dumps({"production": {"hosts": {
            "localhost": {"ansible_connection": "local", "ansible_python_interpreter": "/usr/local/bin/python"}
        }}}))
        self.playbook = self.root / "test.yml"
        self.playbook.write_text(json.dumps([{
            "hosts": "production", "gather_facts": False, "roles": ["notify_admins"],
        }]))

    def tearDown(self):
        self.temp.cleanup()

    def run_notify(self, app_dir, message="Deploy v1.0.1 failed", chat_ids_env=None):
        # DEPLOY_NOTIFY_CHAT_IDS is a GitHub environment variable in
        # production, not part of the server .env — exercised the same
        # way here, through the process environment.
        env = dict(self.env)
        if chat_ids_env is not None:
            env["DEPLOY_NOTIFY_CHAT_IDS"] = chat_ids_env
        return subprocess.run([
            "ansible-playbook", "-i", str(self.inventory), str(self.playbook),
            "--extra-vars", json.dumps({"app_dir": str(app_dir), "notify_admins_message": message}),
        ], env=env, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)

    def test_missing_message_fails(self):
        result = self.run_notify(self.app, message="")
        self.assertNotEqual(result.returncode, 0, result.stdout)

    def test_no_configured_admins_is_a_quiet_noop_without_env_file(self):
        # No DEPLOY_NOTIFY_CHAT_IDS at all means the role never even
        # looks for the server .env — safe on a VM that isn't fully
        # provisioned yet.
        result = self.run_notify(self.app)
        self.assertEqual(result.returncode, 0, result.stdout)
        self.assertFalse(self.log.exists() and self.log.read_text().strip())

    def test_missing_env_file_fails_when_admins_are_configured(self):
        result = self.run_notify(self.app, chat_ids_env="111")
        self.assertNotEqual(result.returncode, 0, result.stdout)

    def test_sends_a_dm_to_every_configured_admin(self):
        # A space after the comma is fine — each ID is trimmed.
        (self.app / ".env").write_text("TELEGRAM_BOT_TOKEN=test-token-123\n")
        result = self.run_notify(self.app, message="Deploy v1.0.1 failed: https://example/run/1",
                                  chat_ids_env="111, 222")
        self.assertEqual(result.returncode, 0, result.stdout)
        self.assertNotIn("test-token-123", result.stdout)
        calls = self.log.read_text().splitlines()
        self.assertEqual(len(calls), 2)
        self.assertTrue(any("chat_id=111" in c for c in calls))
        self.assertTrue(any("chat_id=222" in c for c in calls))
        for c in calls:
            self.assertIn("https://api.telegram.org/bottest-token-123/sendMessage", c)
            self.assertIn("Deploy v1.0.1 failed: https://example/run/1", c)

    def test_no_configured_admins_is_a_quiet_noop(self):
        (self.app / ".env").write_text("TELEGRAM_BOT_TOKEN=test-token-123\n")
        result = self.run_notify(self.app)
        self.assertEqual(result.returncode, 0, result.stdout)
        self.assertFalse(self.log.exists() and self.log.read_text().strip())


if __name__ == "__main__":
    unittest.main()
