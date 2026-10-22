#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

printf '\n== Go dependency resolution ==\n'
go mod tidy

printf '\n== Go tests ==\n'
go test ./...

printf '\n== Go vet ==\n'
go vet ./...

printf '\n== Go formatting ==\n'
unformatted="$(gofmt -l $(find . -name '*.go' -type f))"
if [[ -n "$unformatted" ]]; then
  printf '%s\n' "$unformatted"
  exit 1
fi

printf '\n== Frontend JavaScript syntax ==\n'
node --check web/api.js
node - <<'JS'
const fs = require('node:fs');
const cp = require('node:child_process');
const html = fs.readFileSync('web/prototype-v6.html', 'utf8');
const scripts = [...html.matchAll(/<script\b[^>]*>([\s\S]*?)<\/script>/gi)]
  .map(match => match[1]).filter(script => script.trim());
for (const script of scripts) cp.execFileSync(process.execPath, ['--check', '-'], { input: script });
console.log(`Checked ${scripts.length} inline script(s).`);
JS

printf '\n== SQLite migration consistency and execution ==\n'
python3 - <<'PY'
import glob
import os
import sqlite3

migration_names = [
    '001_init.sql',
    '002_proxy_group_node_ids.sql',
    '003_subscription_snapshots.sql',
    '004_node_probe_results.sql',
]
for name in migration_names:
    root = open('migrations/' + name, 'rb').read()
    internal = open('internal/store/migrations/' + name, 'rb').read()
    if root != internal:
        raise SystemExit(f'Migration copies differ: {name}')

for directory in ('migrations', 'internal/store/migrations'):
    files = sorted(glob.glob(directory + '/*.sql'))
    if [os.path.basename(path) for path in files] != migration_names:
        raise SystemExit(f'Unexpected migration set in {directory}: {files}')
    db = sqlite3.connect(':memory:')
    for path in files:
        db.executescript(open(path, encoding='utf-8').read())
    tables = {row[0] for row in db.execute("SELECT name FROM sqlite_master WHERE type='table'")}
    required = {'subscriptions', 'subscription_snapshots', 'node_probe_results', 'proxy_groups'}
    if not required <= tables:
        raise SystemExit(f'Missing expected tables in {directory}: {required - tables}')
    print(f'{directory}: {len(files)} migrations applied; {len(tables)} tables found.')
print('Root and internal migration copies are identical.')
PY

printf '\nAll available verification steps passed.\n'
