#!/usr/bin/env python3
"""Offline executable regressions for the production layer boundary."""
from contextlib import redirect_stderr, redirect_stdout
from io import StringIO
from pathlib import Path
import tempfile
import unittest

import check_layers

MODULE = 'example.test/toolbox'


class LayersTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        (self.root / 'go.mod').write_text('module ' + MODULE + '\n\ngo 1.26.0\n')

    def source(self, package, dependencies=(), name='owner.go', body=''):
        path = self.root / package / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text('package owner\n' + ''.join(
            f'import "{MODULE}/{dep}"\n' for dep in dependencies) + body)

    def test_downward_and_same_layer(self):
        edges = {'lib/value': [], 'lib/codec': ['lib/value'],
                 'vendors/database': ['lib/codec'],
                 'onchain/evm': ['vendors/database'],
                 'middleware/auth': ['vendors/database', 'lib/value'],
                 'rpc/generic': ['lib/value'],
                 'procedures/command': ['middleware/auth', 'onchain/evm', 'rpc/generic'],
                 'procedures/command/rpc': ['procedures/command', 'rpc/generic'],
                 'cli': ['procedures/command/rpc'], 'cmd/blossm': ['cli']}
        for package, dependencies in edges.items():
            self.source(package, dependencies)
        self.assertEqual(check_layers.check(self.root), [])

    def test_forbidden_directions(self):
        for source, target in [('lib/a', 'vendors/b'), ('lib/a', 'procedures/b'),
                               ('vendors/a', 'procedures/b'), ('rpc/a', 'procedures/b'),
                               ('middleware/a', 'onchain/b'), ('tools/a', 'lib/b')]:
            with self.subTest(source=source, target=target):
                self.source(target)
                self.source(source, [target])
                self.assertTrue(any(f'forbidden {source.split("/")[0]} -> {target.split("/")[0]}' in e
                                    for e in check_layers.check(self.root)))
                (self.root / source / 'owner.go').unlink()

    def test_package_cycle_inside_lib(self):
        self.source('lib/a', ['lib/b'])
        self.source('lib/b', ['lib/c'])
        self.source('lib/c', ['lib/a'])
        self.assertIn('package cycle: lib/a -> lib/b -> lib/c -> lib/a', check_layers.check(self.root))

    def test_self_cycle(self):
        self.source('lib/a', ['lib/a'])
        self.assertIn('package cycle: lib/a -> lib/a', check_layers.check(self.root))

    def test_explicit_test_harnesses_have_no_runtime_incoming_edges(self):
        self.source('lib/value')
        self.source('procedures/event', ['lib/value'])
        self.source('tests/querytest', ['procedures/event'],
                    body='import "testing"\n')
        self.source('tests/frontiertest', ['tests/querytest'])
        self.assertEqual(check_layers.check(self.root), [])
        for layer in check_layers.ALLOWED:
            if layer in {'tests', 'tools'}:
                continue
            with self.subTest(layer=layer):
                self.source(layer + '/consumer', ['tests/querytest'])
                self.assertTrue(any(f'forbidden {layer} -> tests' in e
                                    for e in check_layers.check(self.root)))
                (self.root / layer / 'consumer/owner.go').unlink()
        # Composition tests outside lib may use shared harnesses.
        self.source('procedures/event', ['tests/querytest'], name='integration_test.go')
        self.assertEqual(check_layers.check(self.root), [])
        self.source('lib/value', ['tests/querytest'], name='integration_test.go')
        self.assertTrue(any('forbidden lib -> tests' in e for e in check_layers.check(self.root)))

    def test_lib_tests_are_checked_without_production_edges(self):
        self.source('lib/a')
        self.source('lib/b', ['lib/a'])
        self.source('vendors/kms')
        self.source('lib/a', ['lib/b'], name='owner_test.go')
        external = self.root / 'lib/a/external_test.go'
        external.write_text(f'package owner_test\nimport "{MODULE}/lib/a"\n')
        self.assertEqual(check_layers.check(self.root), [])  # no fake a->a or a->b->a cycle
        for package_name in ['owner', 'owner_test']:
            with self.subTest(package_name=package_name):
                external.write_text(f'//go:build ignored_tag\n\npackage {package_name}\nimport "{MODULE}/vendors/kms"\n')
                self.assertTrue(any('forbidden lib -> vendors' in e for e in check_layers.check(self.root)))
        external.write_text(f'package owner_test\nimport "{MODULE}/lib/missing"\n')
        self.assertTrue(any('no production source: lib/missing' in e for e in check_layers.check(self.root)))
        self.source('lib/missing', name='only_test.go')
        self.assertTrue(any('no production source: lib/missing' in e for e in check_layers.check(self.root)))

    def test_lexer_import_forms_and_escapes(self):
        source = '''/* import "ignored/a" */
package owner; import "fmt"
import renamed "example.test/toolbox/lib/a"
import (
 _ "example.test/toolbox/lib/b" // import "ignored/b"
 . `example.test/toolbox/lib/c`; "strings"
 /* comment */ normal "example.test/toolbox/lib/d"
)
import "example.test/toolbox/lib\\x2fe"
import "example.test/toolbox/lib\\057f"
import "example.test/toolbox/lib\\u002fg"
const generated = `package other
import "example.test/toolbox/procedures/forbidden"
`
var quoted = "import \\"also/ignored\\""
'''
        self.assertEqual(check_layers.imports(source), ['fmt', MODULE+'/lib/a', MODULE+'/lib/b',
                         MODULE+'/lib/c', 'strings', MODULE+'/lib/d', MODULE+'/lib/e',
                         MODULE+'/lib/f', MODULE+'/lib/g'])
        self.source('lib/template', body='const template = `package x\nimport "'+MODULE+'/procedures/fake"\n`')
        self.assertEqual(check_layers.check(self.root), [])

    def test_unknown_roots_and_exact_module_prefix(self):
        self.source('lib/a', ['mystery/b'])
        self.source('mystery/b')
        errors = check_layers.check(self.root)
        self.assertTrue(any('unknown internal root mystery' in e for e in errors))
        self.assertTrue(any('unknown production layer mystery' in e for e in errors))
        (self.root / 'mystery/b/owner.go').unlink()
        self.source('lib/a', body='import "example.test/toolbox-extra/procedures/x"\n')
        self.assertEqual(check_layers.check(self.root), [])

    def test_all_tags_but_not_test_files_or_excluded_directories(self):
        self.source('vendors/a')
        self.source('lib/a', name='unsafe_windows.go', body='import "'+MODULE+'/vendors/a"\n')
        path = self.root / 'lib/a/unsafe_windows.go'
        path.write_text('//go:build impossible_tag\n\n'+path.read_text())
        self.assertTrue(any('forbidden lib -> vendors' in e for e in check_layers.check(self.root)))
        path.unlink()
        for directory, name in [('integration', 'upward_test.go'), ('vendors/a', 'upward_test.go'),
                                ('vendor/ignored', 'ignored.go'), ('.git/ignored', 'ignored.go'),
                                ('bin/generated', 'ignored.go')]:
            self.source(directory, ['unknown/upward'], name=name)
        self.assertEqual(check_layers.check(self.root), [])
        self.source('lib/a', ['vendors/a'], name='upward_test.go')
        self.assertTrue(any('forbidden lib -> vendors' in e for e in check_layers.check(self.root)))
        self.source('integration', ['lib/a'])
        self.assertTrue(any('unknown production layer integration' in e for e in check_layers.check(self.root)))

    def test_missing_internal_package_and_malformed_header_fail(self):
        self.source('lib/a', ['lib/missing'])
        self.assertTrue(any('no production source: lib/missing' in e for e in check_layers.check(self.root)))
        for body in ['import (', 'import alias', 'import "unfinished', 'import "bad\\qpath"']:
            self.source('lib/a', body=body)
            self.assertTrue(check_layers.check(self.root))

    def test_cli_status_and_deterministic_diagnostics(self):
        self.source('lib/a')
        out, err = StringIO(), StringIO()
        with redirect_stdout(out), redirect_stderr(err):
            self.assertEqual(check_layers.main([str(self.root)]), 0)
        self.assertIn('all build tags; lib tests checked', out.getvalue())
        self.source('lib/a', ['procedures/b'])
        self.source('procedures/b')
        with redirect_stdout(out), redirect_stderr(err):
            self.assertEqual(check_layers.main([str(self.root)]), 1)
        self.assertIn('forbidden lib -> procedures', err.getvalue())
        self.assertEqual(check_layers.check(self.root), check_layers.check(self.root))


if __name__ == '__main__':
    unittest.main()
