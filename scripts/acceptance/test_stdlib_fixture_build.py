"""Local source/toolchain test only; no Agent, service, browser, socket or container.

Run this separately with preinstalled Go 1.25.6 and Git on PATH. It intentionally
has no toolchain download/fallback and does not substitute for actual-stack CI.
"""

import os
from pathlib import Path
import subprocess
import tempfile
import unittest

import run_actual_app as app


class StdlibFixtureBuildTests(unittest.TestCase):
    def test_real_compile_failure_new_commit_and_built_program(self):
        with tempfile.TemporaryDirectory(prefix="arcentra-local-stdlib-") as directory:
            root = Path(directory)
            env = app.child_environment(os.environ, root)
            app.verify_toolchain(env)
            source = root / "source"
            app.copy_fixture_source(source)

            def command(*args, success=True):
                result = subprocess.run(args, cwd=source, env=env, capture_output=True, text=True, timeout=120)
                if success:
                    self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                return result

            def commit(message):
                command("git", "add", ".")
                command("git", "-c", "user.name=Acceptance Fixture", "-c", "user.email=acceptance@example.invalid",
                        "commit", "-m", message)
                return command("git", "rev-parse", "HEAD").stdout.strip()

            command("git", "init", "--initial-branch=main")
            main = source / "main.go"
            main.write_text(main.read_text() + app.COMPILE_ERROR)
            failed_commit = commit("Synthetic project with compiler error")
            checked = command("sh", "build.sh", "toolchain")
            self.assertIn("ACTUAL_PROJECT_TOOLCHAIN go1.25.6", checked.stdout)
            failed = command("sh", "build.sh", "test", success=False)
            self.assertNotEqual(failed.returncode, 0)
            self.assertIn("undefined: acceptanceCompileError", failed.stdout + failed.stderr)
            self.assertFalse((source / app.ARTIFACT_NAME).exists())
            self.assertFalse((source / app.MANIFEST_NAME).exists())
            main.write_bytes((app.FIXTURE_SOURCE / "main.go").read_bytes())
            fixed_commit = commit("Fix synthetic project compiler error")
            self.assertNotEqual(failed_commit, fixed_commit)
            for stage in ("toolchain", "test", "build", "verify"):
                command("sh", "build.sh", stage)
            app.validate_manifest((source / app.MANIFEST_NAME).read_bytes(),
                                  (source / app.ARTIFACT_NAME).read_bytes(), fixed_commit)
            app.verify_downloaded_program((source / app.ARTIFACT_NAME).read_bytes(), root, env)
            self.assertEqual(command("./stdlib-demo", "arcentra").stdout, app.EXPECTED_OUTPUT)
            invalid = command("./stdlib-demo", "agent1", success=False)
            self.assertEqual(invalid.returncode, 2)
            self.assertEqual(invalid.stdout, "")
            self.assertTrue(invalid.stderr)
            # No module download can have supplied the compiler or project code.
            self.assertFalse((source / "go.sum").exists())
            self.assertEqual(list((source / ".cache/go-mod").iterdir()), [])


if __name__ == "__main__":
    unittest.main()
