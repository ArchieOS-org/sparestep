---
name: sparestep
description: Explicit Sparestep workflow for bounded Codex activity capture in the active project.
---

<!-- sparestep-managed-skill: v1 -->

Use only for explicit `$sparestep` or slash-command requests. Resolve `<active checkout/worktree>` from task context (for Dispatch, the current worktree) and shell-quote it in every call; the tool's cwd may be `/home`. Never replace a named worktree with the canonical root. If cwd is outside a Git repository and no project was named, ask one project question; never guess `/home`.

For no action, default, or `connect`, run exactly one shell invocation:

```sh
{{BINARY}} start --project '<active checkout/worktree>' --json
```

`start` finds the Git root, installs capture definitions, and returns JSON with `project`, `status`, `message`, `url`, and `next_action`. Report success only when JSON says so. Return `url` when present and mention nonempty `next_action`. If native hook trust is needed, tell the user to open `/hooks` and review/trust Sparestep entries; never bypass trust or edit private stores.

Map requests:

- `status`: `{{BINARY}} doctor --project '<active checkout/worktree>' --json`
- `open`: `{{BINARY}} open --project '<active checkout/worktree>' --json`
- `pause`: `{{BINARY}} pause --project '<active checkout/worktree>' --json`
- `resume`: `{{BINARY}} resume --project '<active checkout/worktree>' --json`
- `disconnect`: `{{BINARY}} disconnect --project '<active checkout/worktree>' --json`

Translate JSON into one short sentence, a clickable `url` when present, and nonempty `next_action`; never dump JSON. Do not scan, diagnose, run models, or poll. Use real links; no demos unless asked. `open_in_codex`, when available, is a separate browser call; otherwise return the URL and mention SSH forwarding only if required.
