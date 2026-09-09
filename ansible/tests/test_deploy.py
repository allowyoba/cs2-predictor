"""Run in an isolated Linux container as root; Docker commands are stubbed."""
import json
import os
from pathlib import Path
import subprocess
import tarfile
import tempfile
import unittest


class DeploymentTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        self.app = self.root / "app"
        self.app.mkdir()
        (self.app / ".env").write_text("SECRET=do-not-log-this\n")
        (self.app / ".env").chmod(0o600)
        (self.app / "docker").mkdir()
        (self.app / "docker" / "Caddyfile").write_text("previous config\n")
        self.backups = self.root / "backups"
        self.lock = self.root / "lock"
        self.bin = self.root / "bin"
        self.bin.mkdir()
        docker = self.bin / "docker"
        # MOCK_FAIL_UP_IMAGE: "up" fails only when APP_IMAGE matches it, so
        # a rollback to a different image can still succeed.
        docker.write_text('''#!/bin/sh
printf '%s\\n' "$*" >> "$MOCK_DOCKER_LOG"
case "$*" in
  login*) cat >/dev/null ;;
  *" up "*)
    if [ -n "${MOCK_FAIL_UP_IMAGE:-}" ] && [ "${APP_IMAGE:-}" = "$MOCK_FAIL_UP_IMAGE" ]; then
      exit 1
    fi
    ;;
esac
''')
        docker.chmod(0o755)
        self.new_image = "ghcr.io/example/app@sha256:" + "a" * 64
        self.previous_image = "ghcr.io/example/app@sha256:" + "b" * 64
        self.new_env_values = {
            "POSTGRES_PASSWORD": "fresh-pg-password",
            "TELEGRAM_BOT_TOKEN": "fresh-bot-token",
            "TELEGRAM_WEBHOOK_SECRET": "fresh-webhook-secret",
            "PANDASCORE_TOKEN": "fresh-pandascore-token",
            "CADDY_DOMAIN": "bot.example.test",
        }
        self.new_env_file = "".join(f"{k}={v}\n" for k, v in self.new_env_values.items())
        self.env = dict(os.environ, PATH=str(self.bin) + ":" + os.environ["PATH"],
                        REGISTRY_USER="test", REGISTRY_TOKEN="private-token",
                        MOCK_DOCKER_LOG=str(self.root / "docker.log"), **self.new_env_values)
        self.vars = {
            "app_dir": str(self.app), "app_user": "root",
            "backup_root": str(self.backups), "deploy_lock": str(self.lock),
            "release_dir": "/src", "release_tag": "v1.0.1",
            "app_image": self.new_image,
            "ansible_become": False,
        }
        self.inventory = self.root / "inventory.json"
        self.inventory.write_text(json.dumps({"production": {"hosts": {
            "localhost": {"ansible_connection": "local", "ansible_python_interpreter": "/usr/local/bin/python"}
        }}}))

    def tearDown(self):
        self.temp.cleanup()

    def run_deploy(self):
        result = subprocess.run([
            "ansible-playbook", "-i", str(self.inventory), "/src/ansible/deploy.yml",
            "--extra-vars", json.dumps(self.vars),
        ], env=self.env, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
        self.assertNotIn("do-not-log-this", result.stdout)
        self.assertNotIn("private-token", result.stdout)
        for value in self.new_env_values.values():
            self.assertNotIn(value, result.stdout)
        return result

    def assert_backup(self):
        archives = list(self.backups.glob("*/configuration.tar.gz"))
        self.assertEqual(len(archives), 1)
        archive = archives[0]
        self.assertEqual(archive.stat().st_mode & 0o777, 0o600)
        self.assertEqual(archive.parent.stat().st_mode & 0o777, 0o700)
        with tarfile.open(archive) as backup:
            prefix = str(self.app).lstrip("/")
            self.assertEqual(backup.extractfile(prefix + "/docker/Caddyfile").read(), b"previous config\n")
            self.assertEqual(backup.extractfile(prefix + "/.env").read(), b"SECRET=do-not-log-this\n")

    def test_success_backs_up_old_configuration(self):
        result = self.run_deploy()
        self.assertEqual(result.returncode, 0, result.stdout)
        self.assert_backup()
        self.assertFalse(self.lock.exists())
        self.assertEqual(json.loads((self.app / "deployment.json").read_text())["tag"], "v1.0.1")
        # The old .env was backed up above; the live file now reflects
        # this deploy's APP_ENV_FILE secret.
        self.assertEqual((self.app / ".env").read_text(), self.new_env_file)
        self.assertEqual((self.app / ".env").stat().st_mode & 0o777, 0o600)

    def test_env_file_is_created_from_scratch_on_a_first_install(self):
        (self.app / ".env").unlink()
        result = self.run_deploy()
        self.assertEqual(result.returncode, 0, result.stdout)
        self.assertEqual((self.app / ".env").read_text(), self.new_env_file)

    def test_missing_env_value_fails_before_anything_changes(self):
        # Any single one missing is enough — not just all of them.
        del self.env["TELEGRAM_BOT_TOKEN"]
        result = self.run_deploy()
        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertFalse(self.backups.exists())
        self.assertEqual((self.app / ".env").read_text(), "SECRET=do-not-log-this\n")

    def test_backup_failure_prevents_configuration_changes(self):
        self.backups.write_text("not a directory")
        result = self.run_deploy()
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual((self.app / "docker/Caddyfile").read_text(), "previous config\n")
        self.assertEqual((self.app / ".env").read_text(), "SECRET=do-not-log-this\n")
        self.assertNotIn("pull", (self.root / "docker.log").read_text())
        self.assertFalse(self.lock.exists())

    def test_failed_start_with_no_previous_release_keeps_backup_and_record(self):
        # No "image" field — an older format, or a first install — must
        # not crash before the lock/backup even runs.
        (self.app / "deployment.json").write_text('{"tag":"v1.0.0"}')
        self.env["MOCK_FAIL_UP_IMAGE"] = self.new_image
        result = self.run_deploy()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("No previous release was recorded", result.stdout)
        self.assert_backup()
        self.assertEqual(json.loads((self.app / "deployment.json").read_text())["tag"], "v1.0.0")
        self.assertFalse(self.lock.exists())

    def test_failed_health_check_rolls_back_to_previous_release(self):
        (self.app / "deployment.json").write_text(json.dumps({"tag": "v1.0.0", "image": self.previous_image}))
        self.env["MOCK_FAIL_UP_IMAGE"] = self.new_image
        result = self.run_deploy()
        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertIn(f"Rolled back to the previously running release ({self.previous_image})", result.stdout)
        self.assert_backup()
        # Must reflect v1.0.0/previous_image again, not the failed attempt.
        restored = json.loads((self.app / "deployment.json").read_text())
        self.assertEqual(restored["tag"], "v1.0.0")
        self.assertEqual(restored["image"], self.previous_image)
        self.assertFalse(self.lock.exists())
        calls = (self.root / "docker.log").read_text()
        self.assertIn(f"pull {self.previous_image}", calls)
        # The new APP_ENV_FILE might be why the health check failed —
        # roll it back alongside the image, not just deployment.json.
        self.assertEqual((self.app / ".env").read_text(), "SECRET=do-not-log-this\n")

    def test_failed_health_check_and_failed_rollback_both_reported(self):
        (self.app / "deployment.json").write_text(json.dumps({"tag": "v1.0.0", "image": self.previous_image}))
        # Both images fail "up": a broken host, not just a bad release.
        docker = self.bin / "docker"
        docker.write_text('''#!/bin/sh
printf '%s\\n' "$*" >> "$MOCK_DOCKER_LOG"
case "$*" in
  login*) cat >/dev/null ;;
  *" up "*) exit 1 ;;
esac
''')
        docker.chmod(0o755)
        result = self.run_deploy()
        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertIn("Rollback to the previous release ALSO failed", result.stdout)
        self.assertFalse(self.lock.exists())

    def test_existing_lock_is_preserved(self):
        self.lock.mkdir()
        result = self.run_deploy()
        self.assertNotEqual(result.returncode, 0)
        self.assertTrue(self.lock.exists())
        self.assertFalse(self.backups.exists())


if __name__ == "__main__":
    unittest.main()
