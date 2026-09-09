"""Exercise native Ansible input validation and the dynamic inventory."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest


class PreparationTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="prepare with spaces ")
        self.root = Path(self.temp.name)
        self.bin = self.root / "bin"
        self.bin.mkdir()
        docker = self.bin / "docker"
        docker.write_text("""#!/bin/sh
case "$1" in
  login) cat >/dev/null; exit 0 ;;
esac
printf '%s\n' "$TEST_MANIFEST"
""")
        docker.chmod(0o755)
        cosign = self.bin / "cosign"
        cosign.write_text("""#!/bin/sh
exit "${MOCK_COSIGN_EXIT:-0}"
""")
        cosign.chmod(0o755)
        self.env = dict(os.environ,
                        PATH=str(self.bin) + ":" + os.environ["PATH"],
                        ANSIBLE_ROLES_PATH="/src/ansible/roles",
                        DEPLOY_HOST="example.com", DEPLOY_PORT="22",
                        DEPLOY_USER="cs2deploy", APP_USER="service_account", APP_PATH="/srv/custom-install", TAG="v1.0.1",
                        REPOSITORY="Example/App", DEPLOY_KEY="private-test-key",
                        REGISTRY_USER="test-user", REGISTRY_TOKEN="registry-test-token",
                        KNOWN_HOSTS="example.com ssh-ed25519 test", RUNNER_TEMP=str(self.root),
                        TEST_MANIFEST=json.dumps({"digest": "sha256:" + "a" * 64}))
        play = [{"hosts": "localhost", "connection": "local", "gather_facts": False,
                 "roles": ["prepare_deploy"], "tasks": [{
                     "name": "Capture prepared host variables",
                     "ansible.builtin.copy": {
                         "dest": str(self.root / "host.json"),
                         "content": "{{ hostvars['application'] | to_json }}",
                     },
                 }]}]
        self.playbook = self.root / "test.yml"
        self.playbook.write_text(json.dumps(play))

    def tearDown(self):
        self.temp.cleanup()

    def run_prepare(self):
        result = subprocess.run(["ansible-playbook", str(self.playbook)], env=self.env,
                                text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
        self.assertNotIn("private-test-key", result.stdout)
        self.assertNotIn("registry-test-token", result.stdout)
        return result

    def test_preparation_creates_private_credentials_and_inventory(self):
        result = self.run_prepare()
        self.assertEqual(result.returncode, 0, result.stdout)
        host = json.loads((self.root / "host.json").read_text())
        self.assertEqual(host["ansible_host"], "example.com")
        self.assertEqual(host["ansible_port"], 22)
        self.assertEqual(host["ansible_user"], "cs2deploy")
        self.assertEqual(host["app_user"], "service_account")
        self.assertEqual(host["app_dir"], "/srv/custom-install")
        self.assertEqual(host["app_image"], "ghcr.io/example/app@sha256:" + "a" * 64)
        self.assertEqual(host["release_tag"], "v1.0.1")
        self.assertIn("production", host["group_names"])
        self.assertIn("StrictHostKeyChecking=yes", host["ansible_ssh_common_args"])
        self.assertEqual((self.root / "deploy_key").stat().st_mode & 0o777, 0o600)

    def test_invalid_inputs_fail_before_credentials_are_written(self):
        for key, value in [("DEPLOY_HOST", "-oProxyCommand=id"), ("DEPLOY_USER", "root;id"),
                           ("DEPLOY_PORT", "0"), ("TAG", "v1.0.1;id"), ("REPOSITORY", "../repo"), ("APP_USER", "cs2deploy"),
                           ("APP_PATH", "/"), ("APP_PATH", "/../etc"), ("APP_PATH", "relative")]:
            original = self.env[key]
            with self.subTest(key=key):
                self.env[key] = value
                result = self.run_prepare()
                self.assertNotEqual(result.returncode, 0, result.stdout)
                self.assertFalse((self.root / "deploy_key").exists())
            self.env[key] = original

    def test_invalid_digest_fails_before_credentials_are_written(self):
        self.env["TEST_MANIFEST"] = '{"digest":"latest"}'
        result = self.run_prepare()
        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertFalse((self.root / "deploy_key").exists())

    def test_unsigned_image_fails_before_credentials_are_written(self):
        self.env["MOCK_COSIGN_EXIT"] = "1"
        result = self.run_prepare()
        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertFalse((self.root / "deploy_key").exists())


if __name__ == "__main__":
    unittest.main()
