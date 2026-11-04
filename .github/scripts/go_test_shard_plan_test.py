import io
from pathlib import Path
import runpy
import unittest

select = runpy.run_path(str(Path(__file__).with_name("go_test_shard_plan.py")))["select"]


class ShardPlanTests(unittest.TestCase):
    def plans(self, names, history):
        return [select(io.StringIO(names), io.StringIO(history), shard) for shard in range(4)]

    def test_balances_durations_not_test_counts(self):
        history = "TestA 100\nTestB 90\nTestC 80\nTestD 70\nTestE 60\nTestF 50\nTestG 40\nTestH 30\n"
        names = [f"Test{letter}" for letter in "ABCDEFGH"]
        plans = self.plans("\n".join(names), history)
        durations = dict(line.split() for line in history.splitlines())
        loads = [sum(float(durations[name]) for name in plan) for plan in plans]
        self.assertEqual(loads, [130, 130, 130, 130])
        self.assertLess(max(loads), 160)  # Alphabetical round-robin's slowest shard.
        self.assertEqual(plans, self.plans("\n".join(reversed(names)), history))
        self.assertEqual(sorted(name for plan in plans for name in plan), names)

    def test_unknown_stale_zero_unicode_examples_and_fuzz(self):
        names = ["ExampleComparison", "FuzzBytes", "TestNew", "TestZero", "Test\u03a9mega"]
        plans = self.plans("\n".join(names), "TestRemoved 100\nTestZero 0\n")
        self.assertEqual(sorted(name for plan in plans for name in plan), names)
        self.assertTrue(all(plans))
        self.assertEqual(plans, self.plans("\n".join(names), "TestZero 0\nTestRemoved 100\n"))

    def test_without_history(self):
        plans = self.plans("TestA\nTestB\nTestC\nTestD\nTestE\n", "# no history\n")
        self.assertEqual(plans, [["TestA", "TestE"], ["TestB"], ["TestC"], ["TestD"]])

    def test_rejects_bad_inputs(self):
        for history in ("TestA nan\n", "TestA inf\n", "TestA -1\n", "TestA 1\nTestA 2\n", "broken\n"):
            with self.subTest(history=history), self.assertRaises(ValueError):
                self.plans("TestA\n", history)
        for inventory in ("", "TestA\nTestA\n"):
            with self.subTest(inventory=inventory), self.assertRaises(ValueError):
                self.plans(inventory, "")


if __name__ == "__main__":
    unittest.main()
