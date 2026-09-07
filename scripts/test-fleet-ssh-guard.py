#!/usr/bin/env python3
import importlib.util
import fcntl
import multiprocessing
import os
from pathlib import Path
import queue
import socket
import tempfile
import threading
import time
import unittest
from unittest import mock


spec = importlib.util.spec_from_file_location("fleet_ssh_guard", Path(__file__).with_name("fleet-ssh-guard.py"))
guard = importlib.util.module_from_spec(spec)
spec.loader.exec_module(guard)


def raw_policy(**changes):
    policy = {
        "version": 1,
        "max_connections": 4,
        "min_interval_seconds": 0.03,
        "failure_window_seconds": 0.20,
        "max_auth_tries": 3,
        "max_failures": 5,
        "connect_timeout_seconds": 1,
        "login_grace_seconds": 0.01,
        "wait_timeout_seconds": 0,
        "targets": {"127.0.0.1": ["alpha", "beta"]},
    }
    policy.update(changes)
    return policy


class Server:
    def __init__(self, reply=False):
        self.listener = socket.socket()
        self.listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        self.listener.bind(("127.0.0.1", 0))
        self.listener.listen()
        self.port = self.listener.getsockname()[1]
        self.reply, self.stop = reply, threading.Event()
        self.received = bytearray()
        self.thread = threading.Thread(target=self._serve, daemon=True)
        self.thread.start()

    def _serve(self):
        self.listener.settimeout(0.05)
        workers = []
        while not self.stop.is_set():
            try:
                connection, _ = self.listener.accept()
            except TimeoutError:
                continue
            workers.append(threading.Thread(target=self._connection, args=(connection,), daemon=True))
            workers[-1].start()
        self.listener.close()
        for worker in workers:
            worker.join(1)

    def _connection(self, connection):
        with connection:
            while True:
                data = connection.recv(65536)
                if not data:
                    break
                self.received.extend(data)
            if self.reply:
                connection.sendall(b"reply:" + bytes(self.received))

    def close(self):
        self.stop.set()
        self.thread.join(1)


def paced_worker(raw, directory, port, results):
    policy = guard.validate_policy(raw)
    try:
        admission = guard.admit_connection(policy, "alpha", port, directory)
        results.put(("ok", time.monotonic()))
        time.sleep(0.08)
        admission.close()
    except Exception as error:  # pragma: no cover - sent to parent process
        results.put(("error", str(error)))


def slot_worker(raw, directory, port, ready):
    policy = guard.validate_policy(raw)
    admission = guard.admit_connection(policy, "alpha", port, directory)
    ready.put("admitted")
    time.sleep(10)
    admission.close()


def saturation_worker(raw, directory, port, events):
    policy = guard.validate_policy(raw)
    admission = guard.admit_connection(policy, "alpha", port, directory)
    events.put((time.monotonic(), "open"))
    time.sleep(0.20)
    admission.close()
    events.put((time.monotonic(), "close"))


def state_lock_worker(directory, ready):
    directory = Path(directory)
    directory.mkdir(mode=0o700, parents=True, exist_ok=True)
    with (directory / "state.lock").open("a+") as handle:
        fcntl.flock(handle.fileno(), fcntl.LOCK_EX)
        ready.put("locked")
        time.sleep(10)


class FleetSSHGuardTests(unittest.TestCase):
    def setUp(self):
        # The repository's runtime intentionally blocks permanent deletion.
        # Leave OS-managed temporary test directories for its normal cleanup.
        self.temporary = tempfile.TemporaryDirectory(ignore_cleanup_errors=True)
        self.directory = Path(self.temporary.name) / "state"
        self.server = Server()

    def tearDown(self):
        self.server.close()
        self.temporary.cleanup()

    def policy(self, **changes):
        return guard.validate_policy(raw_policy(**changes))

    def test_real_processes_share_global_pacing(self):
        raw = raw_policy(max_auth_tries=1, max_failures=9, min_interval_seconds=0.12, wait_timeout_seconds=1)
        results = multiprocessing.Queue()
        processes = [multiprocessing.Process(target=paced_worker, args=(raw, str(self.directory), self.server.port, results)) for _ in range(3)]
        for process in processes:
            process.start()
        values = [results.get(timeout=3) for _ in processes]
        for process in processes:
            process.join(3)
            self.assertEqual(process.exitcode, 0)
        self.assertTrue(all(kind == "ok" for kind, _ in values), values)
        starts = sorted(value for _, value in values)
        self.assertGreaterEqual(starts[1] - starts[0], 0.10)
        self.assertGreaterEqual(starts[2] - starts[1], 0.10)

    def test_aliases_share_weighted_failure_budget_and_callback_clears_it(self):
        policy = self.policy(failure_window_seconds=1, min_interval_seconds=0.01)
        first = guard.admit_connection(policy, "alpha", self.server.port, self.directory)
        first.close()
        time.sleep(0.02)
        with self.assertRaisesRegex(guard.GuardError, "failure budget"):
            guard.admit_connection(policy, "127.0.0.1", self.server.port, self.directory)
        guard.authenticated(policy, "beta", self.directory, parent_pid=os.getppid())
        second = guard.admit_connection(policy, "127.0.0.1", self.server.port, self.directory)
        second.close()
        snapshot = guard.status(policy, self.directory)
        self.assertEqual(snapshot["state"]["pending_count"], 1)
        self.assertEqual(snapshot["state"]["pending"][0]["host"], "127.0.0.1")

    def test_unacknowledged_failure_expires_without_refund(self):
        policy = self.policy(failure_window_seconds=0.15, connect_timeout_seconds=0.01, wait_timeout_seconds=0.08)
        first = guard.admit_connection(policy, "alpha", self.server.port, self.directory)
        first.close()
        with self.assertRaises(guard.GuardError):
            guard.admit_connection(policy, "beta", self.server.port, self.directory)
        time.sleep(0.18)
        second = guard.admit_connection(policy, "beta", self.server.port, self.directory)
        second.close()

    def test_killed_owner_releases_kernel_slot(self):
        raw = raw_policy(max_connections=1, max_auth_tries=1, max_failures=9, wait_timeout_seconds=0.2)
        ready = multiprocessing.Queue()
        process = multiprocessing.Process(target=slot_worker, args=(raw, str(self.directory), self.server.port, ready))
        process.start()
        self.assertEqual(ready.get(timeout=3), "admitted")
        process.terminate()
        process.join(3)
        self.assertNotEqual(process.exitcode, 0)
        admission = guard.admit_connection(self.policy(max_connections=1, max_auth_tries=1, max_failures=9, wait_timeout_seconds=0.2), "alpha", self.server.port, self.directory)
        admission.close()

    def test_real_processes_never_exceed_global_connection_slots(self):
        raw = raw_policy(max_connections=2, max_auth_tries=1, max_failures=9, min_interval_seconds=0.01, wait_timeout_seconds=2)
        events = multiprocessing.Queue()
        processes = [multiprocessing.Process(target=saturation_worker, args=(raw, str(self.directory), self.server.port, events)) for _ in range(5)]
        for process in processes:
            process.start()
        observed = [events.get(timeout=5) for _ in range(10)]
        for process in processes:
            process.join(5)
            self.assertEqual(process.exitcode, 0)
        active = maximum = 0
        for _, event in sorted(observed):
            active += 1 if event == "open" else -1
            maximum = max(maximum, active)
            self.assertGreaterEqual(active, 0)
        self.assertEqual(maximum, 2)

    def test_corrupt_inputs_never_open_a_network_socket(self):
        corrupt_policy = Path(self.temporary.name) / "bad-policy.json"
        corrupt_policy.write_text("{", encoding="utf-8")
        with self.assertRaises(guard.GuardError):
            guard.load_policy(corrupt_policy)
        self.directory.mkdir()
        (self.directory / "state.json").write_text("not json", encoding="utf-8")
        with mock.patch.object(guard.socket, "socket", side_effect=AssertionError("network attempted")):
            with self.assertRaises(guard.GuardError):
                guard.admit_connection(self.policy(), "alpha", self.server.port, self.directory)

    def test_default_zero_wait_rejects_pacing_and_budget_without_dialing(self):
        pacing = self.policy(max_auth_tries=1, max_failures=9, min_interval_seconds=1)
        first = guard.admit_connection(pacing, "alpha", self.server.port, self.directory)
        first.close()
        with mock.patch.object(guard.socket, "socket", side_effect=AssertionError("network attempted")):
            with self.assertRaisesRegex(guard.GuardError, "pacing active"):
                guard.admit_connection(pacing, "alpha", self.server.port, self.directory)

        budget = self.policy(max_auth_tries=3, max_failures=5, min_interval_seconds=0.01)
        other_directory = Path(self.temporary.name) / "budget-state"
        first = guard.admit_connection(budget, "alpha", self.server.port, other_directory)
        first.close()
        time.sleep(0.02)
        with mock.patch.object(guard.socket, "socket", side_effect=AssertionError("network attempted")):
            with self.assertRaisesRegex(guard.GuardError, "failure budget"):
                guard.admit_connection(budget, "alpha", self.server.port, other_directory)

    def test_default_zero_wait_rejects_held_slot_and_state_lock_without_dialing(self):
        raw = raw_policy(max_connections=1, max_auth_tries=1, max_failures=9, wait_timeout_seconds=0)
        ready = multiprocessing.Queue()
        holder = multiprocessing.Process(target=slot_worker, args=(raw, str(self.directory), self.server.port, ready))
        holder.start()
        self.assertEqual(ready.get(timeout=3), "admitted")
        with mock.patch.object(guard.socket, "socket", side_effect=AssertionError("network attempted")):
            with self.assertRaisesRegex(guard.GuardError, "slot unavailable"):
                guard.admit_connection(self.policy(max_connections=1, max_auth_tries=1, max_failures=9), "alpha", self.server.port, self.directory)
        holder.terminate()
        holder.join(3)

        other_directory = Path(self.temporary.name) / "locked-state"
        lock_ready = multiprocessing.Queue()
        holder = multiprocessing.Process(target=state_lock_worker, args=(str(other_directory), lock_ready))
        holder.start()
        self.assertEqual(lock_ready.get(timeout=3), "locked")
        with mock.patch.object(guard.socket, "socket", side_effect=AssertionError("network attempted")):
            with self.assertRaisesRegex(guard.GuardError, "state is busy"):
                guard.admit_connection(self.policy(), "alpha", self.server.port, other_directory)
        holder.terminate()
        holder.join(3)

    def test_slow_dial_cannot_compress_the_next_start_interval(self):
        policy = self.policy(max_auth_tries=1, max_failures=9, min_interval_seconds=0.08, wait_timeout_seconds=0.3)
        real_socket = socket.socket

        class SlowSocket(real_socket):
            def connect_ex(self, address):
                time.sleep(0.15)
                return super().connect_ex(address)

        with mock.patch.object(guard.socket, "socket", SlowSocket):
            first = guard.admit_connection(policy, "alpha", self.server.port, self.directory)
        first_started = time.monotonic()
        first.close()
        second = guard.admit_connection(policy, "alpha", self.server.port, self.directory)
        second.close()
        self.assertGreaterEqual(time.monotonic() - first_started, 0.07)

    def test_interrupted_dial_requires_a_new_full_interval(self):
        policy = self.policy(max_auth_tries=1, max_failures=9, min_interval_seconds=0.08, wait_timeout_seconds=0.3)
        self.directory.mkdir()
        guard._write_state(self.directory, {"last_start": None, "dialing": time.monotonic(), "pending": []})
        started = time.monotonic()
        admission = guard.admit_connection(policy, "alpha", self.server.port, self.directory)
        admission.close()
        self.assertGreaterEqual(time.monotonic() - started, 0.07)

    def test_relay_preserves_bytes_and_half_close(self):
        self.server.close()
        self.server = Server(reply=True)
        policy = self.policy(max_auth_tries=1, max_failures=9)
        input_read, input_write = os.pipe()
        output_read, output_write = os.pipe()
        result = []
        parent_pid = os.getppid()
        thread = threading.Thread(target=lambda: result.append(guard.proxy(policy, "alpha", self.server.port, self.directory, parent_pid, input_read, output_write)))
        thread.start()
        payload = b"a\x00b\n" * 100
        os.write(input_write, payload)
        os.close(input_write)
        thread.join(3)
        os.close(input_read)
        try:
            os.close(output_write)
        except OSError:
            pass
        response = b""
        while True:
            chunk = os.read(output_read, 65536)
            if not chunk:
                break
            response += chunk
        os.close(output_read)
        self.assertFalse(thread.is_alive())
        self.assertEqual(result, [None])
        self.assertEqual(response, b"reply:" + payload)


if __name__ == "__main__":
    unittest.main()
