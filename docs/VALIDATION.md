# v0.1.0 preview validation

Validated on September 8, 2026. This is an implementation preview, with no measured savings claim.

## Completed checks

- Go tests with the race detector across all packages, including event attribution, conservative findings, task status, redaction, hook preservation, durable feedback, and authenticated browser writes.
- `go vet ./...`.
- Release binaries cross-built with Go 1.27.1 for Linux amd64 and arm64, `CGO_ENABLED=0`; both inspected as statically linked ELF executables.
- amd64 execution on an Ubuntu 26.04 x86_64 host and installation/report execution as a non-root user in a Debian 12-based container with networking disabled and a read-only root filesystem.
- Real Codex CLI 0.153.4 configuration discovery: all six installed hook definitions are visible; native trust remains required; disconnect removes them. This check changes only an isolated temporary configuration and runs no model task.
- Supported hook payload replay through the actual executable: recurring failure grouping, command-argument redaction, duplicate suppression, persistent pause, concurrent recording processes, unknown outcomes, and Markdown drafts.
- Browser interactions against the release executable in headless Chrome: access-token exchange/removal, authenticated reload, necessary/dismiss/undo, edited draft download and Linear handoff URL, saved existing issue link, pause/resume, and a 390 px mobile viewport without horizontal overflow. No Linear issue was submitted.
- The README screenshot comes from the running application with isolated, labeled example data.

A 34-process capture replay on the development host measured approximately 4.2 ms median and 5.6 ms p95 per recorder invocation. This small sample includes startup and some concurrent contention. It is not a production latency guarantee or a measurement of saved time or tokens.

## Not yet validated or implemented

- No fresh VM boot/reboot campaign or arm64 hardware execution was performed. The headless checks cover host and container execution; arm64 is cross-built only.
- Native hook discovery and payload replay are tested separately. End-to-end recording during a real model task still needs field validation after the user trusts the hooks. Changes in Codex payload shapes can reduce coverage; missing results remain unknown.
- Native shell responses supplied as plain text are not parsed for success/failure. The recurring-failure replay uses supported typed metadata; it does not demonstrate recognition of ordinary text-only shell failures. Structured MCP errors are supported.
- Actual token telemetry, automatic Linear submission, contextual instruction suggestions, and automatic learning are not connected.
- Live hooks do not establish the unchanged context required for repeated successful check/read findings; those detectors abstain without it.
- Human onboarding studies, accessibility audits with assistive technology, controlled false-positive studies, and savings experiments remain planned work.

The original roadmap is in [PLAN.md](PLAN.md). Its product targets are not measured results.
