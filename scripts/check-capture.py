#!/usr/bin/env python3
"""Replay supported hook payloads through the release binary; no model calls."""
import argparse
from concurrent.futures import ThreadPoolExecutor
import json
from pathlib import Path
import statistics
import subprocess
import tempfile
import time

parser = argparse.ArgumentParser()
parser.add_argument('--binary', required=True)
binary = str(Path(parser.parse_args().binary).resolve())

with tempfile.TemporaryDirectory(prefix='sparestep-capture-') as tmp:
    project = Path(tmp) / 'example project'
    project.mkdir()
    state = Path(tmp) / 'state'
    timings = []

    def run(command, *args, payload=None):
        started = time.perf_counter()
        result = subprocess.run(
            [binary, command, '--project', str(project), '--state-dir', str(state), *args],
            input=json.dumps(payload) if payload is not None else None,
            text=True, capture_output=True, check=True, timeout=15)
        if command == 'hook':
            timings.append((time.perf_counter() - started) * 1000)
            assert json.loads(result.stdout) == {}, result.stdout
            assert result.stderr == '', result.stderr
        return result.stdout

    def hook(event, session, operation='', response=None):
        payload = {'hook_event_name': event, 'cwd': str(project),
                   'session_id': session, 'turn_id': 'turn-1'}
        if operation:
            payload.update(tool_name='exec_command', tool_use_id=operation,
                           tool_input={'cmd': 'missing-runtime --secret=PRIVATE-EXAMPLE'})
        if response is not None:
            payload['tool_response'] = response
        run('hook', payload=payload)

    for i in range(3):
        session = f'task-{i}'
        hook('UserPromptSubmit', session)
        hook('PreToolUse', session, 'call-1')
        hook('PostToolUse', session, 'call-1', {'exit_code': 127, 'duration_ms': 20})
        hook('Stop', session)

    report = json.loads(run('status', '--json'))
    assert report['events_count'] == 12, report
    assert len(report['findings']) == 1, report
    finding = report['findings'][0]
    assert finding['occurrences'] == 3, finding
    assert report['status'] == 'needs_attention', report
    assert not report.get('token_usage'), report
    assert 'PRIVATE-EXAMPLE' not in json.dumps(report), 'raw command secret persisted'
    hook('PostToolUse', 'task-2', 'call-1', {'exit_code': 127, 'duration_ms': 20})
    assert json.loads(run('status', '--json'))['events_count'] == 12, 'replay duplicated'

    run('pause')
    hook('UserPromptSubmit', 'paused-task')
    assert json.loads(run('status', '--json'))['events_count'] == 12
    run('resume')

    # Separate processes share the same initialized database.
    with ThreadPoolExecutor(max_workers=4) as pool:
        list(pool.map(lambda i: hook('PostToolUse', f'parallel-{i}', 'call',
                                    {'output': '{"exit_code":0}'}), range(20)))
    report = json.loads(run('status', '--json'))
    assert report['events_count'] == 32, report['events_count']
    unknown = [e for e in report['events'] if e['session_id'].startswith('parallel-')]
    assert len(unknown) == 20
    assert all(e.get('exit_code') is None and e.get('success') is None for e in unknown)
    assert not (state / 'recording-error.txt').exists(), 'recording failure diagnostic'
    draft = run('draft', finding['id'])
    assert 'PRIVATE-EXAMPLE' not in draft
    p95 = sorted(timings)[int((len(timings) - 1) * .95)]
    print('PASS: recurring failures, redaction, durable pause, replay deduplication, '
          'concurrent processes, unknown outcomes, and local draft.')
    print(f'Local capture sample: {len(timings)} subprocesses, '
          f'median {statistics.median(timings):.1f} ms, p95 {p95:.1f} ms '
          '(includes process startup and concurrent contention; not a savings estimate).')
