"""Fitness tests for the database_backup role's S3 path.

These assert on the role's structure rather than running it: the real thing
needs Docker, an S3 endpoint and a database, none of which belong in this
suite. What they protect is the property a real incident turned on — that a
failing upload says why it failed.

The role used to pass the S3 credentials through each task's `environment:`,
which forced `no_log: true` on those tasks, which in turn swallowed every
AWS CLI error message. A deploy then failed with nothing but "output has
been hidden", and the cause (an expired key? a missing binary? a bucket
policy?) could only be guessed at. Credentials now live in one private
file, and the commands that use it stay legible.
"""

import unittest
from pathlib import Path

import yaml

ROLE = Path(__file__).resolve().parents[1] / "roles" / "database_backup" / "tasks" / "main.yml"


def _walk(tasks):
    """Yield every task, descending into block/rescue/always."""
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


class DatabaseBackupRoleTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.tasks = list(_walk(yaml.safe_load(ROLE.read_text())))

    def aws_cli_tasks(self):
        for task in self.tasks:
            argv = (task.get("ansible.builtin.command") or {}).get("argv") or []
            if any(isinstance(arg, str) and arg.startswith("amazon/aws-cli") for arg in argv):
                yield task, argv

    def test_aws_cli_tasks_exist(self):
        self.assertTrue(list(self.aws_cli_tasks()), "no AWS CLI tasks found — did the role move or get rewritten?")

    def test_aws_cli_errors_are_not_hidden(self):
        for task, _ in self.aws_cli_tasks():
            self.assertNotIn(
                "no_log", task,
                f"{task.get('name')!r} hides its output; an S3 failure must be able to say what went wrong",
            )

    def test_credentials_reach_the_container_through_a_private_file(self):
        for task, argv in self.aws_cli_tasks():
            self.assertIn("--env-file", argv, f"{task.get('name')!r} must take its credentials from --env-file")
            self.assertNotIn(
                "environment", task,
                f"{task.get('name')!r} must not carry secrets in its environment — that is what forced no_log",
            )
            self.assertNotIn("AWS_SECRET_ACCESS_KEY", argv, f"{task.get('name')!r} passes a secret on the command line")

    def test_the_credentials_file_itself_is_written_privately_and_hidden(self):
        writers = [t for t in self.tasks if "ansible.builtin.copy" in t and "AWS_SECRET_ACCESS_KEY" in str(t)]
        self.assertEqual(len(writers), 1, "expected exactly one task writing the S3 credentials file")
        writer = writers[0]
        self.assertTrue(writer.get("no_log"), "the task that writes the credentials must be the one hiding its output")
        self.assertEqual(writer["ansible.builtin.copy"].get("mode"), "0600", "credentials file must not be readable by others")

    def test_the_credentials_file_is_always_removed(self):
        role = yaml.safe_load(ROLE.read_text())
        always = [t for block in role if isinstance(block, dict) for t in block.get("always", [])]
        removals = [
            t for t in always
            if (t.get("ansible.builtin.file") or {}).get("state") == "absent"
            and "creds" in str((t.get("ansible.builtin.file") or {}).get("path", ""))
        ]
        self.assertTrue(removals, "the staged credentials must be removed even when the backup fails")


if __name__ == "__main__":
    unittest.main()
