import copy
import json
from pathlib import Path
import subprocess
import unittest


class ContainerAdmissionTests(unittest.TestCase):
    def test_adjacent_or_invalid_unit_never_executes(self):
        image = Path(".runner-reference/image-id").read_text().strip()
        request = json.loads(Path("examples/runner/request.json").read_text())
        command = [
            "docker", "run", "--rm", "--interactive", "--platform", "linux/amd64",
            "--network=none", "--read-only",
            "--tmpfs", "/work:rw,noexec,nosuid,size=32m,uid=65532,gid=65532,mode=0700",
            image,
        ]
        for field, value in (("shard", "neighbor"), ("task", "neighbor"), ("trial", 4), ("attempt", 4)):
            with self.subTest(field=field):
                invalid = copy.deepcopy(request)
                invalid["selection"][field] = value
                process = subprocess.run(command, input=json.dumps(invalid), text=True, capture_output=True, timeout=90)
                self.assertEqual(process.returncode, 6, process.stderr)
                result = json.loads(process.stdout)
                self.assertEqual(result["outcome"], "invalid_request")
                self.assertEqual(result["exit_code"], 6)
                self.assertFalse(result["selection_valid"])
                self.assertNotIn("observation", result)
                self.assertNotIn("identity", result)
                self.assertNotIn("resolved", result)
                validate = subprocess.run(
                    ["docker", "run", "--rm", "--interactive", "--platform", "linux/amd64",
                     "--network=none", "--read-only", "--entrypoint", "/waza",
                     image, "runner", "validate-result"],
                    input=process.stdout, text=True, capture_output=True, timeout=30,
                )
                self.assertEqual(validate.returncode, 0, validate.stderr)


if __name__ == "__main__":
    unittest.main()
