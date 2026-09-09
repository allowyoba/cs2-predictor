"""Exercise the register_webhook role directly; curl is stubbed."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest


class WebhookTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        self.app = self.root / "app"
        self.app.mkdir()
        self.bin = self.root / "bin"
        self.bin.mkdir()
        self.log = self.root / "curl.log"
        self.reply = self.root / "curl-reply.json"
        curl = self.bin / "curl"
        curl.write_text('''#!/bin/sh
printf '%s\\n' "$*" >> "$MOCK_CURL_LOG"
cat "$MOCK_CURL_REPLY"
''')
        curl.chmod(0o755)
        self.env = dict(os.environ, PATH=str(self.bin) + ":" + os.environ["PATH"],
                        ANSIBLE_ROLES_PATH="/src/ansible/roles",
                        MOCK_CURL_LOG=str(self.log), MOCK_CURL_REPLY=str(self.reply))
        self.inventory = self.root / "inventory.json"
        self.inventory.write_text(json.dumps({"production": {"hosts": {
            "localhost": {"ansible_connection": "local", "ansible_python_interpreter": "/usr/local/bin/python"}
        }}}))
        self.playbook = self.root / "test.yml"
        self.playbook.write_text(json.dumps([{
            "hosts": "production", "gather_facts": False, "roles": ["register_webhook"],
        }]))

    def tearDown(self):
        self.temp.cleanup()

    def run_webhook(self, app_dir):
        return subprocess.run([
            "ansible-playbook", "-i", str(self.inventory), str(self.playbook),
            "--extra-vars", json.dumps({"app_dir": str(app_dir)}),
        ], env=self.env, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)

    def test_missing_env_file_fails(self):
        result = self.run_webhook(self.app)
        self.assertNotEqual(result.returncode, 0, result.stdout)

    def test_success_calls_telegram_with_env_secrets(self):
        (self.app / ".env").write_text(
            "TELEGRAM_BOT_TOKEN=test-token-123\n"
            "TELEGRAM_WEBHOOK_SECRET=abcdefghijklmnop\n"
            "CADDY_DOMAIN=bot.example.com\n")
        self.reply.write_text('{"ok":true,"result":true}')
        result = self.run_webhook(self.app)
        self.assertEqual(result.returncode, 0, result.stdout)
        self.assertNotIn("test-token-123", result.stdout)
        self.assertNotIn("abcdefghijklmnop", result.stdout)
        call = self.log.read_text()
        self.assertIn("https://api.telegram.org/bottest-token-123/setWebhook", call)
        self.assertIn("url=https://bot.example.com/telegram/webhook", call)
        self.assertIn("secret_token=abcdefghijklmnop", call)

    def test_telegram_rejection_fails_without_leaking_response(self):
        (self.app / ".env").write_text(
            "TELEGRAM_BOT_TOKEN=test-token-123\n"
            "TELEGRAM_WEBHOOK_SECRET=abcdefghijklmnop\n"
            "CADDY_DOMAIN=bot.example.com\n")
        self.reply.write_text('{"ok":false,"error_code":401,"description":"Unauthorized"}')
        result = self.run_webhook(self.app)
        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertNotIn("test-token-123", result.stdout)
        self.assertNotIn("Unauthorized", result.stdout)


if __name__ == "__main__":
    unittest.main()
