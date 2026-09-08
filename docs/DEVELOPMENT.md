# Developing Sparestep

The v0.3 preview adds a mode-aware focus controller and a durable, OAuth-capable Linear outbox while preserving the local activity reports. Upstream history is retained at tag `upstream-c490db4`; the upstream license and notices remain in effect.

## Build and check

The module requires Go 1.26 or newer. Use Go 1.27.1 for the release build:

```sh
go test -race ./...
go vet ./...
sh scripts/build-release.sh
```

The release script builds static Linux amd64 and arm64 binaries and their SHA-256 manifest. Users do not need Go, Node, SQLite CLI, or a desktop. The interface is embedded in the executable.

Additional integration checks:

```sh
python3 scripts/check-capture.py --binary dist/sparestep-linux-amd64
python3 scripts/check-focus.py
python3 scripts/check-native-discovery.py --binary dist/sparestep-linux-amd64
python3 scripts/check-onboarding.py --binary dist/sparestep-linux-amd64
# Optional: one real model call with your existing Codex login
python3 scripts/check-native-focus.py --binary dist/sparestep-linux-amd64
npm ci
npm run test:browser
node scripts/check-focus-browser.mjs
```

Native discovery requires Codex CLI and uses an isolated temporary configuration without starting a model task. Browser checks require Chrome (`CHROME_PATH` overrides its location). Node and Playwright are developer tools only. See [validation](VALIDATION.md) for what was actually tested.

## Architecture

- `internal/hooks`: bounded Codex event normalization and reversible project connection.
- `internal/codexsetup`: scoped setup through Codex's public hook catalog and configuration API.
- `skills`: embedded, explicit Sparestep skill and safe local installation.
- `internal/store`: SQLite observations, dispositions, drafts, reports, and conservative detection in the existing `sparestep.db` store.
- `internal/focus`: explicit task briefs, mode-aware lifecycle, native scope checks, completion criteria, and `focus.db` persistence.
- `internal/linear`: OAuth/MCP client, live Dispatch policy validation, durable deduplicated outbox in `linear.db`, bounded worker retries, and ambiguity reconciliation.
- `internal/model`: versioned observation/report boundaries.
- `internal/app`: terminal guide, commands, hook runner, browser listener, and isolated examples.
- `internal/web`: authenticated loopback interface and issue-draft handoff.

The browser server is optional. Hooks append directly to SQLite so recording does not depend on the browser process. Native focus guards can deny supported out-of-scope edits, deny recognized implementation writes when Plan mode is known, and issue one bounded Stop response after an explicit completion request with unmet criteria. Ordinary stops, foreground questions, and unknown shell effects remain quiet; the guard never forces a continuation loop. Capture and guard handling make no per-tool model calls.

## Evidence boundaries

Codex's actual project config must load and its hooks must be trusted. The skill invokes `start`, which installs only Sparestep's project definitions and registers their exact hashes through the same native configuration API used by Codex's hook review. It preserves unrelated hooks and disabled settings. A separate setup process cannot prove that an already-open task reloaded its configuration; setup may ask the user to open a new task once. Unsupported clients or untrusted project layers retain a specific native setup step. The lower-level `connect` command still writes definitions only. `doctor` reports whether events have actually reached the recorder. A successful configuration write is not successful capture or native focus protection.

The guard reads only typed `turn_context` collaboration-mode metadata matching the native hook turn ID. Reads are bounded to a 256 KiB rollout tail; successful observations are cached for the turn. Approval `permission_mode` does not identify Plan mode. Missing or unsupported metadata leaves an explicit coverage gap; the skill remains responsible for respecting the current mode.

The skill is explicit-only. In Plan mode it keeps the brief and deferred ideas in conversation and does not install hooks, mutate focus state, or publish Linear issues. After the user leaves Plan mode and requests implementation, it adopts an existing agreed plan without a second planning exercise and begins one compact brief. Completion is attempted only through the explicit finish path; a missing criterion never becomes success because Codex stopped or asked a question. Review criteria are agent assessments, while check criteria require a command result recorded by the CLI.

The hook source may omit exit status. Printed output, printed JSON, and statements such as “done” do not turn unknown results into successes. Observed command durations and time between hook events have different provenance; the latter includes hook overhead and scheduling. The report never calls their sum guaranteed wall-clock savings.

In particular, native Bash/local tools normally send model-facing output in `tool_response`, which may be plain text. The adapter deliberately does not infer an exit code from that text. It accepts a typed `exit_code` when a sender supplies one, or a structured MCP `isError` response. The latter can classify a bounded known error signature from MCP text without storing that text. Ordinary shell failures can remain invisible to findings until a reliable result adapter is connected. Focus scope inspection understands supported file edits and only a small set of literal Bash checks and formatter commands; arbitrary shell, hosted tools, and remote activity remain coverage gaps. See the [official PostToolUse contract](https://learn.chatgpt.com/docs/hooks#posttooluse).

The initial live detector focuses on recurring known failure signatures. Repeated successful checks/reads require `ContextKnown` and a nonempty matching state key. Normal hook capture does not assert this, because it cannot establish all source, environment, instruction, and compaction context. Future source adapters can supply this evidence after validation.

There is no actual token source connected in this release. The model/store boundary preserves source-tagged usage for future adapters. Do not estimate tokens from weighted calls or output length and label that measured usage.

## Local access and privacy

The server listens only on `127.0.0.1`, generates a per-process access token, and prints a fragment-based browser URL. The browser exchanges it for a local session and removes the fragment. Write requests require a matching session/CSRF/origin or the bearer token. Treat the printed access URL as local access credentials.

On a VM, forward the chosen loopback port over SSH. The printed `USER@VM` is a placeholder for the user's SSH host. If the local port on the user's computer is busy, choose another local port in `ssh -L` and use that port in the browser address. The recorder and database stay on the execution host.

Do not upload complete transcripts. Hook storage uses bounded, conservative command descriptions and error signatures. Review a draft before sharing it: project names and issue descriptions can still be private. Linear receives bounded, redacted issue content through its MCP issue-creation tool only after the user has connected OAuth and live policy validation succeeds. OAuth state is stored locally with restricted permissions; Sparestep does not require or store a Linear API key, and it does not reuse Codex's private connector storage.

## Installation and removal

`install.sh` accepts `SPARESTEP_VERSION`, `SPARESTEP_INSTALL_DIR`, and a local `SPARESTEP_RELEASE_DIR` for offline installs/testing. It installs the executable and the embedded skill. The skill lives in `~/.agents/skills/sparestep` by default, or `$CODEX_HOME/skills/sparestep` when that configuration directory is explicitly set. `sparestep install-skill --skill-dir FOLDER` selects an alternate final skill directory. Unowned files are preserved; managed updates keep a first backup. Upgrades keep recordings and feedback.

`start` and `open` reuse an authenticated loopback report process per worktree. A short local file lock prevents duplicate launches. Private readiness files and logs live in the state folder's `reports` directory. No startup service is registered. Opening a report after reboot recreates its process. Other commands resolve the current Git worktree too; the hook recorder uses its configured project without running Git on each event.

Disconnect each configured project before removing the executable. Disconnect preserves other hooks and saved history. To erase history, remove the dedicated state directory after closing the report. No system service or root installation is required. A foreground browser process can be run under the user's preferred session manager; it is not needed for capture.

## Remaining product stages

The Linear OAuth and MCP publisher are implemented, but sign-in against a real Linear account and a real issue creation have not been validated in this environment. Do not describe the integration as connected or an item as filed without a confirmed issue URL. Live token telemetry, fully evidenced repeated-read/check detection, contextual suggestions, and learning remain separate milestones. The schemas retain provenance and feedback to support them. One real CLI model task verified native scope denial before an edit. Broader desktop/client coverage, human usability studies, and controlled savings experiments remain pending; replay and discovery checks are separate evidence. Do not advertise planned thresholds as measured results.
