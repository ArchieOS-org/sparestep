# Sparestep product and implementation plan

This plan describes the approved v0.3 direction and the implementation that exists around it. It is a local focus controller for Codex, with optional deferred Linear filing. It does not claim measured savings or complete coverage.

## 1. Product promise and novice workflow

Sparestep helps a user finish an agreed task. The user explicitly starts it with `/sparestep <task>`. The brief contains the outcome, completion conditions, affected component paths, and exclusions. The user works normally, records checks when required, explains necessary scope expansion, and explicitly asks to finish.

Plan mode keeps the outcome, scope, and optional ideas in the conversation. It does not install hooks, mutate focus state, publish Linear issues, or force implementation. When the user leaves Plan mode and requests implementation, Sparestep uses the existing agreed plan as the brief without another planning exercise. A bare `/sparestep` can connect or open the report; `status` shows focus state and deferred work; the report remains available from the terminal or optional browser UI.

The first connection uses Codex's native hook setup. A new Codex task may be required once for the hook entries to load and become trusted. The setup response must say what to do next and must not claim that capture or native guards are active before a real event is observed. A Linux VM is a first-class environment: terminal use works over SSH, and `serve` provides an authenticated loopback report that can be read through SSH forwarding.

## 2. Mode-aware focus lifecycle

The persisted focus task supports `planning` and `implementing` modes. Its lifecycle status is `active`, `paused`, `completed`, `stopped`, `cancelled`, or `superseded`.

An implementation handoff begins one compact brief. A native `PreToolUse` event sets `hook_observed`; a CLI focus record by itself is not proof that native guards have run. Until the first real native event, the UI says **Waiting for native guards**. The UI says **Done** only for a completed task. A pause is durable and can resume; cancellation is honored immediately; a stopped task records what remains.

The completion gate runs only after an explicit `focus finish` attempt. Completion checks every required criterion. A missing criterion is an error and never becomes success through a stop event, an assistant question, a passing-looking sentence, or a CLI-only state change. Stop handling may explain the missing obligations after an explicit completion request, but it does not force the agent to continue after a question or imply that a task is complete.

At meaningful boundaries:

- Necessary expansion records a reason, evidence, criterion when relevant, and affected paths, then announces the expansion before work continues.
- Worthwhile optional work records a bounded deferred item and continues the original task. It does not trigger a second investigation or a new planning loop.
- A required command runs through the focus check path so its real result is recorded. Review evidence records a judgment separately from machine checks.
- Finish reconciles the actual criteria. Missing obligations are resolved or the task is stopped with an explanation.

## 3. Native guard boundary and honest evidence

The native hook is quiet and local. It can enforce supported path boundaries for recognized file edits and, in Bash, a small set of recognized formatter or check operations. It can invalidate checks after relevant edits, restore the brief after session start or compaction, and identify opaque operations as coverage gaps. It does not understand every Bash command, arbitrary shell side effect, hosted tool, remote action, or external machine.

The report therefore separates observed activity, check results, review evidence, and coverage gaps. It must not claim that every command or subagent was covered, that a saved credential proves a live integration, or that a passing command proves correct code or deployment. Observed duration is evidence about an attempt, not a measured saving. Automatic learning remains disabled; no instruction or skill is silently rewritten from findings.

## 4. Deferred Linear delivery

Linear is optional to the core report. Sparestep saves side work to a durable local outbox and continues the focus task. The outbox fingerprint is derived from repository, component, and problem so repeated observations do not create a second item merely because wording or task IDs differ. A worker uses leases and bounded retries; an uncertain create result is reconciled before another create attempt. A timeout is never treated as proof of creation.

The visible states are `queued`, `sending`, `created`, `ambiguous`, `auth_required`, and `needs_attention`. Only `created` with a confirmed issue URL is filed. The UI must keep queued and filed language distinct and must not expose credentials or full transcripts.

The first connection is one browser OAuth sign-in through Linear's MCP endpoint. Sparestep owns its OAuth state and does not require an API key or reuse Codex's private connector storage. A headless VM prints the browser and SSH-forwarding steps. If authentication or live policy is unavailable, local work remains queued or needs attention.

Dispatch filing uses live validated policy. The current default resolves to the one active Dispatch project, currently `Dispatch v1 — Duncan feedback`, in `Backlog`, with the team, assignee, issue type, area, and any required surface or milestone validated before a write. Policy failures stop filing rather than inventing a destination. Automatic issue creation does not add a per-issue approval step after the user has enabled this task behavior.

## 5. State, release, and migration boundaries

The current source keeps focus tasks in `internal/focus` and persists them in `focus.db`. The Linear outbox is implemented in `internal/linear` and currently persists in the separate `linear.db`; its tables include the durable outbox and task association. The existing activity and finding reports remain in the old `sparestep.db` store. These stores have separate responsibilities. A migration must preserve old reports and state, explain any unavailable history honestly, and never present migrated observations as newly verified evidence.

The v0.3.0 preview targets Linux x86_64 and arm64 through the verified installer. The [releases page](https://github.com/ArchieOS-org/sparestep/releases) is the canonical download link. Clean headless Linux and SSH forwarding checks, focus lifecycle checks, outbox retry and ambiguity checks, and browser accessibility checks are release evidence rather than claims made in advance.

The implementation keeps the product small: finish the agreed task, explain necessary changes, and preserve useful optional work. Broader shell coverage, hosted-tool coverage, measured savings, automatic learning, and other integrations require separate evidence and a separate decision.
