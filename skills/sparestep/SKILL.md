---
name: sparestep
description: "Explicit task focus for Codex: finish the agreed result, explain necessary expansion, and defer worthwhile side work to Linear."
---

<!-- sparestep-managed-skill: v1 -->

Activate only for explicit `$sparestep` or `/sparestep`. Resolve the active Git checkout/worktree from task context and pass its shell-quoted absolute path as `--project` in every command. Never substitute Dispatch's canonical root for a named worktree. If the project is unknown, ask once.

**Respect the current mode first.** In Plan mode, keep the outcome, scope, and deferred ideas in the conversation. Research and refine the plan; do not install hooks, mutate focus state, publish Linear issues, or use completion hooks to force implementation. When the user leaves Plan mode and requests implementation, adopt the agreed plan without another planning exercise or slash invocation. If the user only requested a plan, a finished plan is the deliverable.

For an implementation request, use the existing plan or form one compact brief: outcome, required completion conditions, affected component paths, and explicit exclusions. Read only enough to identify these; don't invent extra tests or broaden the task. Show a short outcome/done summary, without routine approval.

Begin with one command, sending JSON through a quoted heredoc (never interpolate user text into shell code):

```sh
{{BINARY}} focus begin --project '<worktree>' --json <<'SPARESTEP_INPUT'
{"goal":"Requested outcome","criteria":[{"id":"behavior","description":"Requested behavior works","kind":"review"},{"id":"tests","description":"Relevant required checks pass","kind":"check"}],"paths":["affected/component"],"excluded_paths":[]}
SPARESTEP_INPUT
```

Use conditions appropriate to the actual task, with stable IDs. `review` records a judgment with evidence; `check` requires an executed command. Initial paths are component boundaries, not permission to modify everything beneath them. Preserve required repository workflows and tests.

If the result says `setup_only`, relay its `next_action` and do not claim protection is active. First-time setup may require one new Codex task. Never bypass native trust. Otherwise retain `focus.id` for subsequent calls (`--id ID`); native session identity supplies isolation.

Work toward the brief using ordinary Codex judgment; no supervisor agents, extra per-tool reports, or repeated planning. At a meaningful boundary:

- **Necessary expansion:** `focus amend --id ID --project '<worktree>' --json`, with stdin JSON `{ "criterion_id":"…", "reason":"Why this is necessary", "evidence":"Observed dependency or failure", "paths":["new/component"] }`. Before continuing, prominently announce **Scope expanded — [reason]**. Never silently weaken the goal, exclusions, or required checks.
- **Worthwhile side work:** `focus defer --id ID --project '<worktree>' --json`, with stdin JSON containing `component`, stable `problem` key, `title`, `description`, `criteria` array, `evidence` array, and live `type_label`/`area_label` (`surface_label` where required). Capture what is already known; don't investigate the side task. Report queued work as queued until a confirmed issue URL exists. Continue the original task.
- **Required command:** run it through `focus check --id ID --project '<worktree>' --criterion CHECK_ID -- command args`. This executes the check and records its real result in the same invocation. Run again after relevant changes when required; don't repeat a passing check solely for Sparestep.
- **Review evidence:** `focus review --id ID --project '<worktree>' --json`, stdin `{ "criterion_id":"…", "evidence":"Concrete result/reference" }`. Use only review conditions; this is not machine proof.
- **Complete:** `focus finish --id ID --project '<worktree>' --json` reconciles required results. Resolve genuine missing obligations; never loop merely to satisfy the recorder. If blocked, use `--result stopped` and explain what remains. Honor cancellation immediately with `focus cancel`.

For commands without a new task:

- Bare `/sparestep`, `connect`, or `open`: `{{BINARY}} start --project '<worktree>' --json` (in Plan mode, use read-only `focus status` instead).
- `status`: `{{BINARY}} focus status --project '<worktree>' --json`.
- `pause`, `resume`, or `disconnect`: `{{BINARY}} ACTION --project '<worktree>' --json`.
- `linear connect`: `{{BINARY}} linear connect --project '<worktree>'`; relay the browser login link and any SSH forwarding instruction. Keep the waiting command alive while the user signs in; do not poll it with model loops.
- `linear configure`: send destination JSON such as `{"team_id":"TEAM_ID","project_id":"PROJECT_ID","assignee_id":"me","state":"Backlog"}` to `{{BINARY}} linear configure --project '<worktree>' --json` once. Use live metadata, including Dispatch's issue policy. Ask for a destination only when it cannot be resolved.

Return concise status and real links. The GUI is optional; `open` exposes the same task and scope changes. Do not claim every shell, remote tool, or subagent is covered, or that saved credentials prove a live connection. Automatic learning remains off.
