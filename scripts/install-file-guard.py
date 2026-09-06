#!/usr/bin/env python3
"""Install a native Codex Trash-denial profile without replacing other config."""
import os
from pathlib import Path
import re
import tempfile
import tomllib

PROFILE = '''# BEGIN agent-file-guard permissions
[permissions.agent-file-guard]
description = "Local development with macOS Trash inaccessible"

[permissions.agent-file-guard.filesystem]
":root" = "write"
"~/.Trash" = "deny"
"~/.Trashes" = "deny"
"/**/.[Tt][Rr][Aa][Ss][Hh]" = "deny"
"/**/.[Tt][Rr][Aa][Ss][Hh]/**" = "deny"
"/**/.[Tt][Rr][Aa][Ss][Hh][Ee][Ss]" = "deny"
"/**/.[Tt][Rr][Aa][Ss][Hh][Ee][Ss]/**" = "deny"

[permissions.agent-file-guard.network]
enabled = true
# END agent-file-guard permissions
'''


def profile_config(text):
    document = tomllib.loads(text)
    if "sandbox_workspace_write" in document:
        raise ValueError("Existing sandbox_workspace_write requires explicit migration")
    text = re.sub(r"(?ms)^# BEGIN agent-file-guard permissions\n.*?^# END agent-file-guard permissions\n?", "", text)
    first_table = re.search(r"(?m)^\s*\[", text)
    index = first_table.start() if first_table else len(text)
    header, tables = text[:index], text[index:]
    header = re.sub(r"(?m)^\s*(sandbox_mode|default_permissions)\s*=.*\n?", "", header)
    result = 'default_permissions = "agent-file-guard"\n' + header + tables.rstrip() + "\n\n" + PROFILE
    parsed = tomllib.loads(result)
    assert parsed["default_permissions"] == "agent-file-guard"
    assert "sandbox_mode" not in parsed
    return result


def install(path):
    path = path.resolve()
    old = path.read_text() if path.exists() else ""
    new = profile_config(old)
    if old == new:
        return
    path.parent.mkdir(parents=True, exist_ok=True)
    backup = path.with_name(path.name + ".before-agent-file-guard")
    if old and not backup.exists():
        with backup.open("x") as output:
            os.chmod(backup, 0o600)
            output.write(old)
    fd, temporary = tempfile.mkstemp(prefix=".guard-profile-", dir=path.parent)
    with os.fdopen(fd, "w") as output:
        output.write(new)
        output.flush()
        os.fsync(output.fileno())
    os.replace(temporary, path)
    print("Installed native Trash-denial profile:", path)


if __name__ == "__main__":
    home = Path(os.environ.get("ONE_SHOT_INSTALL_HOME", Path.home()))
    codex = home / ".codex" if "ONE_SHOT_INSTALL_HOME" in os.environ else Path(os.environ.get("CODEX_HOME", home / ".codex"))
    paths = {home / ".codex/config.toml", codex / "config.toml"}
    paths.update((home / ".codex-accounts").glob("*/config.toml"))
    for path in sorted({p.resolve() for p in paths}):
        install(path)
