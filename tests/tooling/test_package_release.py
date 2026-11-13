import importlib.util
from pathlib import Path
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location('package_release', ROOT / 'scripts/package_release.py')
release = importlib.util.module_from_spec(spec)
spec.loader.exec_module(release)


class ReleaseSchemaTests(unittest.TestCase):
    def test_actual_schema_is_derived_from_paired_migrations(self):
        self.assertEqual(release.schema_version(ROOT / 'migrations'), 48)

    def test_future_schema_does_not_require_packaging_constant_change(self):
        with tempfile.TemporaryDirectory() as folder:
            path = Path(folder)
            for name in ['000001_initial', '000002_next']:
                for direction in ['up', 'down']:
                    (path / f'{name}.{direction}.sql').write_text('SELECT 1;')
            self.assertEqual(release.schema_version(path), 2)

    def test_empty_or_incomplete_migrations_fail_closed(self):
        with tempfile.TemporaryDirectory() as folder:
            path = Path(folder)
            with self.assertRaises(RuntimeError):
                release.schema_version(path)
            (path / '000001_initial.up.sql').write_text('SELECT 1;')
            with self.assertRaises(RuntimeError):
                release.schema_version(path)


if __name__ == '__main__':
    unittest.main()
