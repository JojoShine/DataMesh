import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]


class GatewayStartupTests(unittest.TestCase):
    def test_heap_options_reach_java_launcher_before_gateway_arguments(self):
        script = (ROOT / 'deploy/docker/flink/sql-gateway.sh').read_text()
        with tempfile.TemporaryDirectory() as folder:
            entrypoint = Path(folder) / 'entrypoint'
            entrypoint.write_text('#!/bin/bash\nprintf "JVM:%s\\n" "${FLINK_ENV_JAVA_OPTS_SQL_GATEWAY:-}"\nprintf "ARG:%s\\n" "$@"\n')
            entrypoint.chmod(0o755)
            env = dict(os.environ, FLINK_ENV_JAVA_OPTS_SQL_GATEWAY='-Dexisting.option=true')
            result = subprocess.run(['bash', '-c', script.replace('/docker-entrypoint.sh', str(entrypoint))], env=env, text=True, capture_output=True, check=True)
        lines = result.stdout.splitlines()
        self.assertIn('-Dexisting.option=true', lines[0])
        self.assertIn('-Xms128m', lines[0])
        self.assertIn('-Xmx384m', lines[0])
        self.assertFalse(any(line.startswith('ARG:-Denv.java.opts') for line in lines))
        self.assertIn('ARG:/opt/flink/bin/sql-gateway.sh', lines)
        self.assertIn('ARG:start-foreground', lines)


if __name__ == '__main__':
    unittest.main()
