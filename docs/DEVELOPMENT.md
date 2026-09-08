# Developing Sparestep

The first preview replaces the upstream scorecard and bundled utilities with a small Linux application. Upstream history is retained at tag `upstream-c490db4`. The upstream license and notices remain in effect.

## Build and check

Use Go 1.27.1 for the release build:

```sh
go test -race ./...
go vet ./...
sh scripts/build-release.sh
```

The release script builds static Linux amd64 and arm64 binaries and their SHA-256 manifest. Users do not need Go, Node, SQLite CLI, or a desktop. The interface is embedded in the executable.

Additional integration checks:

```sh
python3 scripts/check-capture.py --binary dist/sparestep-linux-amd64
python3 scripts/check-native-discovery.py --binary dist/sparestep-linux-amd64
python3 scripts/check-onboarding.py --binary dist/sparestep-linux-amd64
npm ci
npm run test:browser
```

Native discovery requires Codex CLI and uses an isolated temporary configuration without starting a model task. Browser checks require Chrome (`CHROME_PATH` overrides its location). Node and Playwright are developer tools only. See [validation](VALIDATION.md) for what was actually tested.

## Architecture

- `internal/hooks`: bounded Codex event normalization and reversible project connection.
- `internal/codexsetup`: scoped setup through Codex's public hook catalog and configuration API.
- `skills`: embedded, explicit Sparestep skill and safe local installation.
- `internal/store`: SQLite observations, dispositions, drafts, reports, and conservative detection.
- `internal/model`: versioned observation/report boundaries.
- `internal/app`: terminal guide, commands, hook runner, browser listener, and isolated examples.
- `internal/web`: authenticated loopback interface and issue-draft handoff.

The browser server is optional. Hooks append directly to SQLite so recording does not depend on the browser process. A failed recorder returns an empty hook response and leaves a local diagnostic when storage permits. It never blocks tools, rewrites commands, or forces a continuation.

## Evidence boundaries

Codex's actual project config must load and its hooks must be trusted. The skill invokes `start`, which installs only Sparestep's project definitions and registers their exact hashes through the same native configuration API used by Codex's hook review. It preserves unrelated hooks and disabled settings. A separate setup process cannot prove that an already-open task reloaded its configuration; setup may ask the user to open a new task once. Unsupported clients or untrusted project layers retain a specific native setup step. The lower-level `connect` command still writes definitions only. `doctor` reports whether events have actually reached the recorder. A successful configuration write is not successful capture.

The hook source may omit exit status. Printed output, printed JSON, and statements such as “done” do not turn unknown results into successes. Observed command durations and time between hook events have different provenance; the latter includes hook overhead and scheduling. The report never calls their sum guaranteed wall-clock savings.

In particular, native Bash/local tools normally send model-facing output in `tool_response`, which may be plain text. The adapter deliberately does not infer an exit code from that text. It accepts a typed `exit_code` when a sender supplies one, or a structured MCP `isError` response. The latter can classify a bounded known error signature from MCP text without storing that text. Ordinary shell failures can remain invisible to findings until a reliable result adapter is connected. See the [official PostToolUse contract](https://learn.chatgpt.com/docs/hooks#posttooluse).

The initial live detector focuses on recurring known failure signatures. Repeated successful checks/reads require `ContextKnown` and a nonempty matching state key. Normal hook capture does not assert this, because it cannot establish all source, environment, instruction, and compaction context. Future source adapters can supply this evidence after validation.

There is no actual token source connected in this release. The model/store boundary preserves source-tagged usage for future adapters. Do not estimate tokens from weighted calls or output length and label that measured usage.

## Local access and privacy

The server listens only on `127.0.0.1`, generates a per-process access token, and prints a fragment-based browser URL. The browser exchanges it for a local session and removes the fragment. Write requests require a matching session/CSRF/origin or the bearer token. Treat the printed access URL as local access credentials.

On a VM, forward the chosen loopback port over SSH. The printed `USER@VM` is a placeholder for the user's SSH host. If the local port on the user's computer is busy, choose another local port in `ssh -L` and use that port in the browser address. The recorder and database stay on the execution host.

Do not upload complete transcripts. Hook storage uses bounded, conservative command descriptions and error signatures. Review a draft before sharing it: project names and issue descriptions can still be private. Linear receives only the edited form content when the user opens it. No issue-creation API, credentials, or model provider are configured by this application.

## Installation and removal

`install.sh` accepts `SPARESTEP_VERSION`, `SPARESTEP_INSTALL_DIR`, and a local `SPARESTEP_RELEASE_DIR` for offline installs/testing. It installs the executable and the embedded skill. The skill lives in `~/.agents/skills/sparestep` by default, or `$CODEX_HOME/skills/sparestep` when that configuration directory is explicitly set. `sparestep install-skill --skill-dir FOLDER` selects an alternate final skill directory. Unowned files are preserved; managed updates keep a first backup. Upgrades keep recordings and feedback.

`start` and `open` reuse an authenticated loopback report process per worktree. A short local file lock prevents duplicate launches. Private readiness files and logs live in the state folder's `reports` directory. No startup service is registered. Opening a report after reboot recreates its process. Other commands resolve the current Git worktree too; the hook recorder uses its configured project without running Git on each event.

Disconnect each configured project before removing the executable. Disconnect preserves other hooks and saved history. To erase history, remove the dedicated state directory after closing the report. No system service or root installation is required. A foreground browser process can be run under the user's preferred session manager; it is not needed for capture.

## Remaining product stages

Live token telemetry, fully evidenced repeated-read/check detection, automatic Linear submission, contextual suggestions, and learning remain separate milestones. The schemas retain provenance and feedback to support them. Human usability studies and controlled savings experiments have not been performed; do not advertise their planned thresholds as measured results.
