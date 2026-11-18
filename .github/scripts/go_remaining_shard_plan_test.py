import io
from pathlib import Path
import runpy
import subprocess
import sys
import unittest

plan = runpy.run_path(str(Path(__file__).with_name("go_remaining_shard_plan.py")))
assignments = plan["assignments"]
select_tests = plan["select_tests"]


class RemainingShardPlanTests(unittest.TestCase):
    def test_checked_in_cost_table_is_complete_and_assignable(self):
        source = Path(__file__).with_name("go_remaining_package_durations.tsv")
        history = source.read_text(encoding="utf-8").splitlines()
        packages = [line.split()[0] for line in history if line.strip() and not line.lstrip().startswith("#")]
        groups = assignments(io.StringIO("\n".join(packages)), io.StringIO("\n".join(history)))
        self.assertEqual(len(packages), 111)
        self.assertTrue(all(groups))
        self.assertEqual(sorted(package for group in groups for package in group), sorted(packages))

    def test_deterministic_assignment_uses_measured_package_costs(self):
        packages = [f"example/pkg-{index}" for index in range(8)]
        history = "\n".join(f"{package} {100 - index * 10}" for index, package in enumerate(packages))
        expected = assignments(io.StringIO("\n".join(packages)), io.StringIO(history))
        self.assertEqual(expected, assignments(io.StringIO("\n".join(reversed(packages))),
                                               io.StringIO("\n".join(reversed(history.splitlines())))))
        self.assertEqual(
            expected,
            [
                ["example/pkg-0", "example/pkg-7"],
                ["example/pkg-1", "example/pkg-6"],
                ["example/pkg-2", "example/pkg-5"],
                ["example/pkg-3", "example/pkg-4"],
            ],
        )

    def test_exactly_once_selection_includes_unmeasured_tests_and_packages(self):
        packages = [f"example/pkg-{index}" for index in range(5)]
        history = "\n".join(f"{package} {10 + index}" for index, package in enumerate(packages[:4]))
        tests = [
            f"{packages[0]}::TestKnown",
            f"{packages[0]}::TestAddedWithoutTiming",
            f"{packages[1]}::TestB",
            f"{packages[2]}::TestC",
            f"{packages[3]}::TestD",
            f"{packages[4]}::TestNewPackageWithoutTiming",
        ]
        package_groups = assignments(io.StringIO("\n".join(packages)), io.StringIO(history))
        selected_packages = [group for group in package_groups]
        selected_tests = [
            select_tests(io.StringIO("\n".join(tests)), io.StringIO("\n".join(packages)), group)
            for group in selected_packages
        ]
        self.assertEqual(sorted(package for group in selected_packages for package in group), packages)
        self.assertEqual(len([package for group in selected_packages for package in group]), len(packages))
        flattened_tests = [target for group in selected_tests for target in group]
        self.assertEqual(sorted(flattened_tests), sorted(tests))
        self.assertEqual(len(flattened_tests), len(set(flattened_tests)))
        self.assertIn(f"{packages[0]}::TestAddedWithoutTiming", flattened_tests)
        self.assertIn(f"{packages[4]}::TestNewPackageWithoutTiming", flattened_tests)

    def test_rejects_invalid_costs_inventories_and_native_package(self):
        for history in ("example/pkg-a nan\n", "example/pkg-a inf\n", "example/pkg-a -1\n",
                        "example/pkg-a 1\nexample/pkg-a 2\n", "broken\n"):
            with self.subTest(history=history), self.assertRaises(ValueError):
                assignments(io.StringIO("\n".join(f"example/pkg-{i}" for i in range(4))),
                            io.StringIO(history))
        for packages in ("", "example/pkg-a\nexample/pkg-a\n",
                         "github.com/microsoft/waza/internal/controlledcomparison\n" +
                         "\n".join(f"example/pkg-{i}" for i in range(3))):
            with self.subTest(packages=packages), self.assertRaises(ValueError):
                assignments(io.StringIO(packages), io.StringIO(""))

    def test_rejects_unknown_package_references_in_test_inventory(self):
        with self.assertRaisesRegex(ValueError, "unassigned test target"):
            select_tests(
                io.StringIO("example/pkg-a::TestA\nexample/pkg-new::TestNew\n"),
                io.StringIO("\n".join(f"example/pkg-{i}" for i in range(4))),
                ["example/pkg-0"],
            )

    def test_cli_writes_lf_delimited_assignments_on_every_platform(self):
        source = Path(__file__).with_name("go_remaining_shard_plan.py")
        durations = Path(__file__).with_name("go_remaining_package_durations.tsv")
        result = subprocess.run(
            [sys.executable, str(source), str(durations), "0"],
            input=b"example/pkg-0\nexample/pkg-1\nexample/pkg-2\nexample/pkg-3\n",
            capture_output=True,
            check=True,
        )
        self.assertEqual(result.stdout, b"example/pkg-0\n")


if __name__ == "__main__":
    unittest.main()
