# Validation

This file separates the current v0.3 implementation checks from historical preview records. Passing a check does not prove measured savings, complete native coverage, a live Linear connection, or a published release.

## v0.3.1 worktree repair

Validated September 9, 2026. Codex loads linked-worktree hooks from the root checkout; setup now verifies and trusts that actual source, and shared hook commands route events to explicitly connected worktrees. Focus scope remains local to each worktree. A ready first activation saves the brief immediately.

- Go race tests and vet passed, including registered/unregistered routing, nested unrelated repositories, malformed Git metadata, and disconnect isolation.
- Focus and native onboarding regressions passed. The linked worktree reaches ready while preserving neighboring hook definitions and shared trust; disconnecting it retains the root hooks.
- `python3 scripts/check-native-focus.py --binary dist/sparestep-linux-amd64 --linked-worktree` passed using one isolated Spark turn: setup ready, native hook observed, outside file absent.
- Installed v0.3.1 on the Linux host. Setup for the affected Dispatch worktree now reports ready without a restart instruction. This is setup verification, not a claim that the existing task has already activated a focus brief.

## v0.3.0 checks

Validated September 8, 2026 on the Linux development host with Go 1.27.1 and Codex CLI 0.153.4.

The following checks passed:

- `python3 scripts/check-focus.py`: setup-only handoff, native scope decisions, the observed-hook transition, evidenced amendment, failed and passing checks, one-shot Stop gate, Plan mode read-only behavior, session/worktree isolation, durable completion, and credential-free deferred deduplication.
- `python3 scripts/check-onboarding.py --binary /tmp/sparestep-browser-check`: nested worktree setup, idempotent connection, concurrent report reuse, persistent pause, separate worktrees, background report lifetime, and scoped disconnect. The native setup portion trusted nine generated hooks while preserving an unrelated hook state entry.
- `python3 scripts/check-native-discovery.py --binary /tmp/sparestep-browser-check`: Codex discovered all required hooks, native trust remained required, and disconnect removed them. It used an isolated temporary Codex configuration and no model task.
- `SPARESTEP_BINARY=/tmp/sparestep-browser-check ... node scripts/check-focus-browser.mjs`: empty focus, native-guard waiting and observed states, planning mode, completed-to-Done, persistent scope amendments, criteria/evidence, deferred queue versus confirmed link, invalid URL rejection, collapsible legacy activity, mobile overflow, and browser runtime errors.
- The legacy browser flow passed from a temporary copy of `scripts/check-browser.mjs` with its screenshot redirected outside the repository: authenticated reload, findings feedback, draft download and Linear handoff URL, saved issue link, pause/resume, and mobile layout. It did not submit a Linear issue.
- Static Linux amd64 and arm64 release binaries were cross-built with `CGO_ENABLED=0` and inspected as static ELF files. The release amd64 binary passed focus, capture, onboarding, and native-discovery integration checks.
- `go test -race ./...`, `go vet ./...`, JavaScript syntax checks, skill validation, and `git diff --check` passed.
- `python3 scripts/check-native-focus.py --binary dist/sparestep-linux-amd64` reproduced a real `codex exec --json` run with `gpt-5.3-codex-spark` used a private temporary Codex home, normal `sparestep start` setup, nine trusted exact hook hashes, and a focus bound to the actual native session. Its out-of-scope `apply_patch` was blocked before file creation. Codex reported `Command blocked by PreToolUse hook`; the report recorded `hook_observed:true`. No trust bypass was used. An earlier definitions-only probe correctly remained unprotected.
- Typed collaboration-mode metadata is matched to native `turn_id`, read from at most 256 KiB of the rollout tail, and cached for the turn. Tests cover Plan/default, stale or missing turns, unsupported metadata, incomplete records, oversized input, and non-regular files. Native approval `permission_mode` is not treated as collaboration mode.

The focus check and browser fixtures use temporary projects, synthetic native hook payloads, and intercepted report data. They validate the local protocol and UI without contacting Linear. The separate real-model check above verifies one supported edit boundary; it does not prove coverage of every tool or desktop workflow.

## Current limitations and pending evidence

- The Linear MCP/OAuth publisher and durable outbox are implemented. Browser OAuth discovery and authorization-link creation were exercised, but sign-in was not completed and no real issue was created by Sparestep. Live Dispatch metadata was read through the existing Codex connector; the standalone publisher was tested with schema-validating MCP fixtures. Do not call a queued item filed without a confirmed issue URL.
- The outbox retry, lease, deduplication, and ambiguous-result paths have local tests and black-box queue coverage; external network failure and a real Linear create/reconcile sequence remain pending.
- Native scope denial was exercised in one real CLI task. Desktop Plan-mode transitions, additional tools, hosted or remote actions, and arbitrary Bash effects still need field validation. Missing rollout metadata leaves a visible coverage gap and relies on the explicit skill instructions.
- `focus check` records the actual command exit result. Review criteria record an agent assessment and are not machine proof. Completion is accepted only after an explicit finish attempt with all required criteria satisfied; an ordinary stop or foreground question does not complete a task.
- No fresh VM reboot campaign, arm64 hardware run, assistive-technology audit, user study, controlled false-positive study, token telemetry validation, or savings experiment has been performed for the current implementation.

## v0.2.0 historical record

Validated September 8, 2026 during the one-command setup preview. This section records that preview separately from the current v0.3 checks.

- The explicit skill embedded and installed without replacing unrelated skills; managed-skill backups and unowned metadata handling were checked.
- Temporary Git repositories with a nested folder and separate worktree verified root selection, idempotent definitions, neighboring-hook preservation, pause persistence, and worktree-scoped disconnect.
- An isolated Codex configuration checked the then-current generated hook definitions through `hooks/list` and `config/batchWrite`, with no model task. A new Codex task could still be required before an already-open task loaded new configuration.
- Concurrent report opens reused one background process, and authenticated health checks verified each project.

The v0.2 preview did not validate native capture in a real model task or issue creation. Its hook-count wording reflects that historical setup and is not the current v0.3 hook catalog.

## v0.1.0 historical record

Validated September 8, 2026 as an earlier implementation preview, with no measured savings claim. The recorded checks included Go race tests and vet, static Linux amd64/arm64 cross-builds, amd64 execution, a restricted non-root container, isolated Codex hook discovery, supported payload replay, authenticated browser interactions, and the example report screenshot.

That preview also recorded a small 34-process capture sample of approximately 4.2 ms median and 5.6 ms p95 per recorder invocation. It was not a production latency guarantee and did not measure saved time or tokens. Its browser flow opened a Linear prefilled form but submitted no issue.

The historical preview did not include the current focus controller, native guard boundary, durable Linear outbox, OAuth/MCP publisher, or current browser focus fixtures. Those features require the current checks and the pending live integration validation above.

The product plan is in [PLAN.md](PLAN.md). Planned targets are not measured results.
