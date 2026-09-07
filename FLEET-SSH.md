# Shared outbound fleet SSH limits

Disabled at the user's request. The active SSH config no longer includes this
guard, and the full installer no longer activates it. The implementation below
is retained as an inactive reference.

The local OpenSSH configuration sends BoomPay and CISL2/A250 fleet connections
through `fleet-ssh-guard.py`. All covered callers on this Mac share one budget.
This includes `/usr/bin/ssh`, SCP, SFTP, and Docker's SSH transport when they use
the normal user SSH configuration.

## Policy

`fleet-ssh-policy.json` owns the limits and the known public fleet IPs.
It covers ten existing SSH aliases and eighteen addresses: the addresses in
the local SSH config plus the five-node controllers' pinned runtime host files.
Older addresses remain covered because existing aliases still refer to them.
The installer does not change those aliases' destinations or host keys.

| Local limit | Value |
| --- | --- |
| Active connections across both fleets | 4 |
| Minimum interval between connection starts across both fleets | 5 seconds |
| TCP connection attempts per admitted invocation | 1 |
| Reserved authentication failures per unconfirmed connection | 3 |
| Failure threshold per destination | 5 within 600 seconds |
| TCP and OpenSSH connection setup timeout | 3 seconds |
| Server login grace allowance | 30 seconds |
| Admission wait | None; unavailable capacity fails immediately |

These are local operating limits. Both repositories configure `MaxAuthTries 3`,
`MaxSessions 4`, `MaxStartups 10:30:60`, and Fail2ban with `maxretry = 5` and
`findtime = 10m` in their SSH Salt states. Those files describe intended server
configuration; they do not prove the current settings on every server.

`MaxSessions` limits channels within one SSH connection. `MaxStartups` limits
unauthenticated connections; it is not a starts-per-second setting. The local
four-connection cap and five-second spacing are conservative operating choices,
not a reinterpretation of those server settings. See the
[OpenSSH server manual](https://man.openbsd.org/sshd_config).

Each connection reserves three possible authentication failures before dialing.
An authenticated SSH client clears its reservation through `LocalCommand`.
An unsuccessful or interrupted connection keeps its reservation for 633 seconds
from admission: the ten-minute failure window plus connection and login grace.
A second unconfirmed connection to the same IP would exceed the remaining
failure budget, so the guard exits immediately without connecting. The failure
window is retained bookkeeping, not a sleep or command timeout.

The proxy cannot inspect encrypted authentication messages. This reservation
policy limits repeated unsuccessful connections; it does not measure failed key
offers inside an eventually successful authentication. Use the correct explicit
identity and retain `IdentitiesOnly yes`.

Kernel file locks hold the active slots. A shared state transaction controls
admission and start timing across processes. A terminated proxy releases its slot;
its unconfirmed attempt remains recorded. Missing or invalid policy and corrupt
state cause an error before a connection can start.

## Install and verify

Run `python3 scripts/install-fleet-ssh-guard.py` to install this component.
The full `./install.sh` also installs it. The tally-only installer leaves it alone.

The installer validates the effective configuration with `ssh -G` for all covered
IPs and aliases. This check opens no network connections. It preserves unrelated
host entries and saves the original config as
`~/.ssh/config.before-fleet-ssh-guard` on the first change.

Run these local regression checks:

```sh
python3 scripts/test-fleet-ssh-guard.py
python3 scripts/test-fleet-ssh-installer.py
```

The installed guard's `status` command shows its policy and pending attempts.
The guard never writes protocol diagnostics to standard output during proxy use.

On September 7, 2026, eleven limiter tests and four installer tests passed.
Read-only `true` commands succeeded through the installed guard on BoomPay's
`138.197.172.189` and A250's `137.184.166.201`. Both authentication callbacks
cleared their reservations. The initial version waited for shared pacing;
the current policy returns immediately when the next start is not yet allowed.
The installed guard rejected a held state lock in 0.052 seconds without dialing.

The older control aliases still resolve to `142.93.158.204` and `146.190.242.198`.
Their checks respectively timed out and failed host-key verification. Those
attempts remained reserved; no host key was replaced and no failed budget was reset.

## Scope

This is a user-level OpenSSH guard on this Mac. It does not control another
machine sharing the public source IP, an existing connection, a non-OpenSSH
client, or a command that overrides the SSH config or proxy. It is not a kernel
firewall. Do not use `-F`, `ProxyCommand`, `ProxyJump`, or multiplexing overrides
to bypass it for a covered fleet. New fleet addresses must enter the policy before
use. No server firewall or SSH authentication settings are changed.
