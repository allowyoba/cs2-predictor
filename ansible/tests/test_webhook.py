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
        # getent is stubbed so the tests never depend on real DNS:
        # MOCK_RESOLVES lists the families this domain has an address in.
        getent = self.bin / "getent"
        getent.write_text('''#!/bin/sh
case ":$MOCK_RESOLVES:" in
  *":$1:"*) echo "$2 resolved" ;;
  *) exit 2 ;;
esac
''')
        getent.chmod(0o755)
        self.env = dict(os.environ, PATH=str(self.bin) + ":" + os.environ["PATH"],
                        ANSIBLE_ROLES_PATH="/src/ansible/roles",
                        MOCK_CURL_LOG=str(self.log), MOCK_CURL_REPLY=str(self.reply),
                        MOCK_RESOLVES="ahostsv4:ahostsv6")
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

        (self.app / ".env").write_text(
            "TELEGRAM_BOT_TOKEN=test-token-123\n"
            "TELEGRAM_WEBHOOK_SECRET=abcdefghijklmnop\n"
            "CADDY_DOMAIN=bot.example.com\n")
        self.reply.write_text('{"ok":false,"error_code":401,"description":"Unauthorized"}')
        result = self.run_webhook(self.app)
        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertNotIn("test-token-123", result.stdout)
        self.assertNotIn("Unauthorized", result.stdout)


    def test_ipv6_only_domain_is_refused_with_an_explanation(self):
        # Telegram delivers webhooks over IPv4 only, so an AAAA-only
        # domain — an IPv6 nip.io name, for instance — registers fine and
        # then silently receives nothing. Catch it here instead.
        (self.app / ".env").write_text(
            "TELEGRAM_BOT_TOKEN=test-token-123\n"
            "TELEGRAM_WEBHOOK_SECRET=abcdefghijklmnop\n"
            "CADDY_DOMAIN=2001-db8--1.nip.io\n")
        self.reply.write_text('{"ok":true,"result":true}')
        self.env["MOCK_RESOLVES"] = "ahostsv6"
        result = self.run_webhook(self.app)
        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertIn("IPv6 only", result.stdout)
        self.assertFalse(self.log.exists(), "setWebhook must not be called for an unreachable domain")

    def test_domain_that_does_not_resolve_is_refused(self):
        (self.app / ".env").write_text(
            "TELEGRAM_BOT_TOKEN=test-token-123\n"
            "TELEGRAM_WEBHOOK_SECRET=abcdefghijklmnop\n"
            "CADDY_DOMAIN=bot.example.com\n")
        self.reply.write_text('{"ok":true,"result":true}')
        self.env["MOCK_RESOLVES"] = ""
        result = self.run_webhook(self.app)
        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertIn("does not resolve", result.stdout)

    def test_dual_stack_domain_registers(self):
        # An IPv6 address alongside an IPv4 one is fine: Telegram uses the
        # IPv4 one, and everything else about the host can stay dual-stack.
        (self.app / ".env").write_text(
            "TELEGRAM_BOT_TOKEN=test-token-123\n"
            "TELEGRAM_WEBHOOK_SECRET=abcdefghijklmnop\n"
            "CADDY_DOMAIN=bot.example.com\n")
        self.reply.write_text('{"ok":true,"result":true}')
        self.env["MOCK_RESOLVES"] = "ahostsv4:ahostsv6"
        result = self.run_webhook(self.app)
        self.assertEqual(result.returncode, 0, result.stdout)
        self.assertIn("url=https://bot.example.com/telegram/webhook", self.log.read_text())


if __name__ == "__main__":
    unittest.main()
