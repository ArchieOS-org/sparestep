# Sparestep

Tell Codex what you want finished. Sparestep helps it stay on that task and saves worthwhile side work to Linear.

## Install on Linux

```sh
curl -fsSL https://raw.githubusercontent.com/ArchieOS-org/sparestep/main/install.sh | sh
```

No root, compiler, or desktop needed. The installer verifies the Linux x86_64 or arm64 binary and adds the Codex skill. [Release downloads](https://github.com/ArchieOS-org/sparestep/releases).

## One command

In a Codex task for your project:

```text
/sparestep Fix the login redirect
```

Codex records what needs to work, stays within the agreed components, and runs the relevant checks. Sparestep adds no model calls to supervise each action.

- **Necessary extra work:** Codex explains why the scope must expand. The change stays visible in the report.
- **Useful but optional work:** saved for Linear while Codex continues your original task.
- **Finished:** required checks and review evidence are recorded. Missing evidence stays incomplete.

Ordinary Codex tasks are unaffected until you explicitly invoke Sparestep.

## Already making a plan?

Use Sparestep while planning, or invoke it after you have a plan. Planning stays in the conversation. Leave Plan mode and say **“implement the agreed plan”**; Codex uses that plan without making you start over.

You can also begin with `/sparestep Implement the agreed plan above`.

## See what is happening

```text
/sparestep status          Show the task and saved side work
/sparestep open            Open the optional GUI
/sparestep pause           Pause Sparestep
/sparestep resume          Resume it
```

The GUI shows the task, completion conditions, scope changes, and deferred issues. **Queued means saved locally. Filed means a confirmed Linear issue link exists.**

## Two one-time setup steps

The first invocation installs the project hooks. Codex may need one new task to load them; Sparestep tells you the next step. “Waiting for native guards” means protection has not yet been observed.

To enable automatic issue filing:

```text
/sparestep linear connect
```

Sign in through Linear once. No API key needed. For Dispatch, Sparestep defaults to `Dispatch v1 — Duncan feedback`, in `Backlog`, and validates the destination and required labels before filing. Other repositories need a destination once. If delivery fails, the work stays saved.

## On a Linux VM

Install it where Codex runs, including the Dispatch checkout or worktree you are using. Slash commands work without a GUI. To view the GUI or complete browser sign-in from another computer, use the SSH forwarding instructions Sparestep prints. Records remain on the VM; closing the GUI does not stop capture.

## Its limits

Supported native file edits can be blocked before they leave the agreed scope. Arbitrary shell programs, remote tools, and semantic detours within an allowed component are not fully guarded. Plan-mode behavior also depends on Codex following the explicit skill instructions. This is not a guarantee that an agent cannot drift.

Sparestep does not weaken required repository checks, claim measured token savings, or learn and rewrite its own instructions. Automatic learning is left for later.

[Implementation and limits](docs/DEVELOPMENT.md) · [What was tested](docs/VALIDATION.md) · [Product plan](docs/PLAN.md)

This fork retains the upstream [license](LICENSE); it has not been relicensed.
