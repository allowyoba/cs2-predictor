"""Run in an isolated Linux container as root; Docker commands are stubbed."""
import gzip
import json
import os
from pathlib import Path
import subprocess
import tarfile
import tempfile
import time
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
        # MOCK_FAIL_DB_BACKUP: "exec ... pg_dump" fails when set, so the
        # database_backup role's failure path can be exercised without a
        # real Postgres. Otherwise "exec ... pg_dump" writes deterministic
        # fake dump bytes to stdout, so the compressed file that lands in
        # the backup destination is non-empty and inspectable.
        docker.write_text('''#!/bin/sh
printf '%s\\n' "$*" >> "$MOCK_DOCKER_LOG"
# Real compose.prod.yml declares the bot service as `image: ${APP_IMAGE:?required}`
# — real Compose refuses to interpolate the file at all without it, for
# *any* subcommand that touches the project (exec/config/ps included, not
# just up). Mirrored here so a task that forgets to set APP_IMAGE in its
# own environment (as database_backup's pg_dump exec once did) fails the
# same way in this stub as it would for real, instead of the stub silently
# succeeding regardless.
case "$*" in
  *--project-directory*)
    if [ -z "${APP_IMAGE:-}" ]; then
      echo "error while interpolating services.bot.image: required variable APP_IMAGE is missing a value: required" >&2
      exit 1
    fi
    ;;
esac
case "$*" in
  login*) cat >/dev/null ;;
  *" up "*)
    if [ -n "${MOCK_FAIL_UP_IMAGE:-}" ] && [ "${APP_IMAGE:-}" = "$MOCK_FAIL_UP_IMAGE" ]; then
      exit 1
    fi
    ;;
  *pg_dump*)
    if [ -n "${MOCK_FAIL_DB_BACKUP:-}" ]; then
      exit 1
    fi
    printf 'fake-database-dump'
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
            "DEPLOY_NOTIFY_CHAT_IDS": "",
            "VALVE_VRS_ENABLED": "",
            "VALVE_VRS_SYNC_INTERVAL": "",
            "GRID_ENABLED": "",
            "GRID_API_KEY": "",
            "GRID_SYNC_INTERVAL": "",
            "LIQUIPEDIA_ENABLED": "",
            "LIQUIPEDIA_API_KEY": "",
            "LIQUIPEDIA_SYNC_INTERVAL": "",
            "HLTV_ENABLED": "",
            "APIFY_TOKEN": "",
            "APIFY_RANKING_CHECK_INTERVAL": "",
            "APIFY_MAX_TEAMS": "",
            "EVENT_EVE_LEAD": "",
            "DATABASE_POOL_SIZE": "",
            # Defaulted from CADDY_DOMAIN rather than left blank: the Mini
            # App is served from the same host, and a deployment that hosts
            # it should not have to name the address twice.
            "MINIAPP_URL": "https://bot.example.test/app/",
            # Machine-resource alert levels: unset here, so the bot keeps
            # its own defaults rather than the deploy inventing numbers.
            "HOST_ALERT_MEMORY": "",
            "HOST_ALERT_DISK": "",
            "HOST_ALERT_LOAD_PER_CPU": "",
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
            # An empty value (DEPLOY_NOTIFY_CHAT_IDS when unset, as in this
            # fixture) can't leak anything and would trivially "match" any
            # string, which is not what this check is for.
            if value:
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

    def write_previous_deployment(self):
        # A previous deployment.json is what makes the database_backup
        # role treat this as "not a first install" — see
        # application_deploy_previous_raw in application_deploy/tasks.
        (self.app / "deployment.json").write_text(json.dumps({"tag": "v1.0.0", "image": self.previous_image}))

    def db_backup_dir(self):
        return self.app / "backups" / "database"

    def test_database_backup_skipped_on_a_first_install(self):
        # setUp leaves no deployment.json, so there's genuinely nothing to
        # back up yet.
        result = self.run_deploy()
        self.assertEqual(result.returncode, 0, result.stdout)
        self.assertFalse(self.db_backup_dir().exists())

    def test_database_backup_defaults_to_the_filesystem(self):
        self.write_previous_deployment()
        result = self.run_deploy()
        self.assertEqual(result.returncode, 0, result.stdout)
        dumps = list(self.db_backup_dir().glob("*.dump.gz"))
        self.assertEqual(len(dumps), 1, result.stdout)
        with gzip.open(dumps[0]) as f:
            self.assertEqual(f.read(), b"fake-database-dump")
        self.assertEqual(dumps[0].stat().st_mode & 0o777, 0o600)

    def test_database_backup_prunes_files_beyond_the_retention_count(self):
        self.write_previous_deployment()
        # Set explicitly rather than relying on the default, so this test
        # doesn't need updating every time the default itself changes.
        self.env["DB_BACKUP_RETENTION_COUNT"] = "5"
        backup_dir = self.db_backup_dir()
        backup_dir.mkdir(parents=True)
        # 5 pre-existing backups (the configured retention count) plus the
        # one this deploy creates itself must leave exactly 5: the newest
        # 5, i.e. everything except the very oldest pre-existing file.
        oldest = backup_dir / "db-v0.9.0-20000101T000000Z.dump.gz"
        names = [
            "db-v0.9.0-20000101T000000Z.dump.gz",
            "db-v0.9.1-20000102T000000Z.dump.gz",
            "db-v0.9.2-20000103T000000Z.dump.gz",
            "db-v0.9.3-20000104T000000Z.dump.gz",
            "db-v0.9.4-20000105T000000Z.dump.gz",
        ]
        for i, name in enumerate(names):
            f = backup_dir / name
            f.write_bytes(gzip.compress(b"old-dump"))
            t = time.time() - ((len(names) - i) * 86400)
            os.utime(f, (t, t))
        result = self.run_deploy()
        self.assertEqual(result.returncode, 0, result.stdout)
        self.assertFalse(oldest.exists())
        self.assertEqual(len(list(backup_dir.glob("*.dump.gz"))), 5)

    def test_database_backup_failure_blocks_the_deploy(self):
        self.write_previous_deployment()
        self.env["MOCK_FAIL_DB_BACKUP"] = "1"
        result = self.run_deploy()
        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertFalse(self.lock.exists())
        self.assertNotIn("pull", (self.root / "docker.log").read_text())
        self.assertEqual((self.app / ".env").read_text(), "SECRET=do-not-log-this\n")

    def test_database_backup_can_be_disabled(self):
        self.write_previous_deployment()
        self.env["DB_BACKUP_ENABLED"] = "false"
        result = self.run_deploy()
        self.assertEqual(result.returncode, 0, result.stdout)
        self.assertFalse(self.db_backup_dir().exists())

    def test_database_backup_uploads_to_s3_with_yandex_cloud_defaults(self):
        self.write_previous_deployment()
        self.env.update({
            "DB_BACKUP_STORAGE": "s3",
            "DB_BACKUP_S3_BUCKET": "cs2predictor-backups",
            "DB_BACKUP_S3_ACCESS_KEY_ID": "AKIDEXAMPLE",
            "DB_BACKUP_S3_SECRET_ACCESS_KEY": "s3cr3t",
        })
        result = self.run_deploy()
        self.assertEqual(result.returncode, 0, result.stdout)
        self.assertFalse(self.db_backup_dir().exists())
        # The S3 steps run through the official AWS CLI image via `docker
        # run` (no `aws` binary is ever installed on the host — see
        # database_backup/tasks/main.yml's own comment for why), so their
        # invocations land in docker.log, not a separate aws-specific log.
        docker_log = (self.root / "docker.log").read_text()
        self.assertIn("amazon/aws-cli", docker_log)
        self.assertIn("https://storage.yandexcloud.net", docker_log)
        self.assertIn("s3 cp", docker_log)
        self.assertIn("s3://cs2predictor-backups/database/", docker_log)
        # Retention is enforced by listing objects under the prefix and
        # deleting everything but the newest N (a count cap, not a bucket
        # lifecycle rule) — the mock docker never actually runs the
        # container, so the listing comes back empty and nothing is
        # deleted, but the listing call itself must still happen.
        self.assertIn("list-objects-v2", docker_log)
        self.assertIn("sort_by(Contents", docker_log)
        self.assertNotIn("s3cr3t", result.stdout)

    def test_database_backup_to_s3_requires_credentials(self):
        self.write_previous_deployment()
        self.env["DB_BACKUP_STORAGE"] = "s3"
        result = self.run_deploy()
        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertIn("DB_BACKUP_S3_BUCKET", result.stdout)
        self.assertFalse(self.lock.exists())


if __name__ == "__main__":
    unittest.main()
