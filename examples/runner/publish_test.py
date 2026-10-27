import base64
from email.message import Message
import hashlib
import gzip
import io
import json
import subprocess
import tarfile
import unittest
from unittest.mock import patch
import urllib.error
import urllib.response

import publish


def digest(body):
    return "sha256:" + hashlib.sha256(body).hexdigest()


class PublicationTests(unittest.TestCase):
    """Exercise Request, opener, HTTP status processing and header construction.

    Only the HTTPS transport and local Docker/filesystem are replaced. No request
    can reach a registry and no Docker publication command can run.
    """

    def setUp(self):
        self.commit = "a" * 40
        self.config = b'{"architecture":"amd64","os":"linux"}'
        self.config_id = digest(self.config)
        self.layer = gzip.compress(b"bounded layer fixture", mtime=0)
        self.document = {"config": {"digest": self.config_id}}
        self.body = json.dumps(self.document).encode()
        self.token = "acquired-private-bearer"
        self.public_token = "acquired-anonymous-pull"
        self.responses = []
        self.seen = []
        self.enterContext(patch.dict(publish.os.environ, {
            "GITHUB_ACTOR": "publisher", "GHCR_TOKEN": "job-credential", "GITHUB_STEP_SUMMARY": "",
        }))
        self.enterContext(patch.object(publish.os, "chdir"))
        self.enterContext(patch.object(publish.subprocess, "check_output",
                                       side_effect=[self.commit, "", self.commit]))
        self.enterContext(patch.object(publish.Path, "read_text", return_value=self.config_id))
        self.unlink = self.enterContext(patch.object(publish.Path, "unlink"))
        self.write = self.enterContext(patch.object(publish.Path, "write_text"))
        self.run = self.enterContext(patch.object(publish.subprocess, "run"))
        self.output = self.enterContext(patch("builtins.print"))
        self.enterContext(patch.object(publish.urllib.request.HTTPSHandler, "https_open",
                                       side_effect=self.transport))
        archive = io.BytesIO()
        with tarfile.open(fileobj=archive, mode="w") as image:
            for name, body in [
                ("manifest.json", json.dumps([{"Config": "config.json", "Layers": ["layer.tar"]}]).encode()),
                ("config.json", self.config), ("layer.tar", self.layer),
            ]:
                entry = tarfile.TarInfo(name)
                entry.size = len(body)
                image.addfile(entry, io.BytesIO(body))
        archive_bytes = archive.getvalue()
        open_tar = tarfile.open
        self.enterContext(patch.object(publish.tarfile, "open",
                                       side_effect=lambda *args: open_tar(fileobj=io.BytesIO(archive_bytes))))

    def tearDown(self):
        self.assertEqual(self.responses, [], "Expected transport requests were not issued")
        # Credentials must not reach logs, summaries, subprocess args or references.
        exposed = repr((self.output.call_args_list, self.write.call_args_list, self.run.call_args_list))
        for secret in (self.token, self.public_token, "job-credential"):
            self.assertNotIn(secret, exposed)

    def enqueue(self, method, path, status=200, body=b"", headers=None, auth=None):
        self.responses.append((method, path, status, body, headers or {}, auth))

    def transport(self, request):
        self.assertTrue(self.responses, f"Unexpected request: {request.method} {request.full_url}")
        method, path, status, body, headers, auth = self.responses.pop(0)
        self.assertEqual(request.method, method)
        self.assertEqual(request.full_url, "https://ghcr.io" + path)
        self.assertEqual(request.get_header("Authorization"), auth)
        if method == "GET" and "/manifests/" in path:
            self.assertEqual(request.get_header("Accept"), publish.ACCEPT)
        if method == "PUT":
            content_type = "application/octet-stream" if "/blobs/" in path else "application/vnd.oci.image.manifest.v1+json"
            self.assertEqual(request.get_header("Content-type"), content_type)
        self.seen.append(request)
        if isinstance(body, Exception):
            raise body
        message = Message()
        for key, value in headers.items():
            message[key] = value
        response = urllib.response.addinfourl(io.BytesIO(body), message, request.full_url, status)
        response.msg = "fixture"
        return response

    def token_request(self, authenticated=True, status=200, body=None):
        scope = "pull,push" if authenticated else "pull"
        auth = "Basic " + base64.b64encode(b"publisher:job-credential").decode() if authenticated else None
        token = self.token if authenticated else self.public_token
        self.enqueue("GET", f"/token?service=ghcr.io&scope=repository:{publish.PACKAGE}:{scope}",
                     status, json.dumps({"token": token}).encode() if body is None else body, auth=auth)

    def manifest_request(self, reference=None, status=200, body=None, public=False, header_digest=None):
        body = self.body if body is None else body
        self.enqueue("GET", f"/v2/{publish.PACKAGE}/manifests/{reference or 'sha-' + self.commit}",
                     status, body, {"Docker-Content-Digest": header_digest or digest(body)},
                     "Bearer " + (self.public_token if public else self.token))

    def public_requests(self, body=None, status=200):
        body = self.body if body is None else body
        self.token_request(False)
        self.manifest_request(digest(body), status, body, public=True)

    def upload_requests(self, existing=False, fail=None, location=None):
        for body in (self.config, self.layer):
            self.enqueue("HEAD", f"/v2/{publish.PACKAGE}/blobs/{digest(body)}",
                         200 if existing else 404, auth="Bearer " + self.token)
            if not existing:
                path = f"/v2/{publish.PACKAGE}/blobs/uploads/fixture"
                self.enqueue("POST", f"/v2/{publish.PACKAGE}/blobs/uploads/", 202,
                             headers={"Location": location or path + "?state=bounded"}, auth="Bearer " + self.token)
                if location:
                    return
                self.enqueue("PUT", path + "?state=bounded&digest=" + digest(body).replace(":", "%3A"),
                             201, auth="Bearer " + self.token)
        self.enqueue("PUT", f"/v2/{publish.PACKAGE}/manifests/sha-{self.commit}",
                     201 if fail is None else fail, auth="Bearer " + self.token)

    def uploaded_body(self):
        return json.dumps({
            "schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json",
            "config": {"mediaType": "application/vnd.oci.image.config.v1+json",
                       "digest": self.config_id, "size": len(self.config)},
            "layers": [{"mediaType": "application/vnd.oci.image.layer.v1.tar+gzip",
                        "digest": digest(self.layer), "size": len(self.layer)}],
        }, separators=(",", ":")).encode()

    def test_existing_commit_reconciles_with_actual_private_and_anonymous_headers(self):
        self.token_request()
        self.manifest_request()
        self.public_requests()
        publish.main()
        self.assertEqual(self.run.call_count, 1)
        self.assertEqual(self.run.call_args.args[0][:2], ["bash", "examples/runner/smoke.sh"])
        self.write.assert_called_once_with(f"{publish.IMAGE}@{digest(self.body)}\n")

    def test_all_blob_tag_and_readback_requests_use_acquired_token(self):
        self.token_request()
        self.manifest_request(status=404)
        self.upload_requests()
        body = self.uploaded_body()
        self.manifest_request(body=body)
        self.public_requests(body)
        publish.main()
        writes = [req for req in self.seen if req.method == "PUT"]
        self.assertEqual([req.data for req in writes], [self.config, self.layer, body])
        self.assertEqual(self.run.call_args_list[0].args[0][:2], ["docker", "save"])
        self.write.assert_called_once()

    def test_preexisting_blobs_are_not_uploaded_again(self):
        self.upload_requests(existing=True)
        publish.publish_image(self.config_id, self.config_id, "sha-" + self.commit, self.token)
        self.assertEqual([req.method for req in self.seen], ["HEAD", "HEAD", "PUT"])

    def test_authentication_errors_are_never_package_absence(self):
        for status in (401, 403, 429, 500):
            with self.subTest(status=status):
                self.manifest_request(status=status)
                with self.assertRaisesRegex(SystemExit, "do not blindly retry"):
                    publish.manifest("sha-" + self.commit, self.token, allow_missing=True)
        self.run.assert_not_called()

    def test_denied_blob_or_tag_operations_stop_without_retry(self):
        for status in (401, 403):
            for failed_request in range(7):
                with self.subTest(status=status, failed_request=failed_request):
                    self.upload_requests()
                    request = list(self.responses[failed_request])
                    request[2] = status
                    self.responses = self.responses[:failed_request] + [tuple(request)]
                    before = len(self.seen)
                    with self.assertRaises(SystemExit):
                        publish.publish_image(self.config_id, self.config_id, "sha-" + self.commit, self.token)
                    self.assertEqual(len(self.seen) - before, failed_request + 1)
        self.write.assert_not_called()

    def test_local_image_configuration_mismatch_prevents_all_registry_writes(self):
        with self.assertRaisesRegex(SystemExit, "configuration differs"):
            publish.publish_image(self.config_id, "sha256:" + "f" * 64, "sha-" + self.commit, self.token)
        self.assertEqual(self.seen, [])

    def test_token_errors_and_malformed_credentials_fail_closed(self):
        for authenticated in (True, False):
            for status, body in [(401, b""), (403, b""), (200, b"not-json"), (200, b"{}"),
                                 (200, b'{"token":""}'), (200, b'{"token":null}'),
                                 (200, b'{"token":"bad\\nheader"}'), (200, b'{"token":"******"}')]:
                with self.subTest(authenticated=authenticated, status=status, body=body):
                    self.token_request(authenticated, status, body)
                    with self.assertRaises(SystemExit):
                        publish.registry_token(authenticated)
        self.run.assert_not_called()

    def test_missing_job_credentials_fail_before_transport(self):
        for key in ("GITHUB_ACTOR", "GHCR_TOKEN"):
            with self.subTest(key=key), patch.dict(publish.os.environ, {key: ""}):
                with self.assertRaisesRegex(SystemExit, "requires GITHUB_ACTOR and GHCR_TOKEN"):
                    publish.registry_token(True)
        self.assertEqual(self.seen, [])
        self.run.assert_not_called()

    def test_digest_mismatch_and_conflicting_tag_never_write(self):
        self.manifest_request(header_digest="sha256:" + "f" * 64)
        with self.assertRaisesRegex(SystemExit, "independently hashed"):
            publish.manifest("sha-" + self.commit, self.token)
        self.token_request()
        self.manifest_request(body=b'{"config":{"digest":"sha256:other"}}')
        with self.assertRaisesRegex(SystemExit, "refusing to overwrite"):
            publish.main()
        self.run.assert_not_called()
        self.write.assert_not_called()

    def test_unknown_manifest_write_is_not_replayed_or_recorded(self):
        self.token_request()
        self.manifest_request(status=404)
        self.upload_requests(fail=503)
        with self.assertRaisesRegex(SystemExit, "outcome unknown"):
            publish.main()
        self.assertEqual(sum(req.method == "PUT" and "/manifests/" in req.full_url for req in self.seen), 1)
        self.write.assert_not_called()

    def test_lost_manifest_write_response_is_not_replayed(self):
        self.upload_requests(existing=True)
        request = list(self.responses[-1])
        request[3] = urllib.error.URLError("connection lost")
        self.responses[-1] = tuple(request)
        with self.assertRaisesRegex(SystemExit, "outcome unknown"):
            publish.publish_image(self.config_id, self.config_id, "sha-" + self.commit, self.token)
        self.assertEqual(sum(req.method == "PUT" for req in self.seen), 1)
        self.write.assert_not_called()

    def test_lost_blob_write_response_is_not_replayed(self):
        path = f"/v2/{publish.PACKAGE}/blobs/uploads/fixture"
        self.enqueue("HEAD", f"/v2/{publish.PACKAGE}/blobs/{self.config_id}", 404, auth="Bearer " + self.token)
        self.enqueue("POST", f"/v2/{publish.PACKAGE}/blobs/uploads/", 202,
                     headers={"Location": path}, auth="Bearer " + self.token)
        self.enqueue("PUT", path + "?digest=" + self.config_id.replace(":", "%3A"),
                     body=urllib.error.URLError("connection lost"), auth="Bearer " + self.token)
        with self.assertRaisesRegex(SystemExit, "outcome unknown"):
            publish.publish_image(self.config_id, self.config_id, "sha-" + self.commit, self.token)
        self.write.assert_not_called()

    def test_readback_failure_does_not_repeat_known_write(self):
        self.token_request()
        self.manifest_request(status=404)
        self.upload_requests(existing=True)
        self.manifest_request(status=503)
        with self.assertRaisesRegex(SystemExit, "do not blindly retry"):
            publish.main()
        self.assertEqual(sum(req.method == "PUT" for req in self.seen), 1)
        self.write.assert_not_called()

    def test_readback_difference_is_not_success(self):
        self.token_request()
        self.manifest_request(status=404)
        self.upload_requests(existing=True)
        self.manifest_request()
        with self.assertRaisesRegex(SystemExit, "read-back differs"):
            publish.main()
        self.write.assert_not_called()

    def test_anonymous_verification_denial_never_records_private_success(self):
        for status in (401, 403):
            with self.subTest(status=status):
                self.token_request(False)
                self.manifest_request(digest(self.body), status=status, public=True)
                with self.assertRaises(SystemExit):
                    publish.manifest(digest(self.body), publish.registry_token(False))
        self.write.assert_not_called()

    def test_public_read_mismatch_does_not_record_success(self):
        self.token_request()
        self.manifest_request()
        self.token_request(False)
        self.manifest_request(digest(self.body), body=b'{"config":{}}', public=True)
        with self.assertRaisesRegex(SystemExit, "Anonymous digest"):
            publish.main()
        self.run.assert_not_called()
        self.write.assert_not_called()

    def test_runtime_smoke_failure_does_not_record_success(self):
        self.token_request()
        self.manifest_request()
        self.public_requests()
        self.run.side_effect = subprocess.CalledProcessError(1, "smoke")
        with self.assertRaises(subprocess.CalledProcessError):
            publish.main()
        self.write.assert_not_called()

    def test_upload_cannot_send_token_outside_bounded_registry(self):
        for location in ("https://other.invalid/upload", f"http://ghcr.io/v2/{publish.PACKAGE}/blobs/uploads/x",
                         "https://ghcr.io/v2/other/package/blobs/uploads/x", "",
                         f"/v2/{publish.PACKAGE}/blobs/uploads/%2e%2e",
                         f"/v2/{publish.PACKAGE}/blobs/uploads/x?digest="):
            with self.subTest(location=location):
                self.upload_requests(location=location or "https://other.invalid/")
                with self.assertRaisesRegex(SystemExit, "unbounded"):
                    publish.publish_image(self.config_id, self.config_id, "sha-" + self.commit, self.token)

    def test_redirect_is_not_followed_and_cannot_replay_write_or_leak_token(self):
        self.enqueue("PUT", f"/v2/{publish.PACKAGE}/manifests/sha-{self.commit}", 307,
                     headers={"Location": "https://other.invalid/write"}, auth="Bearer " + self.token)
        status, _, _ = publish.request(
            f"https://ghcr.io/v2/{publish.PACKAGE}/manifests/sha-{self.commit}",
            {**publish.bearer_headers(self.token), "Content-Type": "application/vnd.oci.image.manifest.v1+json"},
            "PUT", b"fixture",
        )
        self.assertEqual(status, 307)
        self.assertEqual(len(self.seen), 1)


if __name__ == "__main__":
    unittest.main()
