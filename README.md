# Sparestep

**Help Codex do less busywork.**

Sparestep records your Codex activity and explains recurring problems worth fixing. Read a short report, tell it when work was necessary, and turn a useful finding into an editable Linear issue draft.

It runs locally on Linux, including a VM accessed only through SSH. Recording makes no AI calls.

![An example Sparestep report showing a recurring setup failure](docs/images/sparestep-example.png)

## Start with one command

Download the Linux installer from [the release](https://github.com/ArchieOS-org/sparestep/releases/tag/v0.2.0), then run it once:

```sh
sh install.sh
```

In a Codex task for your project, type `/`, choose **Sparestep**, and send:

```text
/sparestep
```

Sparestep connects that project and opens its report. It handles its own hook setup through Codex's native configuration API. If your current task predates the connection, open a new Codex task once to load it. An unsupported or disabled setup gets one specific next step.

Then use Codex normally. Use `/sparestep` again whenever you want the report; it reuses the existing report and preserves pause state.

In Codex CLI, invoke the same skill with `$sparestep`. If it is missing from the selector, reopen Codex once. The installer adds the skill as well as the executable; it needs `curl` and `sha256sum`, with no root, compiler, or database server.

## Stay in control

| In Codex | What happens |
| --- | --- |
| `/sparestep` | Connect this worktree and open its report |
| `/sparestep status` | See whether activity has actually been recorded |
| `/sparestep pause` | Pause recording until you resume |
| `/sparestep resume` | Resume recording |
| `/sparestep disconnect` | Remove this worktree's connection; keep saved reports |

Sparestep uses the current Git worktree, even when you work in a subfolder. Each separate worktree has its own connection and report. The skill runs only when requested; recording itself makes no model calls. Invoking the skill uses an ordinary Codex turn.

## Using a Linux VM?

Run Sparestep on the VM where Codex runs. Everything works in the SSH terminal.

The skill returns a real report link and opens it in Codex's browser panel when that capability is available. If your browser is on another computer without automatic port forwarding, forward the report's port over SSH. The VM needs no browser, desktop, GPU, or public web port. Records stay on the VM.

The report process survives the command that opened it. After a VM reboot, `/sparestep` opens it again. Closing a browser tab does not stop recording. Sparestep does not keep Codex itself alive after an SSH disconnect or reboot.

## What the preview can tell you

- A known setup or tool failure happened across multiple tasks.
- Which supported checks returned a known pass, failure, or unknown result.
- The recorded duration of attempts, when available.

Native shell results supplied only as text remain **unknown**. Recurring findings need structured failure evidence, such as an MCP tool's error response. This preview will therefore miss many ordinary shell failures.

Repeated-check and repeated-read detectors require reliable unchanged-context evidence. Ordinary Codex hooks do not provide all of that context, so those detectors abstain on incomplete observations. A repeated command alone is not proof of waste.

**Actual token totals are unavailable from this initial hook connection.** Hosted tools and some remote activity are also outside its coverage. It does not claim proven savings, correct code, or successful deployment from a passing command.

## Your feedback becomes useful work

Choose **Review issue draft**, edit the title and description, then download Markdown or open the draft in Linear. Nothing is submitted automatically. You can link an existing Linear issue instead. Opening Linear is never reported as a created issue.

## Prefer the terminal?

```sh
sparestep start             # Connect this worktree and get its report link
sparestep                   # Read the terminal report and review findings
sparestep demo              # Explore isolated, labeled example data
sparestep serve --demo      # Explore the browser with example data
sparestep prune --days 30   # Remove older observations
```

State lives under `~/.local/state/sparestep`, or `$XDG_STATE_HOME/sparestep`. Use `--state-dir` or `SPARESTEP_STATE_DIR` for a different location. Project options go before finding IDs; `sparestep help` shows examples.

Automatic learning is reserved for a later version, with review, a budget, and undo.

For development and exact limitations, see [the developer guide](docs/DEVELOPMENT.md), [preview validation](docs/VALIDATION.md), and [the product plan](docs/PLAN.md). This fork retains the upstream [license](LICENSE); it has not been relicensed.
