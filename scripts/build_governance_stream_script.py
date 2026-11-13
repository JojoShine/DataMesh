#!/usr/bin/env python3
"""Build and verify the Java17 Flink transport adapter with pinned existing images."""
import argparse
from pathlib import Path
import shutil
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[1]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output-dir', type=Path, default=ROOT / '.local/flink-jars')
    args = parser.parse_args()
    output = args.output_dir.resolve()
    output.mkdir(parents=True, exist_ok=True)
    # Maven supplies only javac/jar: no Maven invocation or library download.
    # Tests execute in the production Flink Java17 image, whose base is JRE-only.
    with tempfile.TemporaryDirectory(prefix='datamesh-stream-script-') as temporary:
        subprocess.run([
            'docker', 'build', '--network=none', '--progress=plain',
            '--output', f'type=local,dest={temporary}',
            str(ROOT / 'tools/stream-script'),
        ], cwd=ROOT, check=True, timeout=600)
        target = output / 'datamesh-stream-script.jar'
        staged = output / '.datamesh-stream-script.jar.new'
        shutil.copyfile(Path(temporary) / target.name, staged)
        staged.replace(target)
    print('Verified stream script adapter:', target)


if __name__ == '__main__':
    main()
