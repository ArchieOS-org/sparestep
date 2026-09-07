#!/usr/bin/env python3
"""Check ordinary filesystem operations in the active Codex runtime.

Run this after loading a permission profile. The check uses only new probe
directories in the workspace and temporary locations. It never accesses Trash.
An unsuccessful operation returns a nonzero exit code and its exact error.
"""

import json
import os
from pathlib import Path
import tempfile


def check_location(base):
    result = {"location": str(base), "operations": []}
    try:
        root = Path(tempfile.mkdtemp(prefix="codex-filesystem-check-", dir=base))
    except OSError as error:
        result["operations"].append({"operation": "create_probe", "ok": False, "error": str(error)})
        return result

    def attempt(name, action):
        try:
            action()
            result["operations"].append({"operation": name, "ok": True})
        except OSError as error:
            result["operations"].append({"operation": name, "ok": False, "error": str(error)})

    original, replacement = root / "original.txt", root / "replacement.txt"
    directory, renamed = root / "directory", root / "renamed-directory"
    attempt("write_file", lambda: original.write_text("Codex filesystem permission check\n"))
    attempt("replace_file", lambda: os.replace(original, replacement))
    attempt("delete_file", lambda: (replacement if replacement.exists() else original).unlink())
    attempt("create_directory", directory.mkdir)
    attempt("rename_directory", lambda: directory.rename(renamed))
    attempt("delete_directory", lambda: (renamed if renamed.exists() else directory).rmdir())
    attempt("delete_probe", root.rmdir)
    if root.exists():
        result["retained_probe"] = str(root)
    return result


def main():
    locations = dict.fromkeys((Path.cwd().resolve(), Path(tempfile.gettempdir()).resolve(), Path("/tmp").resolve()))
    results = [check_location(location) for location in locations]
    passed = all(operation["ok"] for result in results for operation in result["operations"])
    print(json.dumps({"ok": passed, "locations": results}, indent=2))
    return 0 if passed else 1


if __name__ == "__main__":
    raise SystemExit(main())
