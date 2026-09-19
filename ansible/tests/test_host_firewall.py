"""Fitness tests for the host_firewall role.

Structural, not behavioural: the role writes packet filter rules and
systemd units, none of which this container can exercise. What they pin
down are the exact properties whose absence has already taken production
down once each.

1. The script waits for the default route instead of exiting non-zero.
   It runs as docker.service's ExecStartPost, and at boot DHCP regularly
   has not finished yet; failing there kills the Docker daemon, and past
   systemd's restart limit the host is left with no container runtime.
2. The Docker drop-in re-applies the rules on every daemon start, because
   Docker recreates DOCKER-USER itself and drops whatever was in it.
3. The restart drop-in widens systemd's start limit past Docker's packaged
   three failures in sixty seconds, which the boot race burned through in
   four.
4. Superseded artifacts are removed rather than left in place: an old
   drop-in and an old INPUT chain both survived their replacements and
   silently took precedence over them.
"""

import unittest
from pathlib import Path

import yaml

ROLE = Path(__file__).resolve().parents[1] / "roles" / "host_firewall"
SCRIPT = ROLE / "files" / "cs2predictor-firewall"


def _walk(tasks):
    for task in tasks or []:
        if not isinstance(task, dict):
            continue
        nested = False
        for key in ("block", "rescue", "always"):
            if key in task:
                nested = True
                yield from _walk(task[key])
        if not nested:
            yield task


class HostFirewallRoleTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.script = SCRIPT.read_text()
        cls.tasks = list(_walk(yaml.safe_load((ROLE / "tasks" / "main.yml").read_text())))
        cls.defaults = yaml.safe_load((ROLE / "defaults" / "main.yml").read_text())

    def test_script_waits_for_the_default_route(self):
        docker_half = self.script.split("if [[ $mode != host ]]; then", 1)
        self.assertEqual(len(docker_half), 2, "the docker half of the script moved or was renamed")
        wait, _, rest = docker_half[1].partition("mapfile -t interfaces")
        self.assertIn("ip -4 -o route show default", wait, "the docker rules must wait for the default route")
        self.assertIn("sleep 1", wait, "waiting means retrying, not checking once")
        self.assertIn(
            "exit 1", rest,
            "a route that never appears must still fail — otherwise published ports go unguarded",
        )

    def test_docker_drop_in_reapplies_the_rules_on_every_start(self):
        drop_in = (ROLE / "templates" / "docker-firewall.conf.j2").read_text()
        self.assertIn("ExecStartPost=/usr/local/sbin/cs2predictor-firewall docker", drop_in)

    def test_restart_drop_in_outlasts_a_boot_race(self):
        drop_in = (ROLE / "templates" / "docker-restart.conf.j2").read_text()
        self.assertIn("Restart=on-failure", drop_in)
        self.assertIn("StartLimitBurst", drop_in)
        self.assertGreater(
            self.defaults["host_firewall_docker_start_limit_burst"], 3,
            "Docker's packaged unit already gives up after 3 failures; widening it is the point",
        )
        self.assertGreater(self.defaults["host_firewall_docker_start_limit_interval"], 60)

    def test_superseded_artifacts_are_removed(self):
        removals = [
            t for t in self.tasks
            if (t.get("ansible.builtin.file") or {}).get("state") == "absent"
        ]
        self.assertTrue(
            any("legacy" in str(t.get("loop", "")) for t in removals),
            "the role must remove the superseded units and scripts listed in its defaults",
        )
        self.assertTrue(
            any("iptables" in str(t.get("ansible.builtin.shell", "")) for t in self.tasks),
            "the role must also unlink the superseded INPUT chain, not just its script",
        )
        for path in self.defaults["host_firewall_legacy_units"] + self.defaults["host_firewall_legacy_scripts"]:
            self.assertTrue(path.startswith("/"), f"legacy path {path!r} must be absolute")

    def test_the_ssh_port_is_never_rewritten_unasked(self):
        self.assertEqual(
            self.defaults["host_firewall_ssh_port"], "",
            "defaulting this to any port means a run without it can lock everyone out",
        )
        writers = [
            t for t in self.tasks
            if (t.get("ansible.builtin.copy") or {}).get("dest") == "/etc/cs2predictor/ssh-port"
        ]
        self.assertEqual(len(writers), 1)
        self.assertIn("host_firewall_ssh_port | length > 0", str(writers[0].get("when")))


if __name__ == "__main__":
    unittest.main()
