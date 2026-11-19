import json
from pathlib import Path
import runpy
import shutil
import unittest
import uuid

verify_module = runpy.run_path(str(Path(__file__).with_name("go_remaining_shard_verify.py")))
verify = verify_module["verify"]
merge_coverage = verify_module["merge_coverage"]
read_coverage = verify_module["read_coverage"]

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
                    "shard": str(shard),
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

    def move_shards_to_attempt(self, attempt, shards):
        for shard in shards:
            prefix = f"coverage-remaining-{shard}.txt"
            artifact_dir = self.root / f"go-remaining-shard-{shard}-12345-{attempt}"
            artifact_dir.mkdir()
            for source in list(self.root.glob(f"{prefix}*")):
                shutil.move(source, artifact_dir / source.name)
            receipt = artifact_dir / f"{prefix}.evidence.json"
            report = json.loads(receipt.read_text(encoding="utf-8"))
            report["identity"]["attempt"] = str(attempt)
            receipt.write_text(json.dumps(report), encoding="utf-8")

    def copy_shards_to_attempt(self, source_attempt, target_attempt, shards):
        for shard in shards:
            prefix = f"coverage-remaining-{shard}.txt"
            source_dir = self.root / f"go-remaining-shard-{shard}-12345-{source_attempt}"
            artifact_dir = self.root / f"go-remaining-shard-{shard}-12345-{target_attempt}"
            artifact_dir.mkdir()
            for source in source_dir.glob(f"{prefix}*"):
                shutil.copy2(source, artifact_dir / source.name)
            receipt = artifact_dir / f"{prefix}.evidence.json"
            report = json.loads(receipt.read_text(encoding="utf-8"))
            report["identity"]["attempt"] = str(target_attempt)
            receipt.write_text(json.dumps(report), encoding="utf-8")

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

    def test_accepts_testless_skipped_package_but_rejects_skipped_selected_tests(self):
        self.create_artifacts()
        empty_package = "example/pkg-z-empty"
        packages = [f"example/pkg-{shard}" for shard in range(4)] + [empty_package]
        for shard in range(4):
            prefix = f"coverage-remaining-{shard}.txt"
            (self.root / f"{prefix}.packages.all").write_text(
                "\n".join(sorted(packages)) + "\n", encoding="utf-8"
            )
        prefix = "coverage-remaining-0.txt"
        selected_packages = sorted(["example/pkg-0", empty_package])
        (self.root / f"{prefix}.packages.selected").write_text(
            "\n".join(selected_packages) + "\n", encoding="utf-8"
        )
        receipt = self.root / f"{prefix}.evidence.json"
        report = json.loads(receipt.read_text(encoding="utf-8"))
        report["packages"].append({
            "package": empty_package,
            "executed": True,
            "status": "skip",
        })
        receipt.write_text(json.dumps(report), encoding="utf-8")

        verified, _ = verify(self.root)
        self.assertEqual(verified["package_count"], 5)

        self.create_artifacts()
        receipt = self.root / f"{prefix}.evidence.json"
        report = json.loads(receipt.read_text(encoding="utf-8"))
        report["packages"][0]["status"] = "skip"
        receipt.write_text(json.dumps(report), encoding="utf-8")
        with self.assertRaisesRegex(ValueError, "skipped a package with selected tests"):
            verify(self.root)

    def test_merger_selects_newest_successful_attempt_per_shard(self):
        self.create_artifacts()
        self.move_shards_to_attempt(1, range(4))
        self.copy_shards_to_attempt(1, 2, [0, 1])

        failed_receipt = (
            self.root / "go-remaining-shard-1-12345-2" / "coverage-remaining-1.txt.evidence.json"
        )
        report = json.loads(failed_receipt.read_text(encoding="utf-8"))
        report["complete"] = False
        report["diagnostics"] = ["execution-failed"]
        report["execution_exit"] = 7
        failed_receipt.write_text(json.dumps(report), encoding="utf-8")

        report, _ = verify(self.root)
        self.assertEqual(report["attempts"], {"0": "2", "1": "1", "2": "1", "3": "1"})

    def test_rejects_mixed_run_artifacts_across_attempts(self):
        self.create_artifacts()
        self.move_shards_to_attempt(1, range(3))
        self.move_shards_to_attempt(2, [3])
        artifact = self.root / "go-remaining-shard-3-12345-2"
        artifact.rename(self.root / "go-remaining-shard-3-67890-2")

        with self.assertRaisesRegex(ValueError, "do not share one workflow run"):
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

    def test_accepts_zero_statement_coverage_blocks(self):
        profile = self.root / "coverage-zero.txt"
        profile.write_text(
            "mode: atomic\ninternal/models/grader_params.go:1.1,1.1 0 0\n",
            encoding="utf-8",
        )
        block = ("internal/models/grader_params.go", (1, 1), (1, 1))
        self.assertEqual(read_coverage(profile), {block: (0, 0)})
        self.assertEqual(
            merge_coverage([read_coverage(profile)]),
            "mode: atomic\ninternal/models/grader_params.go:1.1,1.1 0 0\n",
        )

    def test_rejects_receipt_from_another_shard(self):
        self.create_artifacts()
        receipt = self.root / "coverage-remaining-2.txt.evidence.json"
        report = json.loads(receipt.read_text(encoding="utf-8"))
        report["identity"]["shard"] = "1"
        receipt.write_text(json.dumps(report), encoding="utf-8")
        with self.assertRaisesRegex(ValueError, "source/run identity is invalid"):
            verify(self.root)


if __name__ == "__main__":
    unittest.main()
