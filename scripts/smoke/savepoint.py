#!/usr/bin/env python3
import json
from pathlib import Path
import sys
import time
from urllib.request import Request, urlopen

ROOT = Path(__file__).resolve().parents[2]


def main():
    metadata = json.loads((ROOT / ".local/smoke/flink-job.json").read_text())
    job_id = metadata["job_id"]
    base = "http://127.0.0.1:18081/jobs/" + job_id
    request = Request(base + "/savepoints", data=json.dumps({"target-directory": "file:///opt/flink/state/savepoints", "cancel-job": True}).encode(), method="POST", headers={"Content-Type": "application/json"})
    with urlopen(request, timeout=20) as response:
        request_id = json.load(response)["request-id"]
    for _ in range(60):
        with urlopen(base + "/savepoints/" + request_id, timeout=10) as response:
            result = json.load(response)
        if result["status"]["id"] == "COMPLETED":
            operation = result["operation"]
            if "location" not in operation:
                raise RuntimeError("Savepoint failed; no recoverable location returned")
            output = {"source_job_id": job_id, "location": operation["location"], "label_prefix": metadata["label_prefix"]}
            (ROOT / ".local/smoke/flink-savepoint.json").write_text(json.dumps(output) + "\n")
            print("Flink savepoint completed; sample job stopped with recoverable state.")
            return
        time.sleep(1)
    raise RuntimeError("Savepoint result not known within the bound; query the request before retrying")


if __name__ == "__main__":
    try:
        main()
    except (RuntimeError, OSError, ValueError) as exc:
        print(str(exc) if isinstance(exc, RuntimeError) else "Savepoint operation failed", file=sys.stderr)
        sys.exit(1)
