#!/usr/bin/env python3
"""Publish only the bounded reference, reconciling the immutable commit tag first."""

import base64
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import urllib.error
import urllib.request

PACKAGE = "microsoft/waza-runner-reference"
IMAGE = f"ghcr.io/{PACKAGE}"
ACCEPT = "application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json"


def request(url, headers):
    try:
        with urllib.request.urlopen(urllib.request.Request(url, headers=headers), timeout=60) as response:
            return response.status, response.headers, response.read()
    except urllib.error.HTTPError as error:
        return error.code, error.headers, b""


def registry_token(authenticated):
    headers = {}
    if authenticated:
        credentials = f'{os.environ["GITHUB_ACTOR"]}:{os.environ["GHCR_TOKEN"]}'
        headers["Authorization"] = "Basic " + base64.b64encode(credentials.encode()).decode()
    scope = "pull,push" if authenticated else "pull"
    status, _, body = request(f"https://ghcr.io/token?service=ghcr.io&scope=repository:{PACKAGE}:{scope}", headers)
    if status != 200:
        raise SystemExit(f"GHCR {'authenticated' if authenticated else 'anonymous'} token HTTP {status}; publication/public access unavailable")
    return json.loads(body)["token"]


def manifest(reference, token, allow_missing=False):
    status, headers, body = request(
        f"https://ghcr.io/v2/{PACKAGE}/manifests/{reference}",
        {"Authorization": f"Bearer {token}", "Accept": ACCEPT},
    )
    if status == 404 and allow_missing:
        return None
    if status != 200:
        raise SystemExit(f"Cannot reconcile registry manifest {reference}: HTTP {status}; do not blindly retry a push")
    digest = "sha256:" + hashlib.sha256(body).hexdigest()
    if headers.get("Docker-Content-Digest") != digest:
        raise SystemExit("Registry content digest does not match independently hashed manifest")
    return digest, json.loads(body)


def main():
    root = Path(__file__).resolve().parents[2]
    os.chdir(root)
    Path(".runner-reference/published-reference").unlink(missing_ok=True)
    commit = subprocess.check_output(["git", "rev-parse", "HEAD"], text=True).strip()
    if not re.fullmatch(r"[a-f0-9]{40}", commit):
        raise SystemExit("Expected a complete source commit")
    if subprocess.check_output(["git", "status", "--porcelain"], text=True):
        raise SystemExit("Publication requires a clean tracked checkout")
    local_id = Path(".runner-reference/image-id").read_text().strip()
    config_id = Path(".runner-reference/image-config-id").read_text().strip()
    if not all(re.fullmatch(r"sha256:[a-f0-9]{64}", value) for value in (local_id, config_id)):
        raise SystemExit("Build the reference first")
    # Bind the image to the checked-out source before any registry write.
    revision = subprocess.check_output(
        ["docker", "inspect", "--format", '{{index .Config.Labels "org.opencontainers.image.revision"}}', local_id],
        text=True,
    ).strip()
    if revision != commit:
        raise SystemExit("Image revision differs from checkout; rebuild before publication")

    token = registry_token(True)
    tag = f"sha-{commit}"
    existing = manifest(tag, token, allow_missing=True)
    if existing is None:
        # A failed/uncertain push is never repeated here. A later invocation
        # starts with the authoritative manifest read above.
        subprocess.run(["docker", "tag", local_id, f"{IMAGE}:{tag}"], check=True)
        subprocess.run(["docker", "push", f"{IMAGE}:{tag}"], check=True)
        existing = manifest(tag, token)
    digest, document = existing
    if document.get("config", {}).get("digest") != config_id:
        raise SystemExit("Commit tag exists with different image bytes; refusing to overwrite it")
    reference = f"{IMAGE}@{digest}"
    public = manifest(digest, registry_token(False))
    if public != existing:
        raise SystemExit("Anonymous digest read differs from authenticated commit manifest")
    subprocess.run(["bash", "examples/runner/smoke.sh", reference], check=True)
    Path(".runner-reference/published-reference").write_text(reference + "\n")
    summary = os.environ.get("GITHUB_STEP_SUMMARY")
    if summary:
        with open(summary, "a") as output:
            output.write(f"Confirmed public runner reference: `{reference}`\n")
    print(reference)


if __name__ == "__main__":
    main()
