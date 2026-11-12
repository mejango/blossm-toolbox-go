#!/usr/bin/env python3
"""Check production Go dependency layers, offline and independent of build tags.

Only actual import declarations are read, not comments or generated source strings.
*_test.go files (including integration tests) are excluded: their upward imports
are test wiring, not production dependencies. All other Go files participate,
including platform-specific and build-tagged files. Go compilation remains the
owner of full syntax/type validation; this lexer reads the package/import header.
The explicit tests/ root may contain reusable harness .go packages importing
runtime layers. No runtime layer may import tests/. Harnesses still participate
in cycle detection; their exemption does not depend on tags or import heuristics.
"""
import argparse
import os
from pathlib import Path
import re
import sys

ALLOWED = {
    'lib': {'lib'},
    'vendors': {'lib', 'vendors'},
    'onchain': {'lib', 'vendors', 'onchain'},
    'middleware': {'lib', 'vendors', 'middleware'},
    'rpc': {'lib', 'rpc'},
    'procedures': {'lib', 'vendors', 'onchain', 'middleware', 'rpc', 'procedures'},
    'cli': {'lib', 'vendors', 'onchain', 'middleware', 'rpc', 'procedures', 'cli'},
    'cmd': {'lib', 'vendors', 'onchain', 'middleware', 'rpc', 'procedures', 'cli'},
    'tools': set(),
    'tests': {'lib', 'vendors', 'onchain', 'middleware', 'rpc', 'procedures', 'cli', 'cmd', 'tests'},
}
SKIP_DIRS = {'.git', 'vendor', 'bin'}


def tokens(source):
    """Yield (kind, text) without comments; consume quoted tokens atomically."""
    i = 0
    while i < len(source):
        c = source[i]
        if c.isspace() or c == '\ufeff':
            i += 1
        elif source.startswith('//', i):
            end = source.find('\n', i + 2)
            i = len(source) if end < 0 else end + 1
        elif source.startswith('/*', i):
            end = source.find('*/', i + 2)
            if end < 0:
                raise ValueError('unterminated comment')
            i = end + 2
        elif c in '\"\'`':
            start = i
            i += 1
            while i < len(source) and source[i] != c:
                if c != '`' and source[i] == '\\':
                    i += 1
                i += 1
            if i >= len(source):
                raise ValueError('unterminated quoted token')
            i += 1
            yield ('string' if c != "'" else 'rune', source[start:i])
        elif c.isalpha() or c == '_':
            start = i
            i += 1
            while i < len(source) and (source[i].isalnum() or source[i] == '_'):
                i += 1
            yield ('identifier', source[start:i])
        else:
            yield ('punctuation', c)
            i += 1


def unquote(value):
    """Decode Go import strings, including raw strings and escaped path bytes."""
    if value[0] == '`':
        return value[1:-1].replace('\r', '')
    result = []
    text = value[1:-1]
    i = 0
    escapes = {'a': '\a', 'b': '\b', 'f': '\f', 'n': '\n', 'r': '\r',
               't': '\t', 'v': '\v', '\\': '\\', '"': '"'}
    while i < len(text):
        if text[i] != '\\':
            result.append(text[i])
            i += 1
            continue
        i += 1
        if i >= len(text):
            raise ValueError('incomplete import escape')
        c = text[i]
        if c in escapes:
            result.append(escapes[c])
            i += 1
        else:
            widths = {'x': 2, 'u': 4, 'U': 8}
            width = widths.get(c, 3)
            base = 16 if c in widths else 8
            if c in widths:
                i += 1
            digits = text[i:i + width]
            pattern = '[0-9a-fA-F]+' if base == 16 else '[0-7]+'
            if len(digits) != width or not re.fullmatch(pattern, digits):
                raise ValueError('invalid import escape')
            number = int(digits, base)
            if (base == 8 and number > 255) or number > 0x10ffff or 0xd800 <= number <= 0xdfff:
                raise ValueError('invalid import escape value')
            result.append(chr(number))
            i += width
    return ''.join(result)


def imports(source):
    stream = iter(tokens(source))
    if next(stream, None) != ('identifier', 'package'):
        raise ValueError('missing package declaration')
    name = next(stream, None)
    if name is None or name[0] != 'identifier':
        raise ValueError('missing package name')
    found = []

    def spec(token):
        if token and (token[0] == 'identifier' or token[1] == '.'):
            token = next(stream, None)  # alias, blank or dot import
        if token is None or token[0] != 'string':
            raise ValueError('expected import path string')
        path = unquote(token[1])
        if not path or any(c.isspace() or ord(c) < 32 for c in path):
            raise ValueError('invalid import path')
        found.append(path)

    token = next(stream, None)
    while token is not None:
        if token[1] == ';':
            token = next(stream, None)
            continue
        if token != ('identifier', 'import'):
            break  # remaining declarations cannot contain Go import declarations
        token = next(stream, None)
        if token == ('punctuation', '('):
            token = next(stream, None)
            while token != ('punctuation', ')'):
                if token is None:
                    raise ValueError('unterminated import block')
                if token[1] != ';':
                    spec(token)
                token = next(stream, None)
        else:
            spec(token)
        token = next(stream, None)
    return found


def cycles(graph):
    """Return deterministic cycle witnesses, using iterative DFS for deep graphs."""
    done, active, path, found = set(), {}, [], []
    for start in sorted(graph):
        if start in done:
            continue
        stack = [(start, iter(sorted(graph[start])))]
        active[start] = 0
        path.append(start)
        while stack:
            node, edges = stack[-1]
            target = next(edges, None)
            if target is None:
                stack.pop()
                done.add(node)
                del active[node]
                path.pop()
            elif target in active:
                found.append(' -> '.join(path[active[target]:] + [target]))
            elif target in graph and target not in done:
                active[target] = len(path)
                path.append(target)
                stack.append((target, iter(sorted(graph[target]))))
    return found


def check(root):
    root = Path(root)
    module_lines = [line for line in (root / 'go.mod').read_text().splitlines()
                    if re.match(r'^\s*module\s', line)]
    if len(module_lines) != 1:
        raise ValueError('go.mod must contain exactly one module directive')
    module = module_lines[0].split('//', 1)[0].split()[1].strip('"`')
    graph, declarations, errors = {}, [], []
    for directory, dirs, files in os.walk(root):
        dirs[:] = sorted(d for d in dirs if d not in SKIP_DIRS)
        for name in sorted(files):
            if not name.endswith('.go') or name.endswith('_test.go'):
                continue
            file = Path(directory) / name
            relative = file.relative_to(root)
            package = relative.parent.as_posix()
            layer = relative.parts[0] if len(relative.parts) > 1 else '.'
            graph.setdefault(package, set())
            if layer not in ALLOWED:
                errors.append(f'{relative}: unknown production layer {layer}')
            try:
                paths = imports(file.read_text(encoding='utf-8'))
            except (ValueError, UnicodeError) as exc:
                errors.append(f'{relative}: {exc}')
                continue
            for imported in paths:
                if imported != module and not imported.startswith(module + '/'):
                    continue
                target = imported[len(module) + 1:] if imported != module else '.'
                target_layer = target.split('/')[0]
                graph[package].add(target)
                declarations.append((relative, target))
                if target_layer not in ALLOWED:
                    errors.append(f'{relative}: unknown internal root {target_layer}: {imported}')
                elif target_layer not in ALLOWED.get(layer, set()):
                    errors.append(f'{relative}: forbidden {layer} -> {target_layer}: {imported}')
    for file, target in declarations:
        if target not in graph:
            errors.append(f'{file}: internal package has no production source: {target}')
    errors.extend('package cycle: ' + cycle for cycle in cycles(graph))
    return sorted(set(errors))


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('root', nargs='?', type=Path, default=Path(__file__).resolve().parents[1])
    args = parser.parse_args(argv)
    try:
        errors = check(args.root)
    except (OSError, ValueError) as exc:
        errors = [str(exc)]
    if errors:
        print('\n'.join(errors), file=sys.stderr)
        return 1
    print('Production Go layers and package cycles pass (all build tags; tests excluded).')
    return 0


if __name__ == '__main__':
    sys.exit(main())
