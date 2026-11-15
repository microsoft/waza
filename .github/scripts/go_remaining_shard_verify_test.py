import json
from pathlib import Path
import runpy
import shutil
import unittest
import uuid

verify_module = runpy.run_path(str(Path(__file__).with_name("go_remaining_shard_verify.py")))
verify = verify_module["verify"]
merge_coverage = verify_module["merge_coverage"]

REPOSITORY = Path(__file__).resolve().parents[2]


class RemainingShardVerifyTests(unittest.TestCase):
    def setUp(self):
        self.root = REPOSITORY / f".ci-go-remaining-verify-test-{uuid.uuid4().hex}"
        self.root.mkdir()

    def tearDown(self):
        shutil.rmtree(self.root)

    def create_artifacts(self):
        packages = [f"example/pkg-{shard}" for shard in range(4)]
        tests = [f"{package}::TestCase{shard}" for shard, package in enumerate(packages)]
        for shard, package in enumerate(packages):
            prefix = f"coverage-remaining-{shard}.txt"
            selected_tests = [tests[shard]]
            (self.root / f"{prefix}.packages.all").write_text(
                "\n".join(packages) + "\n", encoding="utf-8"
            )
            (self.root / f"{prefix}.packages.selected").write_text(package + "\n", encoding="utf-8")
            (self.root / f"{prefix}.all").write_text("\n".join(tests) + "\n", encoding="utf-8")
            (self.root / f"{prefix}.selected").write_text(
                "\n".join(selected_tests) + "\n", encoding="utf-8"
            )
            report = {
                "kind": "go",
                "schema": 1,
                "complete": True,
                "diagnostics": [],
                "discovery_exit": 0,
                "discovery_collector_exit": 0,
                "execution_exit": 0,
                "collector_exit": 0,
                "inventory_scope": "qualified",
                "policy": {
                    "retries": 0,
                    "count": 1,
                    "race": True,
                    "covermode": "atomic",
                    "timeout": "30m",
                },
                "identity": {
                    "source_sha": "a" * 40,
                    "workflow_sha": "a" * 40,
                    "repository": "microsoft/waza",
                    "workflow": "Go Build and Test",
                    "job": "remaining",
                    "os": "Linux",
                    "run_id": "12345",
                    "attempt": "1",
                    "event": "push",
                },
                "discovered": tests,
                "selected": selected_tests,
                "packages": [{"package": package, "executed": True, "status": "pass"}],
                "targets": [{
                    "package": package,
                    "name": f"TestCase{shard}",
                    "top_level": True,
                    "executed": True,
                    "status": "pass",
                }],
            }
            (self.root / f"{prefix}.evidence.json").write_text(
                json.dumps(report), encoding="utf-8"
            )
            (self.root / prefix).write_text(
                f"mode: atomic\n{package}/test.go:1.1,2.1 1 {shard + 1}\n",
                encoding="utf-8",
            )

    def test_verifies_complete_exact_once_receipts_and_merges_profiles(self):
        self.create_artifacts()
        report, profile = verify(self.root)
        self.assertEqual(report["shard_count"], 4)
        self.assertEqual(report["package_count"], 4)
        self.assertEqual(report["test_count"], 4)
        self.assertEqual(report["coverage_block_count"], 4)
        self.assertTrue(profile.startswith("mode: atomic\n"))
        self.assertEqual(len(profile.splitlines()), 5)

    def test_propagates_execution_failure_and_missing_receipts(self):
        self.create_artifacts()
        receipt = self.root / "coverage-remaining-2.txt.evidence.json"
        report = json.loads(receipt.read_text(encoding="utf-8"))
        report["complete"] = False
        report["execution_exit"] = 7
        report["diagnostics"] = ["execution-failed"]
        receipt.write_text(json.dumps(report), encoding="utf-8")
        with self.assertRaisesRegex(ValueError, "evidence is incomplete"):
            verify(self.root)

        self.create_artifacts()
        (self.root / "coverage-remaining-3.txt.evidence.json").unlink()
        with self.assertRaisesRegex(ValueError, "receipt is missing"):
            verify(self.root)

    def test_rejects_duplicate_package_selection_and_non_atomic_coverage(self):
        self.create_artifacts()
        prefix = "coverage-remaining-1.txt"
        (self.root / f"{prefix}.packages.selected").write_text(
            "example/pkg-0\n", encoding="utf-8"
        )
        (self.root / f"{prefix}.selected").write_text(
            "example/pkg-0::TestCase0\n", encoding="utf-8"
        )
        receipt = self.root / f"{prefix}.evidence.json"
        report = json.loads(receipt.read_text(encoding="utf-8"))
        report["selected"] = ["example/pkg-0::TestCase0"]
        report["packages"] = [{"package": "example/pkg-0", "executed": True, "status": "pass"}]
        report["targets"] = [{
            "package": "example/pkg-0",
            "name": "TestCase0",
            "top_level": True,
            "executed": True,
            "status": "pass",
        }]
        receipt.write_text(json.dumps(report), encoding="utf-8")
        with self.assertRaisesRegex(ValueError, "exactly once"):
            verify(self.root)

        self.create_artifacts()
        (self.root / "coverage-remaining-0.txt").write_text(
            "mode: set\nexample/pkg-0/test.go:1.1,2.1 1 1\n", encoding="utf-8"
        )
        with self.assertRaisesRegex(ValueError, "nonempty atomic"):
            verify(self.root)

    def test_merges_duplicate_coverage_blocks_by_summing_counts(self):
        block = ("example/shared.go", (1, 1), (2, 1))
        profile = merge_coverage([
            {block: (2, 3)},
            {block: (2, 4)},
        ])
        self.assertEqual(profile, "mode: atomic\nexample/shared.go:1.1,2.1 2 7\n")
        with self.assertRaisesRegex(ValueError, "statement counts disagree"):
            merge_coverage([{block: (1, 1)}, {block: (2, 1)}])


if __name__ == "__main__":
    unittest.main()
