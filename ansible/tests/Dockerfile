FROM python:3.12-slim
# openssh-server/openssh-client/sudo/python3: test_host_access.py builds a
# real sshd + sudo fixture and drives it over an actual SSH connection, then
# runs the host_access role against it — nothing here stubs those out, so
# they must actually be installed. python3 (the Debian package, distinct
# from this image's own /usr/local/bin/python) is also what the role
# connects to the "remote" node as via ansible_python_interpreter. acl is
# required by Ansible's `become` support for switching to a non-root user
# (deploy.yml/webhook.yml/notify_failure.yml all become app_user, not
# root) — without it, Ansible cannot safely hand the become_user read
# access to the temp files it creates as the connecting user. Production
# hosts need this package too; see ansible/README.md.
RUN apt-get update && apt-get install -y --no-install-recommends \
    tar openssh-server openssh-client sudo python3 acl \
    && rm -rf /var/lib/apt/lists/*
COPY requirements.txt /tmp/requirements.txt
RUN pip --no-cache-dir install -r /tmp/requirements.txt
WORKDIR /src
CMD ["python", "-m", "unittest", "discover", "-s", "/src/ansible/tests", "-p", "test_*.py", "-v"]
