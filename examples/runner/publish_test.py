import hashlib
import json
import subprocess
import unittest
from unittest.mock import patch

import publish


class RegistryTests(unittest.TestCase):
    def test_auth_failure_is_not_package_absence(self):
        with patch.object(publish, "request", return_value=(403, {}, b"")):
            with self.assertRaisesRegex(SystemExit, "HTTP 403"):
                publish.registry_token(False)

    def test_only_404_allows_first_publication(self):
        for status in (401, 403, 429, 500):
            with self.subTest(status=status), patch.object(publish, "request", return_value=(status, {}, b"")):
                with self.assertRaisesRegex(SystemExit, "do not blindly retry"):
                    publish.manifest("sha-commit", "token", allow_missing=True)
        with patch.object(publish, "request", return_value=(404, {}, b"")):
            self.assertIsNone(publish.manifest("sha-commit", "token", allow_missing=True))

    def test_digest_is_independently_hashed(self):
        body = json.dumps({"config": {"digest": "sha256:config"}}).encode()
        digest = "sha256:" + hashlib.sha256(body).hexdigest()
        with patch.object(publish, "request", return_value=(200, {"Docker-Content-Digest": digest}, body)):
            self.assertEqual(publish.manifest("tag", "token"), (digest, json.loads(body)))
        with patch.object(publish, "request", return_value=(200, {"Docker-Content-Digest": "sha256:invented"}, body)):
            with self.assertRaisesRegex(SystemExit, "independently hashed"):
                publish.manifest("tag", "token")


class PublicationTests(unittest.TestCase):
    def setUp(self):
        self.commit = "a" * 40
        self.local_id = "sha256:" + "b" * 64
        self.existing = ("sha256:" + "c" * 64, {"config": {"digest": self.local_id}})
        patches = [
            patch.object(publish.os, "chdir"),
            patch.object(publish.subprocess, "check_output", side_effect=[self.commit, "", self.commit]),
            patch.object(publish.Path, "read_text", return_value=self.local_id),
            patch.object(publish.Path, "unlink"),
            patch.object(publish.Path, "write_text"),
            patch.object(publish, "registry_token", return_value="not-a-credential"),
            patch.dict(publish.os.environ, {"GITHUB_STEP_SUMMARY": ""}),
            patch("builtins.print"),
        ]
        self.mocks = [self.enterContext(patcher) for patcher in patches]
        self.write = self.mocks[4]

    def test_existing_commit_is_reconciled_without_push(self):
        with patch.object(publish, "manifest", side_effect=[self.existing, self.existing]), patch.object(publish.subprocess, "run") as run:
            publish.main()
            self.assertEqual(run.call_count, 1)
            self.assertEqual(run.call_args.args[0][:2], ["bash", "examples/runner/smoke.sh"])
            self.write.assert_called_once()

    def test_conflicting_commit_is_not_overwritten(self):
        conflict = (self.existing[0], {"config": {"digest": "sha256:other"}})
        with patch.object(publish, "manifest", return_value=conflict), patch.object(publish.subprocess, "run") as run:
            with self.assertRaisesRegex(SystemExit, "refusing to overwrite"):
                publish.main()
            run.assert_not_called()
            self.write.assert_not_called()

    def test_first_push_is_read_back_before_public_smoke(self):
        with patch.object(publish, "manifest", side_effect=[None, self.existing, self.existing]) as manifest, patch.object(publish.subprocess, "run") as run:
            publish.main()
            self.assertEqual(manifest.call_count, 3)
            self.assertEqual([call.args[0][1] for call in run.call_args_list], ["tag", "push", "examples/runner/smoke.sh"])
            self.write.assert_called_once()

    def test_unknown_push_outcome_is_not_retried(self):
        with patch.object(publish, "manifest", return_value=None) as manifest, patch.object(
            publish.subprocess, "run", side_effect=[None, subprocess.CalledProcessError(1, "docker push")]
        ) as run:
            with self.assertRaises(subprocess.CalledProcessError):
                publish.main()
            self.assertEqual(manifest.call_count, 1)
            self.assertEqual(run.call_count, 2)
            self.write.assert_not_called()

    def test_public_read_mismatch_does_not_record_success(self):
        with patch.object(publish, "manifest", side_effect=[self.existing, ("sha256:other", {})]), patch.object(publish.subprocess, "run") as run:
            with self.assertRaisesRegex(SystemExit, "Anonymous digest"):
                publish.main()
            run.assert_not_called()
            self.write.assert_not_called()

    def test_runtime_smoke_failure_does_not_record_success(self):
        with patch.object(publish, "manifest", side_effect=[self.existing, self.existing]), patch.object(
            publish.subprocess, "run", side_effect=subprocess.CalledProcessError(1, "smoke")
        ):
            with self.assertRaises(subprocess.CalledProcessError):
                publish.main()
            self.write.assert_not_called()


if __name__ == "__main__":
    unittest.main()
