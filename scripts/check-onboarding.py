#!/usr/bin/env python3
"""Exercise first-use setup, worktree scoping, and reusable background reports."""
import argparse
from concurrent.futures import ThreadPoolExecutor
import json
import os
from pathlib import Path
import signal
import shutil
import subprocess
import tempfile
import time
import tomllib
from urllib.request import Request, urlopen
from urllib.parse import urlsplit, parse_qs

parser = argparse.ArgumentParser()
parser.add_argument('--binary', required=True)
binary = str(Path(parser.parse_args().binary).resolve())

with tempfile.TemporaryDirectory(prefix='sparestep-onboarding-') as temp:
    root = Path(temp)
    project, worktree, state, config = (root / p for p in ['project with spaces', 'second worktree', 'state', 'codex'])
    project.mkdir()
    config.mkdir()
    env = {**os.environ, 'CODEX_HOME': str(config)}
    subprocess.run(['git', 'init', '-q', str(project)], check=True)
    subprocess.run(['git', '-C', str(project), '-c', 'user.name=Test', '-c', 'user.email=test@example.invalid',
                    'commit', '-q', '--allow-empty', '-m', 'test'], check=True)
    subprocess.run(['git', '-C', str(project), 'worktree', 'add', '-q', '-b', 'other', str(worktree)], check=True)
    nested = project / 'packages' / 'app'
    nested.mkdir(parents=True)
    (config / 'config.toml').write_text('[features]\nhooks = true\n' +
        '[hooks.state.sentinel]\ntrusted_hash = "sha256:sentinel"\n' + ''.join(
        f'[projects.{json.dumps(str(p))}]\ntrust_level = "trusted"\n' for p in [project, worktree]))
    (project / '.codex').mkdir()
    (project / '.codex' / 'hooks.json').write_text(json.dumps({'hooks': {
        'Stop': [{'hooks': [{'type': 'command', 'command': 'neighbor', 'timeout': 1}]}]}}))

    def run(action, folder=nested):
        result = subprocess.run([binary, action, '--project', str(folder), '--state-dir', str(state),
                                 '--port', '0', '--json'], env=env, capture_output=True, text=True,
                                check=True, timeout=20)
        return json.loads(result.stdout)

    def endpoints():
        return [json.loads(p.read_text()) for p in (state / 'reports').glob('*.json')]

    try:
        first = run('start')
        assert first['project'] == str(project), first
        assert first['events_count'] == 0, first
        assert 'url' in first, first
        native = shutil.which('codex', path=env.get('PATH')) is not None
        if native:
            assert first['status'] == 'ready', first
            assert 'new Codex task' in first.get('next_action', ''), first
            trusted = tomllib.loads((config / 'config.toml').read_text())['hooks']['state']
            assert trusted['sentinel']['trusted_hash'] == 'sha256:sentinel', trusted
            assert len(trusted) == 10, 'setup trusted an unrelated handler or omitted its own'
        else:
            assert first['status'] == 'codex_missing', first
        hookfile = project / '.codex' / 'hooks.json'
        original = hookfile.read_bytes()
        assert not (nested / '.codex').exists(), 'connected a subdirectory rather than its worktree'
        # Reopening the UI should not create duplicate processes or definitions.
        with ThreadPoolExecutor(max_workers=4) as pool:
            repeats = list(pool.map(lambda _: run('open'), range(4)))
        assert all(r['url'] == first['url'] for r in repeats), repeats
        assert len(endpoints()) == 1, endpoints()
        assert run('start')['url'] == first['url']
        assert hookfile.read_bytes() == original, 'reconnect rewrote hook definitions'
        assert b'neighbor' in original, 'setup removed a neighboring hook'
        if native:
            assert tomllib.loads((config / 'config.toml').read_text())['hooks']['state'] == trusted
        paused = run('pause')
        assert paused['status'] == 'paused', paused
        reopened = run('start')
        assert reopened['status'] == ('paused' if native else 'codex_missing'), reopened
        assert run('doctor')['status'] == 'paused', 'opening implicitly resumed capture'
        run('resume')
        second = run('start', worktree)
        assert second['project'] == str(worktree) and second['url'] != first['url'], second
        assert len(endpoints()) == 2, endpoints()
        assert not (worktree / '.codex' / 'hooks.json').samefile(hookfile)
        # The background process outlives the invoking command and serves the right project.
        for report in [first, second]:
            parsed = urlsplit(report['url'])
            token = parse_qs(parsed.fragment)['token'][0]
            req = Request(f'http://{parsed.netloc}/api/health', headers={'Authorization': f'Bearer {token}'})
            with urlopen(req, timeout=2) as response:
                assert json.load(response)['project'] == report['project']
        assert run('disconnect')['status'] == 'disconnected'
        assert run('doctor')['status'] == 'not_connected'
        assert run('doctor', worktree)['status'] != 'not_connected'
        print('PASS: nested-directory setup, idempotent connection, concurrent report reuse, '
              'persistent pause, separate worktrees, background lifetime, and scoped disconnect.')
        if native:
            print('PASS: native Codex setup trusts exactly nine generated hooks and preserves other trust settings.')
        else:
            print('Native trust integration was skipped: Codex CLI is not installed.')
    finally:
        if (state / 'reports').exists():
            for endpoint in endpoints():
                try:
                    os.kill(endpoint['pid'], signal.SIGTERM)
                except ProcessLookupError:
                    pass
            deadline = time.monotonic() + 3
            while endpoints() and time.monotonic() < deadline:
                time.sleep(.05)
