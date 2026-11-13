from pathlib import Path
import os
import subprocess
import sys
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[2]


class DorisStartupTests(unittest.TestCase):
    def test_allocator_cache_is_bounded_and_restart_is_idempotent(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            conf = root / 'be.conf'
            conf.write_text('JEMALLOC_CONF="background_thread:true,dirty_decay_ms:5000,tcache_max:32768"\nmem_limit = 2304m\n')
            init = root / 'init_be.sh'
            init.write_text('exit 0\n')
            script = root / 'be-start.sh'
            script.write_text((ROOT / 'deploy/docker/doris/be-start.sh').read_text()
                              .replace('/opt/apache-doris/be/conf/be.conf', str(conf))
                              .replace('/usr/local/bin/init_be.sh', str(init)))
            # The image's sed is GNU; macOS sed has different -i semantics.
            bindir = root / 'bin'
            bindir.mkdir()
            sed = bindir / 'sed'
            sed.write_text('#!/bin/sh\nif [ "$1" = "-i" ]; then shift; exec /usr/bin/sed -i "" "$@"; fi\nexec /usr/bin/sed "$@"\n')
            sed.chmod(0o755)
            env = os.environ.copy()
            if sys.platform == 'darwin':
                env['PATH'] = str(bindir) + os.pathsep + env['PATH']
            for _ in range(2):
                subprocess.run(['bash', str(script)], check=True, env=env)
                result = conf.read_text()
                self.assertEqual(result.count('tcache_max:'), 1)
                self.assertIn('tcache_max:4096', result)
                self.assertIn('background_thread:true,dirty_decay_ms:5000', result)
                self.assertEqual(result.count('mem_limit ='), 1)
                self.assertIn('mem_limit = 2560m', result)


if __name__ == '__main__':
    unittest.main()
