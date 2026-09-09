"""Run real sshd and sudo inside the test container, never on the developer host."""
import json
import os
from pathlib import Path
import socket
import subprocess
import tempfile
import time
import unittest


class HostAccessTest(unittest.TestCase):
    def run_command(self, argv, **kwargs):
        return subprocess.run(argv, text=True, stdout=subprocess.PIPE,
                              stderr=subprocess.STDOUT, **kwargs)

    def require_success(self, argv, **kwargs):
        result = self.run_command(argv, **kwargs)
        self.assertEqual(result.returncode, 0, result.stdout)
        return result

    def wait_for_ssh(self):
        for _ in range(50):
            try:
                with socket.create_connection(("127.0.0.1", 22), timeout=0.2):
                    return
            except OSError:
                time.sleep(0.1)
        self.fail("sshd did not resume listening after reload")

    def test_migration_real_ssh_then_deployment(self):
        # Reproduces the pre-migration setup: "admin" has unrestricted
        # root sudo; "cs2deploy" is locked to a single fixed command.
        for user in ["admin", "cs2deploy"]:
            self.require_success(["useradd", "-m", "-s", "/bin/bash", user])
        Path("/run/sshd").mkdir(exist_ok=True)
        self.require_success(["ssh-keygen", "-A"])
        Path("/etc/ssh/sshd_config").write_text(
            "Include /etc/ssh/sshd_config.d/*.conf\nPort 22\nUsePAM yes\n"
            "PasswordAuthentication no\nPermitRootLogin no\n"
            "Subsystem sftp internal-sftp\n")
        managed = Path("/etc/ssh/sshd_config.d/00-00-cs2predictor-hardening.conf")
        original = ("AllowUsers admin cs2deploy\nMatch User cs2deploy\n"
                    "    ForceCommand /usr/local/sbin/cs2predictor-ssh-command\nMatch all\n")
        managed.write_text(original)
        legacy_wrapper = Path("/usr/local/sbin/cs2predictor-ssh-command")
        legacy_wrapper.write_text("#!/bin/sh\necho restricted-legacy-account\nexit 126\n")
        legacy_wrapper.chmod(0o755)
        docker = Path("/usr/local/bin/docker")
        docker.write_text('''#!/bin/sh
case "$*" in
  buildx*) printf '%s\\n' '{"digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}' ;;
  login*) cat >/dev/null ;;
esac
''')
        docker.chmod(0o755)
        cosign = Path("/usr/local/bin/cosign")
        cosign.write_text("#!/bin/sh\nexit 0\n")
        cosign.chmod(0o755)
        key = Path("/root/.ssh/cs2deploy_ed25519")
        key.parent.mkdir(mode=0o700, exist_ok=True)
        self.require_success(["ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", str(key)])

        admin_key = Path("/root/.ssh/admin_ed25519")
        self.require_success(["ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", str(admin_key)])
        admin_authorized = Path("/home/admin/.ssh/authorized_keys")
        admin_authorized.parent.mkdir(mode=0o700)
        admin_authorized.write_text(admin_key.with_suffix(".pub").read_text())
        admin_authorized.chmod(0o600)
        self.require_success(["chown", "-R", "admin:admin", str(admin_authorized.parent)])

        authorized = Path("/home/cs2deploy/.ssh/authorized_keys")
        authorized.parent.mkdir(mode=0o700)
        authorized.write_text('restrict,command="/usr/local/sbin/cs2predictor-ssh-command" ' + Path(str(key) + ".pub").read_text())
        authorized.chmod(0o600)
        self.require_success(["chown", "-R", "cs2deploy:cs2deploy", str(authorized.parent)])

        admin_sudoers = Path("/etc/sudoers.d/cs2predictor-admin")
        admin_sudoers.write_text("admin ALL=(ALL:ALL) NOPASSWD: ALL\n")
        admin_sudoers.chmod(0o440)

        self.require_success(["/usr/sbin/sshd"])
        self.wait_for_ssh()

        with tempfile.TemporaryDirectory() as tmp:
            env = dict(os.environ, PATH="/usr/local/bin:" + os.environ["PATH"])

            # An inventory an operator would use, connecting as "admin".
            known_hosts = self.require_success(["ssh-keyscan", "-p", "22", "127.0.0.1"]).stdout
            trusted_hosts = Path(tmp, "trusted-hosts")
            trusted_hosts.write_text(known_hosts)
            inventory = Path(tmp, "migration-inventory.json")
            inventory.write_text(json.dumps({"production": {"hosts": {"application": {
                "ansible_host": "127.0.0.1",
                "ansible_port": 22,
                "ansible_user": "admin",
                "ansible_ssh_private_key_file": str(admin_key),
                "ansible_ssh_common_args": (
                    "-o StrictHostKeyChecking=yes -o BatchMode=yes -o IdentitiesOnly=yes "
                    f"-o UserKnownHostsFile={trusted_hosts}"
                ),
                "ansible_python_interpreter": "/usr/bin/python3",
            }}}}))

            result = self.require_success([
                "ansible-playbook", "-i", str(inventory), "/src/ansible/bootstrap.yml",
                "-e", json.dumps({"backup_root": tmp + "/role-backups"}),
            ], env=env)
            self.assertNotIn("BEGIN OPENSSH PRIVATE KEY", result.stdout)

            # The key is reused, never rotated.
            self.assertTrue(key.exists())
            root_policy = self.require_success(["sshd", "-T", "-C", "user=root,host=localhost,addr=127.0.0.1"])
            self.assertIn("permitrootlogin no", root_policy.stdout)
            policy = self.require_success(["sshd", "-T", "-C", "user=cs2deploy,host=localhost,addr=127.0.0.1"])
            self.assertIn("forcecommand none", policy.stdout)
            self.assertIn("pubkeyauthentication yes", policy.stdout)
            archive = next(Path(tmp, "role-backups").glob("*/configuration.tar.gz"))
            self.assertTrue(archive.exists())

            # Repeat the migration: idempotent, preserves the same keypair.
            self.require_success([
                "ansible-playbook", "-i", str(inventory), "/src/ansible/bootstrap.yml",
                "-e", json.dumps({"backup_root": tmp + "/role-backups"}),
            ], env=env)
            self.wait_for_ssh()

            # Scoped to cs2predictor only, not root — see deploy.yml's
            # become_user and ansible/README.md's "Least-privilege deploy".
            ssh = ["ssh", "-p", "22", "-i", str(key), "-o", "BatchMode=yes",
                   "-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile=" + str(trusted_hosts)]
            result = self.require_success(ssh + ["cs2deploy@127.0.0.1", "sudo -n -u cs2predictor id -u"])
            expected_uid = self.require_success(ssh + ["cs2deploy@127.0.0.1", "id -u cs2predictor"]).stdout.strip()
            self.assertNotEqual(expected_uid, "0")
            self.assertEqual(result.stdout.strip(), expected_uid)
            denied = self.run_command(ssh + ["cs2deploy@127.0.0.1", "sudo -n id -u"])
            self.assertNotEqual(denied.returncode, 0, denied.stdout)

            # Run the full deploy flow (site.yml) against the now-migrated host.
            app = Path(tmp, "application")
            app.mkdir(mode=0o755)
            # cs2predictor runs the deploy now, so it needs write access.
            Path(tmp).chmod(0o777)
            (app / ".env").write_text("SECRET=private-application-secret\n")
            (app / ".env").chmod(0o600)
            # Matches production, where APP_PATH is already cs2predictor-owned.
            self.require_success(["chown", "-R", "cs2predictor:cs2predictor", str(app)])
            deploy_known_hosts = self.require_success(["ssh-keyscan", "-p", "22", "127.0.0.1"]).stdout
            run_env = dict(env, RUNNER_TEMP=tmp, DEPLOY_KEY=key.read_text(),
                           KNOWN_HOSTS=deploy_known_hosts, DEPLOY_HOST="127.0.0.1",
                           DEPLOY_PORT="22", DEPLOY_USER="cs2deploy", APP_USER="cs2predictor",
                           APP_PATH=str(app), TAG="v1.0.1",
                           REPOSITORY="example/app", REGISTRY_USER="test", REGISTRY_TOKEN="test-token",
                           POSTGRES_PASSWORD="fresh-pg-password", TELEGRAM_BOT_TOKEN="fresh-bot-token",
                           TELEGRAM_WEBHOOK_SECRET="fresh-webhook-secret", PANDASCORE_TOKEN="fresh-pandascore-token",
                           CADDY_DOMAIN="bot.example.test")
            result = self.require_success([
                "ansible-playbook", "/src/ansible/site.yml", "-e", json.dumps({
                    "release_dir": "/src",
                    "backup_root": tmp + "/deploy-backups", "deploy_lock": tmp + "/deploy-lock",
                })], env=run_env)
            self.assertNotIn("private-application-secret", result.stdout)
            self.assertNotIn("fresh-bot-token", result.stdout)
            self.assertNotIn("fresh-pg-password", result.stdout)
            self.assertNotIn("BEGIN OPENSSH PRIVATE KEY", result.stdout)
            self.assertEqual(json.loads((app / "deployment.json").read_text())["tag"], "v1.0.1")
            # GitHub-managed secrets are now the source of truth for .env.
            self.assertEqual((app / ".env").read_text(),
                              "POSTGRES_PASSWORD=fresh-pg-password\n"
                              "TELEGRAM_BOT_TOKEN=fresh-bot-token\n"
                              "TELEGRAM_WEBHOOK_SECRET=fresh-webhook-secret\n"
                              "PANDASCORE_TOKEN=fresh-pandascore-token\n"
                              "CADDY_DOMAIN=bot.example.test\n")
            self.assertFalse(Path(tmp, "deploy_key").exists())
            self.assertFalse(Path(tmp, "known_hosts").exists())


if __name__ == "__main__":
    unittest.main()
