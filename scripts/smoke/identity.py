#!/usr/bin/env python3
import http.cookiejar
import json
from pathlib import Path
import ssl
import sys
from urllib.error import HTTPError
from urllib.request import build_opener, HTTPSHandler, HTTPCookieProcessor, Request

ROOT = Path(__file__).resolve().parents[2]


def main():
    credentials = json.loads((ROOT / ".local/bootstrap-admin.json").read_text())
    context = ssl.create_default_context(cafile=str(ROOT / ".local/certs/server.crt"))
    client = build_opener(HTTPSHandler(context=context), HTTPCookieProcessor(http.cookiejar.CookieJar()))
    origin = "https://localhost:18443"
    request = Request(origin + "/auth/login", data=json.dumps({key: credentials[key] for key in ["username", "password"]}).encode(), method="POST", headers={"Content-Type": "application/json", "Origin": origin})
    with client.open(request, timeout=15) as response:
        principal = json.load(response)
    platforms = ["exchange", "governance", "assets", "security"]
    for platform in platforms:
        with client.open(origin + "/api/v1/platforms/" + platform + "/access", timeout=10) as response:
            if not json.load(response)["allowed"]:
                raise RuntimeError("Shared platform access failed")
    request = Request(origin + "/auth/logout", method="POST", headers={"Origin": origin, "X-CSRF-Token": principal["csrf_token"]})
    with client.open(request, timeout=10) as response:
        if response.status != 204:
            raise RuntimeError("Logout failed")
    try:
        client.open(origin + "/api/v1/me", timeout=10)
        raise RuntimeError("Session stayed valid after logout")
    except HTTPError as exc:
        if exc.code != 401:
            raise
    result = {"result": "passed", "transport": "HTTPS with explicit certificate trust", "platforms": platforms, "shared_session": True, "logout_invalidates_session": True}
    (ROOT / ".local/smoke/identity-result.json").write_text(json.dumps(result, indent=2) + "\n")
    print(json.dumps(result))


if __name__ == "__main__":
    try:
        main()
    except (RuntimeError, OSError, ValueError, HTTPError) as exc:
        print(str(exc) if isinstance(exc, RuntimeError) else "HTTPS identity verification failed; credentials are not logged", file=sys.stderr)
        sys.exit(1)
