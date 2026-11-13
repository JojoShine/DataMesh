#!/usr/bin/env python3
import argparse
import gzip
import hashlib
import json
from pathlib import Path
import subprocess
import sys
import tempfile
from urllib.parse import quote
import xml.etree.ElementTree as ET

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts"))
from dev_env import load_env  # noqa: E402


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--input", type=Path, help="JSONL raw events, exported with their Kafka metadata")
    args = parser.parse_args()
    env = load_env()
    credentials = env["RUSTFS_ACCESS_KEY"] + ":" + env["RUSTFS_SECRET_KEY"]
    bucket = "datamesh-raw"
    endpoint = "http://127.0.0.1:19000"
    with tempfile.TemporaryDirectory(dir=ROOT / ".local") as temporary:
        directory = Path(temporary)
        raw = args.input.read_bytes() if args.input else b'{"event_id":"storage-probe","layer":"raw"}\n'
        lines = [json.loads(line) for line in raw.splitlines() if line.strip()]
        if not lines:
            raise RuntimeError("No raw records to archive")
        groups = {}
        if args.input:
            for record in lines:
                group = (record["topic"], int(record["partition"]))
                groups.setdefault(group, []).append(record)
        ranges = []
        for (topic, partition), records in sorted(groups.items()):
            offsets = [int(record["offset"]) for record in records]
            if len(set(offsets)) != len(offsets) or max(offsets) - min(offsets) + 1 != len(offsets):
                raise RuntimeError("Raw export has duplicate or missing offsets; completeness cannot be declared")
            ordered = sorted(records, key=lambda record: int(record["offset"]))
            ranges.append({"topic": topic, "partition": partition, "start_offset": str(min(offsets)),
                           "end_offset_exclusive": str(max(offsets) + 1), "event_count": len(offsets),
                           "source_position_format": "debezium.source",
                           "first_source_position": ordered[0]["raw_event"]["source"],
                           "last_source_position": ordered[-1]["raw_event"]["source"]})
        data = gzip.compress(raw, mtime=0)
        checksum = hashlib.sha256(data).hexdigest()
        key = f"validation/{checksum}.jsonl.gz"
        payload = directory / "archive.gz"
        payload.write_bytes(data)

        def call(method, path, body=None):
            output = directory / "response"
            config = f'user = {json.dumps(credentials)}\naws-sigv4 = "aws:amz:us-east-1:s3"\n'
            command = ["curl", "--silent", "--show-error", "--max-time", "20", "--config", "-", "--request", method,
                       "--output", str(output), "--write-out", "%{http_code}", endpoint + path]
            if body is not None:
                command += ["--data-binary", "@" + str(body)]
            result = subprocess.run(command, input=config, capture_output=True, text=True, check=True)
            return int(result.stdout), output.read_bytes()

        status, _ = call("PUT", "/" + bucket)
        if status not in (200, 409):
            raise RuntimeError(f"Bucket initialization failed: HTTP {status}")
        status, _ = call("PUT", "/" + bucket + "/" + quote(key), payload)
        if status != 200:
            raise RuntimeError(f"Archive write failed: HTTP {status}")
        status, stored = call("GET", "/" + bucket + "/" + quote(key))
        if status != 200 or hashlib.sha256(stored).hexdigest() != checksum or gzip.decompress(stored) != raw:
            raise RuntimeError("Archive readback checksum mismatch")
        status, listing = call("GET", "/" + bucket + "?list-type=2&prefix=validation%2F")
        if status != 200 or key not in [element.text for element in ET.fromstring(listing).iter() if element.tag.endswith("}Key") or element.tag == "Key"]:
            raise RuntimeError("Archive listing did not include the verified object")
        manifest = {"bucket": bucket, "key": key, "sha256": checksum, "event_count": len(lines), "ranges": ranges,
                    "validation": "write-read-list-checksum-passed", "scope": "bounded smoke export; not a continuous archive service"}
        manifest_file = directory / "manifest.json"
        manifest_file.write_text(json.dumps(manifest, indent=2) + "\n")
        status, _ = call("PUT", "/" + bucket + "/" + quote(key + ".manifest.json"), manifest_file)
        if status != 200:
            raise RuntimeError("Archive manifest write failed")
        status, stored_manifest = call("GET", "/" + bucket + "/" + quote(key + ".manifest.json"))
        if status != 200 or json.loads(stored_manifest) != manifest:
            raise RuntimeError("Archive manifest readback failed")
        (ROOT / ".local" / "last-archive-manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
        print(json.dumps({"bucket": bucket, "key": key, "event_count": len(lines), "sha256": checksum, "validated_ranges": len(ranges), "manifest_readback": "passed"}))


if __name__ == "__main__":
    try:
        main()
    except (RuntimeError, subprocess.SubprocessError, OSError, ValueError) as exc:
        message = str(exc) if isinstance(exc, RuntimeError) else "Object-store validation failed"
        print(message, file=sys.stderr)
        sys.exit(1)
