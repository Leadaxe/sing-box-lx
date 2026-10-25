"""Validate synthetic independent-download configs without starting a tunnel.

This checks construction/validation only, not REALITY authentication or exchange.
"""
import argparse
import copy
import hashlib
import json
from pathlib import Path
import subprocess

parser = argparse.ArgumentParser()
parser.add_argument('--binary', type=Path, required=True)
parser.add_argument('--work', type=Path, required=True)
args = parser.parse_args()
binary = args.binary.resolve(strict=True)
base = json.loads((Path(__file__).parent / 'config/xhttp_independent_download.json').read_text())
args.work.mkdir(parents=True, exist_ok=False)
cases = [('independent-reality-xmux', copy.deepcopy(base), None)]

def case(name, change, expected):
    config = copy.deepcopy(base)
    change(config['outbounds'][0]['transport'])
    cases.append((name, config, expected))

case('xmux-array', lambda t: t['download_settings']['xhttp']['xmux'].update(max_concurrency=[2, 4]), None)
# The pinned parseRange explicitly swaps reversed bounds; acceptance is intended.
case('xmux-reversed-bounds', lambda t: t['download_settings']['xhttp']['xmux'].update(max_concurrency='8-2'), None)
case('stream-one-downlink', lambda t: t.update(mode='stream-one'), 'download_settings is incompatible')
case('nested-downlink', lambda t: t['download_settings']['xhttp'].update(download_settings=copy.deepcopy(t['download_settings'])), 'nested download_settings')
case('short-reality-public-key', lambda t: t['download_settings']['tls']['reality'].update(public_key='AAAAAAAAAAA'), 'invalid public_key')
case('long-reality-short-id', lambda t: t['download_settings']['tls']['reality'].update(short_id='0123456789abcdef0'), 'invalid short_id')
case('boolean-xmux-range', lambda t: t['download_settings']['xhttp']['xmux'].update(max_concurrency=True), 'invalid xmux range')
results = []
for name, config, expected_error in cases:
    path = args.work / (name + '.json')
    path.write_bytes((json.dumps(config, indent=2) + '\n').encode())
    result = subprocess.run([str(binary), 'check', '-c', str(path)], capture_output=True,
                            text=True, encoding='utf-8', errors='replace', timeout=20,
                            creationflags=getattr(subprocess, 'CREATE_NO_WINDOW', 0))
    output = result.stdout + result.stderr
    (args.work / (name + '.log')).write_bytes(output.encode())
    passed = result.returncode == 0 if expected_error is None else (
        result.returncode != 0 and expected_error in output.lower() and 'panic:' not in output.lower())
    results.append({'name': name, 'exit': result.returncode, 'expected_error': expected_error, 'passed': passed})
    print(('PASS' if passed else 'FAIL') + ' config check: ' + name, flush=True)
report = {'scope': 'construction only; no service started',
          'binary_sha256': hashlib.sha256(binary.read_bytes()).hexdigest(), 'results': results}
(args.work / 'verification.json').write_bytes((json.dumps(report, indent=2) + '\n').encode())
assert all(result['passed'] for result in results), report