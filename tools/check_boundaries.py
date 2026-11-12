#!/usr/bin/env python3
"""Keep the framework independently consumable; application code belongs outside it."""
from pathlib import Path
import re

root = Path(__file__).resolve().parents[1]
errors = []
for name in ('dia', 'wallet', 'diaruntime', 'serviceapp', 'cmd/dia', 'cmd/framework'):
    if (root / name).exists():
        errors.append(f'application implementation must not live in Toolbox: {name}')
for path in root.rglob('*.go'):
    if any(part in ('.git', 'vendor') for part in path.parts):
        continue
    text = path.read_text()
    if re.search(r'github\.com/mejango/blossm-toolbox-go/(?:dia|wallet|diaruntime|serviceapp)(?:/|[\"\s])', text):
        errors.append(f'framework references removed application package: {path.relative_to(root)}')
    if re.search(r'"github\.com/mejango/dia-go(?:/[^"\n]*)?"', text):
        errors.append(f'framework depends on its application: {path.relative_to(root)}')
module = (root / 'go.mod').read_text()
if 'github.com/mejango/dia-go' in module or re.search(r'^replace\b', module, re.M):
    errors.append('framework go.mod must not depend on Dia or local replacements')
if errors:
    raise SystemExit('\n'.join(errors))
print('Framework application boundary passes.')
