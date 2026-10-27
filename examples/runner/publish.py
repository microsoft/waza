#!/usr/bin/env python3
"""Publish only the bounded reference, reconciling the immutable commit tag first."""

import base64
import hashlib
import http.client
import json
import os
from pathlib import Path
import re
import subprocess
import tarfile
import urllib.error
import urllib.parse
import urllib.request

PACKAGE = "microsoft/waza-runner-reference"
IMAGE = f"ghcr.io/{PACKAGE}"
ACCEPT = "application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json"


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def request(url, headers, method="GET", data=None):
    try:
        with urllib.request.build_opener(NoRedirect).open(
            urllib.request.Request(url, headers=headers, method=method, data=data), timeout=60
        ) as response:
            return response.status, response.headers, response.read()
    except urllib.error.HTTPError as error:
        error.close()
        return error.code, error.headers, b""
    except (urllib.error.URLError, TimeoutError, OSError, http.client.HTTPException):
        raise SystemExit("Registry transport outcome unknown; reconcile before another write") from None


def bearer_headers(token):
    if not isinstance(token, str) or not re.fullmatch(r"[A-Za-z0-9._~+/-]+=*", token):
        raise SystemExit("Registry token response is invalid")
    return {"Authorization": "Bearer " + token}


def registry_token(authenticated):
    headers = {}
    if authenticated:
        if not os.environ.get("GITHUB_ACTOR") or not os.environ.get("GHCR_TOKEN"):
            raise SystemExit("Authenticated publication requires GITHUB_ACTOR and GHCR_TOKEN")
        credentials = f'{os.environ["GITHUB_ACTOR"]}:{os.environ["GHCR_TOKEN"]}'
        headers["Authorization"] = "Basic " + base64.b64encode(credentials.encode()).decode()
    scope = "pull,push" if authenticated else "pull"
    status, _, body = request(f"https://ghcr.io/token?service=ghcr.io&scope=repository:{PACKAGE}:{scope}", headers)
    if status != 200:
        raise SystemExit(f"GHCR {'authenticated' if authenticated else 'anonymous'} token HTTP {status}; publication/public access unavailable")
    try:
        token = json.loads(body)["token"]
        bearer_headers(token)
        return token
    except (ValueError, KeyError, TypeError):
        raise SystemExit("Registry token response is invalid") from None


def manifest(reference, token, allow_missing=False):
    status, headers, body = request(
        f"https://ghcr.io/v2/{PACKAGE}/manifests/{reference}",
        {**bearer_headers(token), "Accept": ACCEPT},
    )
    if status == 404 and allow_missing:
        return None
    if status != 200:
        raise SystemExit(f"Cannot reconcile registry manifest {reference}: HTTP {status}; do not blindly retry a push")
    digest = "sha256:" + hashlib.sha256(body).hexdigest()
    if headers.get("Docker-Content-Digest") != digest:
        raise SystemExit("Registry content digest does not match independently hashed manifest")
    return digest, json.loads(body)


def publish_image(local_id, config_id, tag, token):
    archive = Path(".runner-reference/publish-image.tar")
    try:
        subprocess.run(["docker", "save", "--output", str(archive), local_id], check=True)
        with tarfile.open(archive) as image:
            entries = json.load(image.extractfile("manifest.json"))
            if len(entries) != 1:
                raise SystemExit("Expected exactly one saved reference image")
            entry = entries[0]
            config = image.extractfile(entry["Config"]).read()
            if "sha256:" + hashlib.sha256(config).hexdigest() != config_id:
                raise SystemExit("Saved image configuration differs from the built reference")
            auth = bearer_headers(token)

            def blob(data, media_type):
                digest = "sha256:" + hashlib.sha256(data).hexdigest()
                status, _, _ = request(f"https://ghcr.io/v2/{PACKAGE}/blobs/{digest}", auth, "HEAD")
                if status == 404:
                    status, headers, _ = request(f"https://ghcr.io/v2/{PACKAGE}/blobs/uploads/", auth, "POST", b"")
                    if status != 202:
                        raise SystemExit(f"Cannot start blob upload: HTTP {status}; reconcile before another write")
                    location = urllib.parse.urljoin(f"https://ghcr.io/v2/{PACKAGE}/", headers.get("Location", ""))
                    parsed = urllib.parse.urlsplit(location)
                    if (parsed.scheme != "https" or parsed.netloc != "ghcr.io" or
                            not re.fullmatch(f"/v2/{PACKAGE}/blobs/uploads/[A-Za-z0-9_-]+", parsed.path) or
                            parsed.fragment or "digest" in urllib.parse.parse_qs(parsed.query, keep_blank_values=True)):
                        raise SystemExit("Registry returned an unbounded upload location")
                    separator = "&" if parsed.query else "?"
                    status, _, _ = request(
                        location + separator + urllib.parse.urlencode({"digest": digest}),
                        {**auth, "Content-Type": "application/octet-stream"}, "PUT", data,
                    )
                    if status != 201:
                        raise SystemExit(f"Blob write HTTP {status}; outcome unknown, reconcile before another write")
                elif status != 200:
                    raise SystemExit(f"Cannot reconcile registry blob: HTTP {status}; refusing publication")
                return {"mediaType": media_type, "digest": digest, "size": len(data)}

            def layer(name):
                data = image.extractfile(name).read()
                # Containerd-backed Docker saves can retain compressed OCI blobs.
                suffix = "+gzip" if data.startswith(b"\x1f\x8b") else "+zstd" if data.startswith(b"\x28\xb5\x2f\xfd") else ""
                return blob(data, "application/vnd.oci.image.layer.v1.tar" + suffix)

            document = {
                "schemaVersion": 2,
                "mediaType": "application/vnd.oci.image.manifest.v1+json",
                "config": blob(config, "application/vnd.oci.image.config.v1+json"),
                "layers": [layer(name) for name in entry["Layers"]],
            }
            body = json.dumps(document, separators=(",", ":")).encode()
            status, _, _ = request(
                f"https://ghcr.io/v2/{PACKAGE}/manifests/{tag}",
                {**auth, "Content-Type": document["mediaType"]}, "PUT", body,
            )
            if status != 201:
                raise SystemExit(f"Manifest write HTTP {status}; outcome unknown, reconcile before another write")
            return "sha256:" + hashlib.sha256(body).hexdigest(), document
    finally:
        archive.unlink(missing_ok=True)


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
        # Never replay an uncertain write; a later invocation starts with this read.
        expected = publish_image(local_id, config_id, tag, token)
        existing = manifest(tag, token)
        if existing != expected:
            raise SystemExit("Manifest read-back differs from submitted bytes; do not retry the write")
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
