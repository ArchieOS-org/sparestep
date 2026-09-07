#!/usr/bin/env python3
"""Install the shared fleet SSH guard and verify OpenSSH's effective settings."""

import argparse
import importlib.util
import json
import os
from pathlib import Path
import shlex
import stat
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[1]
MARKER = "# Managed fleet SSH guard"


def atomic_write(path, data, mode):
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, temporary = tempfile.mkstemp(prefix=".fleet-ssh-", dir=path.parent)
    try:
        with os.fdopen(fd, "wb") as stream:
            stream.write(data)
            stream.flush()
            os.fsync(stream.fileno())
        os.chmod(temporary, mode)
        os.replace(temporary, path)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


def settings(path, host):
    result = subprocess.run(["/usr/bin/ssh", "-G", "-F", str(path), host],
                            capture_output=True, text=True, check=True)
    return dict(line.split(" ", 1) for line in result.stdout.splitlines() if " " in line)


def install(home):
    home = home.resolve()
    guard = home / ".local/libexec/fleet-ssh-guard.py"
    policy_path = home / ".config/fleet-ssh-guard/policy.json"
    include = home / ".ssh/fleet-ssh-guard.conf"
    config = home / ".ssh/config"
    for path in (config.parent, config):
        if path.exists() or path.is_symlink():
            info = path.lstat()
            expected_type = stat.S_ISDIR if path == config.parent else stat.S_ISREG
            if not expected_type(info.st_mode) or info.st_uid != os.getuid():
                raise RuntimeError(f"SSH configuration path must be owned and regular: {path}")
    source = ROOT / "scripts/fleet-ssh-guard.py"
    policy = json.loads((ROOT / "fleet-ssh-policy.json").read_text())
    # Import the same validator used at runtime; never install an unusable policy.
    spec = importlib.util.spec_from_file_location("fleet_ssh_guard", source)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    module.validate_policy(policy)
    original = config.read_bytes() if config.exists() else b""
    targets = list(policy["targets"])
    names = [name for aliases in policy["targets"].values() for name in aliases]
    command = shlex.quote(sys.executable) + " " + shlex.quote(str(guard))
    rules = (MARKER + "\nMatch host " + ",".join(targets + names) + "\n"
             "  ProxyCommand " + command + " proxy %h %p\n"
             "  PermitLocalCommand yes\n"
             "  LocalCommand exec " + command + " authenticated %h %p\n"
             "  ConnectionAttempts 1\n"
             "  BatchMode yes\n"
             "  PreferredAuthentications publickey\n"
             "  IdentitiesOnly yes\n"
             "  ControlMaster no\n"
             "  ControlPath none\n"
             "Match all\n")
    if any(character in str(home) for character in ('"', '\n', '\r', '%')):
        raise RuntimeError("SSH config paths cannot contain quotes, newlines, or percent tokens")
    include_line = 'Include "' + str(include) + '"'
    lines = original.decode().splitlines()
    lines = [line for line in lines if line not in (MARKER, include_line)]
    # OpenSSH takes the first value. Put the fleet rules before other includes;
    # Match all restores matching for their unrelated host definitions.
    lines[0:0] = [MARKER, include_line]
    updated = ("\n".join(lines) + "\n").encode()
    config.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    include_fd, candidate_include = tempfile.mkstemp(prefix=".fleet-ssh-rules-", dir=config.parent)
    with os.fdopen(include_fd, "w") as stream:
        stream.write(rules)
    fd, candidate = tempfile.mkstemp(prefix=".fleet-ssh-config-", dir=config.parent)
    os.close(fd)
    try:
        candidate_text = updated.decode().replace(include_line, 'Include "' + candidate_include + '"', 1)
        Path(candidate).write_text(candidate_text)
        for host in targets + names:
            effective = settings(candidate, host)
            for name, expected in {"connectionattempts": "1", "controlmaster": "false",
                                   "permitlocalcommand": "yes", "batchmode": "yes",
                                   "identitiesonly": "yes", "preferredauthentications": "publickey"}.items():
                if effective.get(name) != expected:
                    raise RuntimeError(f"{host}: {name} did not resolve to {expected}")
            if str(guard) not in effective.get("proxycommand", ""):
                raise RuntimeError(f"{host}: fleet proxy was overridden")
            if str(guard) not in effective.get("localcommand", ""):
                raise RuntimeError(f"{host}: authentication callback was overridden")
            if effective.get("controlpath") not in (None, "none"):
                raise RuntimeError(f"{host}: connection multiplexing was enabled")
        if config.exists() and original != updated:
            backup = config.with_name("config.before-fleet-ssh-guard")
            if not backup.exists():
                atomic_write(backup, original, 0o600)
        atomic_write(guard, source.read_bytes(), 0o700)
        atomic_write(policy_path, (json.dumps(policy, indent=2) + "\n").encode(), 0o600)
        atomic_write(include, rules.encode(), 0o600)
        atomic_write(config, updated, 0o600)
    finally:
        os.unlink(candidate)
        os.unlink(candidate_include)
    print(f"Installed shared fleet SSH guard for {len(targets)} IPs and {len(names)} aliases.")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--home", type=Path, default=Path.home())
    install(parser.parse_args().home)
