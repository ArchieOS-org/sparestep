# Deployment hook audit — September 7, 2026

The repeated `Hook failed` reports combined a delivery failure with a missing result handoff. The old tally report ran before shipping and could report high activity while deployment remained incomplete. A Stop hook is an automatic end-of-turn command. It is not an approval requirement or evidence that production changed.

## Findings and repairs

| Finding | Evidence | Result |
| --- | --- | --- |
| Shipping ended after tally had reported. Tally did not receive the final deployment result. | Native Stop runs tally before ship-it. `internal/app/app.go` in ship-it now calls the tally reporter after each repository operation. | Native delivery records FAILED or a verified success. Failure output includes the error and diagnostic log path. |
| A failed deployment caused hook exit 1 even when the failure could be reported through the hook protocol. | The shipping cycle propagated its operation error directly to the hook runner. | A correctly recorded failure returns valid hook JSON and exit 0. The outcome remains FAILED. If delivery and its reporter both fail, the process still returns nonzero. |
| A clean checkout suppressed deployment retries. | The repeated Stop check only looked for unpublished Git changes. A successful push followed by failed deployment leaves Git clean. | One continuation retry is permitted for the failed roots. Successful roots are skipped unless they have new work. An unchanged repeat does not deploy again. |
| Blocker claims could disappear across turns or be outweighed by activity. | The former result was based on per-turn tool state; assistant terminal claims were unused. | A session/project ledger retains unresolved obligations. Abandonment gets FAILED and outcome grade 0. A bounded Stop response asks for evidence and recovery. Only observed failure followed by matching success earns RECOVERED / INCREDIBLE WIN. |
| Current native command hooks omit exit status. | A controlled local probe showed Bash PostToolUse `tool_response` containing stdout only. The structured result visible to the model belongs to another layer. | Stdout-only results remain unknown and the report explains why. Native shipping sends its actual result directly. Printed PASS or JSON is not promoted to success. |
| An older successful receipt could conceal newer work. | A successful operation can finish while the checkout changes. | Success must match a clean current HEAD before clearing delivery. Ordered native receipt IDs reject older results and malformed IDs. |
| Removal guidance pointed only to agent-file-guard recovery. | `internal/fileguard/hook.go`, `removalAdvice`. | The installed guidance recommends macOS Move to Trash, or the host OS Recycle Bin, through a permitted native interface. It covers files and directories. |

## Rules and remaining constraints

These are separate from the repaired result handoff. They should be considered explicitly when deciding deployment policy.

| Source | Exact rule or behavior | Practical effect |
| --- | --- | --- |
| `~/.codex/AGENTS.md:21` | “Agents finish their work and let the hooks run. Do not invoke ship-it, poll it, or add shipping prompts.” | Routine Git delivery belongs to native hooks. An agent should repair and verify the repository command, then receive the native outcome. This rule is not permission to abandon a failed deployment. |
| `~/.codex/AGENTS.md:24` | “Report a blocker only with the attempted command, observed failure, and the specific missing access, information, or external change.” | A prior failed attempt alone does not establish a blocker. |
| `~/.codex/skills/ship-it/SKILL.md` | A tracked `.deploy-it.json` selects the deployment handoff after a successful push. | A push proves Git delivery. The repository deployment command must verify its destination before reporting deployment success. |
| `~/dev/boompay-vps-infra-l2/scripts/deployment-lock.sh:20` | Stale recovery immediately returns when the owner file is missing or empty. | An empty orphaned lock is never recovered by this path. |
| Same file, lines 97–101 | Release deletes the owner file, then suppresses failure to remove the lock directory. | A directory-removal denial can leave an empty lock. Later acquisition says another change is running without an owner record proving that claim. |
| Same file, acquisition loop | Default lock wait is 1500 seconds. | A shorter enclosing deployment timeout can terminate the command before this loop reports its own diagnosis. |
| Active filesystem permission profile | Access to `.Trash` and `.Trashes` paths is denied. Directory removal/rename also returned `operation not permitted` in this session. | Recommending the host Trash does not grant access through this runtime. The permission boundary remains enforced. No Trash access or production-lock removal was performed by this repair. |
| `internal/fileguard/hook.go`, destructive-command regex | The guard scans command text for deletion tokens, including quoted text. | A read-only search or test containing a literal deletion command can be denied. The guard's permission logic was not changed by the wording update. |

The reported production lock is `~/.local/share/boompay-vps-infra-l2-production-controller/runtime/.production-deployment-lock`. A read-only recheck at the end of this repair confirmed that it still exists, contains zero entries, and has no owner record. The reviewed controller code explains the empty-lock failure. This hook repair does not provide a newer production deployment receipt.

## Verification

- Tally root package tests pass, including abandonment, cross-turn recovery, stdout-only results, stale receipts, new local edits, and shipping/deployment identity.
- All ship-it Go packages pass, including legacy-hook compatibility and deployment failure propagation.
- File-guard runtime tests pass for removal guidance, allowed commands, hook protocols, and protected path handling.
- `scripts/test-native-delivery.py` runs compiled binaries against isolated local Git remotes and a simulated deployment command. It verifies failure, one clean continuation retry, recovery, immutable commit identity, no duplicate deployment, independent repository retries, and visible reporter failures.

Tests on this host use `GOFLAGS=-work` and `SHIP_IT_KEEP_TEST_DIRS=1` because directory cleanup is denied. Assertions run normally; temporary fixture directories are retained. Native delivery receipts are a cooperating local executable protocol, not a security boundary against another process running as the same OS user.
