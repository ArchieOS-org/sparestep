#!/usr/bin/env python3
"""A small, shared admission guard for fleet SSH ProxyCommands.

The command line intentionally has no knobs: production policy is always read
from ~/.config/fleet-ssh-guard/policy.json.  The functions below accept a
policy and a state directory so their concurrency behaviour can be tested
without touching that location.
"""

import errno
import fcntl
import ipaddress
import json
import math
import os
from pathlib import Path
import selectors
import socket
import sys
import tempfile
import time
import re


POLICY_PATH = Path.home() / ".config" / "fleet-ssh-guard" / "policy.json"
BUFFER_LIMIT = 1024 * 1024
NUMBER_LIMIT = 86400.0


class GuardError(RuntimeError):
    pass


def _number(value, name, upper=NUMBER_LIMIT, allow_zero=False):
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        raise GuardError(f"policy {name} must be a number")
    value = float(value)
    if not math.isfinite(value) or value > upper or value < 0 or (not allow_zero and value == 0):
        raise GuardError(f"policy {name} is outside its allowed range")
    return value


def validate_policy(raw):
    """Return a normalized policy, rejecting malformed or surprising input."""
    if not isinstance(raw, dict) or set(raw) != {
        "version", "max_connections", "min_interval_seconds",
        "failure_window_seconds", "max_auth_tries", "max_failures",
        "connect_timeout_seconds", "login_grace_seconds", "wait_timeout_seconds", "targets",
    }:
        raise GuardError("policy has an invalid schema")
    if raw["version"] != 1:
        raise GuardError("policy version must be 1")
    integer_names = ("max_connections", "max_auth_tries", "max_failures")
    result = {"version": 1}
    for name in integer_names:
        value = raw[name]
        if isinstance(value, bool) or not isinstance(value, int) or not 1 <= value <= 9:
            raise GuardError(f"policy {name} must be an integer from 1 through 9")
        result[name] = value
    if result["max_auth_tries"] >= result["max_failures"]:
        raise GuardError("max_auth_tries must be below max_failures")
    for name in ("min_interval_seconds", "failure_window_seconds", "login_grace_seconds"):
        result[name] = _number(raw[name], name)
    result["connect_timeout_seconds"] = _number(raw["connect_timeout_seconds"], "connect_timeout_seconds", upper=5)
    result["wait_timeout_seconds"] = _number(raw["wait_timeout_seconds"], "wait_timeout_seconds", upper=5, allow_zero=True)
    targets = raw["targets"]
    if not isinstance(targets, dict) or not targets:
        raise GuardError("policy targets must be a nonempty mapping")
    normalized_targets, aliases = {}, {}
    for address, target_aliases in targets.items():
        if not isinstance(address, str) or not address:
            raise GuardError("policy target must be a nonempty numeric IP")
        try:
            parsed = ipaddress.ip_address(address)
        except ValueError as error:
            raise GuardError("policy target must be a numeric IP") from error
        canonical = str(parsed)
        if canonical != address or canonical in normalized_targets:
            raise GuardError("policy target IP must be canonical and unique")
        if not isinstance(target_aliases, list) or not all(isinstance(alias, str) and re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_.-]*", alias) for alias in target_aliases):
            raise GuardError("policy target aliases must be nonempty strings")
        normalized_targets[canonical] = tuple(target_aliases)
    for canonical, target_aliases in normalized_targets.items():
        for alias in target_aliases:
            if alias in aliases or alias in normalized_targets:
                raise GuardError("policy aliases must be unique and cannot be target IPs")
            aliases[alias] = canonical
    result["targets"] = normalized_targets
    result["aliases"] = aliases
    return result


def load_policy(path=POLICY_PATH):
    try:
        with Path(path).open("r", encoding="utf-8") as handle:
            return validate_policy(json.load(handle))
    except (OSError, json.JSONDecodeError, TypeError) as error:
        raise GuardError(f"cannot read policy: {error}") from error


def resolve_target(policy, host):
    if not isinstance(host, str) or not host:
        raise GuardError("target must be nonempty")
    if host in policy["targets"]:
        return host
    try:
        return policy["aliases"][host]
    except KeyError as error:
        raise GuardError("target is not in the numeric IP allowlist") from error


def _state_path(directory):
    return Path(directory) / "state.json"


def _read_state(directory):
    path = _state_path(directory)
    if not path.exists():
        return {"last_start": None, "dialing": None, "pending": []}
    try:
        with path.open("r", encoding="utf-8") as handle:
            state = json.load(handle)
        if set(state) != {"last_start", "dialing", "pending"}:
            raise ValueError("invalid state schema")
        if state["last_start"] is not None and (isinstance(state["last_start"], bool) or not isinstance(state["last_start"], (int, float)) or not math.isfinite(state["last_start"])):
            raise ValueError("invalid last start")
        if state["dialing"] is not None and (isinstance(state["dialing"], bool) or not isinstance(state["dialing"], (int, float)) or not math.isfinite(state["dialing"])):
            raise ValueError("invalid dial state")
        if not isinstance(state["pending"], list):
            raise ValueError("invalid pending state")
        for entry in state["pending"]:
            if (set(entry) != {"pid", "host", "at", "weight"} or isinstance(entry["pid"], bool)
                    or not isinstance(entry["pid"], int) or entry["pid"] < 1
                    or not isinstance(entry["host"], str) or not entry["host"]
                    or isinstance(entry["at"], bool) or not isinstance(entry["at"], (int, float)) or not math.isfinite(entry["at"])
                    or isinstance(entry["weight"], bool) or not isinstance(entry["weight"], int) or entry["weight"] < 1):
                raise ValueError("invalid pending entry")
        return state
    except (OSError, ValueError, TypeError, json.JSONDecodeError) as error:
        raise GuardError(f"cannot read shared state: {error}") from error


def _write_state(directory, state):
    directory = Path(directory)
    try:
        fd, temporary = tempfile.mkstemp(prefix="state-", suffix=".json", dir=directory)
        with os.fdopen(fd, "w", encoding="utf-8") as handle:
            json.dump(state, handle, separators=(",", ":"), allow_nan=False)
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(temporary, _state_path(directory))
    except (OSError, TypeError, ValueError) as error:
        try:
            os.unlink(temporary)
        except (OSError, UnboundLocalError):
            pass
        raise GuardError(f"cannot write shared state: {error}") from error


class _StateLock:
    def __init__(self, directory, deadline=None):
        self.directory = Path(directory)
        self.deadline = deadline
        self.handle = None

    def __enter__(self):
        try:
            self.directory.mkdir(mode=0o700, parents=True, exist_ok=True)
            self.handle = (self.directory / "state.lock").open("a+")
            while True:
                try:
                    fcntl.flock(self.handle.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
                    break
                except BlockingIOError:
                    if self.deadline is None or time.monotonic() >= self.deadline:
                        raise GuardError("shared state is busy")
                    time.sleep(min(0.01, max(0.0, self.deadline - time.monotonic())))
            return self
        except GuardError:
            self.close()
            raise
        except OSError as error:
            self.close()
            raise GuardError(f"cannot lock shared state: {error}") from error

    def close(self):
        if self.handle is not None:
            self.handle.close()
            self.handle = None

    def __exit__(self, *_):
        self.close()


class _Slot:
    def __init__(self, directory, count):
        self.directory, self.count, self.handle = Path(directory), count, None

    def acquire(self):
        try:
            self.directory.mkdir(mode=0o700, parents=True, exist_ok=True)
            for index in range(self.count):
                handle = (self.directory / f"slot-{index}").open("a+")
                try:
                    fcntl.flock(handle.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
                    self.handle = handle
                    return True
                except BlockingIOError:
                    handle.close()
            return False
        except OSError as error:
            raise GuardError(f"cannot acquire connection slot: {error}") from error

    def close(self):
        if self.handle is not None:
            self.handle.close()
            self.handle = None


def _prune(state, now, window):
    state["pending"] = [entry for entry in state["pending"] if now - entry["at"] < window]


def _reservation_horizon(policy):
    return policy["failure_window_seconds"] + policy["connect_timeout_seconds"] + policy["login_grace_seconds"]


def _rebase_future_times(state, policy, now):
    """A reboot resets monotonic time; preserve a conservative short cooldown."""
    changed = False
    if state["last_start"] is not None and state["last_start"] > now:
        state["last_start"] = now
        changed = True
    if state["dialing"] is not None and state["dialing"] > now:
        state["dialing"] = now
        changed = True
    for entry in state["pending"]:
        if entry["at"] > now:
            entry["at"] = now
            changed = True
    return changed


def _remove_reservation(directory, policy, pid, host):
    with _StateLock(directory):
        state = _read_state(directory)
        now = time.monotonic()
        _rebase_future_times(state, policy, now)
        _prune(state, now, _reservation_horizon(policy))
        state["pending"] = [entry for entry in state["pending"] if not (entry["pid"] == pid and entry["host"] == host)]
        _write_state(directory, state)


class Admission:
    def __init__(self, sock, slot, directory, policy, pid, host):
        self.sock, self.slot = sock, slot
        self.directory, self.policy, self.pid, self.host = directory, policy, pid, host
        self.connected = False

    def reject_connection(self):
        self.sock.close()
        self.slot.close()

    def close(self):
        self.sock.close()
        self.slot.close()


def admit_connection(policy, host, port, directory, parent_pid=None, deadline=None):
    """Reserve a weighted SSH handshake and start one nonblocking TCP connect."""
    if parent_pid is None:
        parent_pid = os.getppid()
    target = resolve_target(policy, host)
    if deadline is None:
        deadline = time.monotonic() + policy["wait_timeout_seconds"]
    while True:
        if os.getppid() != parent_pid:
            raise GuardError("SSH parent exited before connection")
        slot = _Slot(directory, policy["max_connections"])
        if not slot.acquire():
            if time.monotonic() >= deadline:
                raise GuardError("connection slot unavailable")
            time.sleep(0.02)
            continue
        try:
            keep_slot = False
            with _StateLock(directory, deadline):
                now = time.monotonic()
                state = _read_state(directory)
                _rebase_future_times(state, policy, now)
                _prune(state, now, _reservation_horizon(policy))
                last_start = state["last_start"]
                if state["dialing"] is not None:
                    # A previous process may have died between its reservation
                    # and connect_ex.  Convert that unknown dial into a fresh
                    # full interval instead of allowing a catch-up burst.
                    state["last_start"] = now
                    state["dialing"] = None
                    _write_state(directory, state)
                    reason = f"connection start is incomplete; retry in {policy['min_interval_seconds']:.2f}s"
                    wait_for = policy["min_interval_seconds"]
                else:
                    allowed_at = now if last_start is None else max(now, last_start + policy["min_interval_seconds"])
                    used = sum(entry["weight"] for entry in state["pending"] if entry["host"] == target)
                    if allowed_at > now or used + policy["max_auth_tries"] >= policy["max_failures"]:
                        _write_state(directory, state)
                        if allowed_at > now:
                            wait_for = max(0.02, allowed_at - now)
                            reason = f"connection pacing active; retry in {allowed_at - now:.2f}s"
                        else:
                            wait_for = 0.02
                            reason = "authentication failure budget is reserved"
                    else:
                        state["pending"].append({"pid": parent_pid, "host": target, "at": now, "weight": policy["max_auth_tries"]})
                        state["last_start"] = None
                        state["dialing"] = now
                        _write_state(directory, state)
                        family = socket.AF_INET6 if ":" in target else socket.AF_INET
                        sock = socket.socket(family, socket.SOCK_STREAM)
                        sock.setblocking(False)
                        address = (target, port, 0, 0) if family == socket.AF_INET6 else (target, port)
                        status = sock.connect_ex(address)
                        state["dialing"] = None
                        state["last_start"] = time.monotonic()
                        _write_state(directory, state)
                        if status not in (0, errno.EINPROGRESS, errno.EWOULDBLOCK, errno.EALREADY):
                            sock.close()
                            raise GuardError(f"TCP connection failed: {os.strerror(status)}")
                        admission = Admission(sock, slot, directory, policy, parent_pid, target)
                        keep_slot = True
                        return admission
        finally:
            if not keep_slot and slot.handle is not None:
                slot.close()
        if time.monotonic() >= deadline:
            raise GuardError(reason)
        time.sleep(min(wait_for, max(0.0, deadline - time.monotonic())))


def authenticated(policy, host, directory, parent_pid=None):
    target = resolve_target(policy, host)
    _remove_reservation(directory, policy, os.getppid() if parent_pid is None else parent_pid, target)


def relay(admission, parent_pid, connect_timeout, input_fd=0, output_fd=1):
    """Relay raw bytes and preserve both directions' half-close semantics."""
    sock, selector = admission.sock, selectors.DefaultSelector()
    sock.setblocking(False)
    os.set_blocking(output_fd, False)
    incoming, outgoing = bytearray(), bytearray()
    stdin_eof = remote_eof = connected = write_shutdown = output_closed = False
    deadline = time.monotonic() + connect_timeout
    try:
        while True:
            if os.getppid() != parent_pid:
                raise GuardError("SSH parent exited")
            if connected and stdin_eof and not incoming and not write_shutdown:
                try:
                    sock.shutdown(socket.SHUT_WR)
                except OSError:
                    pass
                write_shutdown = True
            events = 0
            if not connected or incoming:
                events |= selectors.EVENT_WRITE
            if connected and len(outgoing) < BUFFER_LIMIT and not remote_eof:
                events |= selectors.EVENT_READ
            if events:
                selector.register(sock, events, "socket")
            if connected and not stdin_eof and len(incoming) < BUFFER_LIMIT:
                selector.register(input_fd, selectors.EVENT_READ, "input")
            if outgoing and not output_closed:
                selector.register(output_fd, selectors.EVENT_WRITE, "output")
            ready = selector.select(0.1)
            for key in list(selector.get_map().values()):
                selector.unregister(key.fileobj)
            if not connected and time.monotonic() >= deadline:
                raise GuardError("TCP connect timed out")
            for key, mask in ready:
                if key.data == "socket":
                    if not connected and mask & selectors.EVENT_WRITE:
                        error = sock.getsockopt(socket.SOL_SOCKET, socket.SO_ERROR)
                        if error:
                            raise GuardError(f"TCP connection failed: {os.strerror(error)}")
                        connected = admission.connected = True
                    if connected and mask & selectors.EVENT_READ:
                        try:
                            data = sock.recv(min(65536, BUFFER_LIMIT - len(outgoing)))
                        except BlockingIOError:
                            data = None
                        if data == b"":
                            remote_eof = True
                        elif data:
                            outgoing.extend(data)
                    if connected and mask & selectors.EVENT_WRITE and incoming:
                        try:
                            sent = sock.send(incoming)
                            del incoming[:sent]
                        except BlockingIOError:
                            pass
                elif key.data == "input":
                    data = os.read(input_fd, min(65536, BUFFER_LIMIT - len(incoming)))
                    if data:
                        incoming.extend(data)
                    else:
                        stdin_eof = True
                else:
                    try:
                        written = os.write(output_fd, outgoing)
                        del outgoing[:written]
                    except BlockingIOError:
                        pass
            if remote_eof and not outgoing and not output_closed:
                os.close(output_fd)
                output_closed = True
            if remote_eof and not outgoing and write_shutdown:
                return
    finally:
        selector.close()
        admission.close()


def proxy(policy, host, port, directory, parent_pid=None, input_fd=0, output_fd=1):
    admission = admit_connection(policy, host, port, directory, parent_pid)
    return relay(admission, admission.pid, policy["connect_timeout_seconds"], input_fd, output_fd)


def status(policy, directory):
    """Return a sanitized, read-only snapshot for operational checks."""
    now = time.monotonic()
    with _StateLock(directory):
        state = _read_state(directory)
    _rebase_future_times(state, policy, now)
    window = _reservation_horizon(policy)
    pending = [entry for entry in state["pending"] if now - entry["at"] < window]
    return {
        "policy": {name: policy[name] for name in (
            "max_connections", "min_interval_seconds", "failure_window_seconds",
            "max_auth_tries", "max_failures", "connect_timeout_seconds", "login_grace_seconds", "wait_timeout_seconds",
        )},
        "state": {
            "last_start_age_seconds": None if state["last_start"] is None else max(0.0, now - state["last_start"]),
            "pending_count": len(pending),
            "pending_weight": sum(entry["weight"] for entry in pending),
            "pending": [{"host": entry["host"], "pid": entry["pid"], "age_seconds": max(0.0, now - entry["at"]), "weight": entry["weight"]} for entry in pending],
        },
    }


def main(argv=None):
    argv = sys.argv[1:] if argv is None else argv
    if argv == ["status"]:
        print(json.dumps(status(load_policy(), POLICY_PATH.parent), separators=(",", ":")))
        return
    if len(argv) != 3 or argv[0] not in {"proxy", "authenticated"}:
        raise GuardError("usage: fleet-ssh-guard.py proxy|authenticated HOST PORT | status")
    action, host, port_text = argv
    if port_text != "22":
        raise GuardError("only SSH port 22 is allowed")
    policy = load_policy()
    directory = POLICY_PATH.parent
    if action == "proxy":
        proxy(policy, host, 22, directory)
    else:
        authenticated(policy, host, directory)


if __name__ == "__main__":
    try:
        main()
    except GuardError as error:
        print(f"fleet SSH guard: {error}", file=sys.stderr)
        raise SystemExit(255)
