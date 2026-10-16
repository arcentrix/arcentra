"""Standalone test-entrypoint regression; uses only disposable temporary repos.

Run with: python3 scripts/test_clean_git_env.py
No daemon, external service, network, or real checkout mutations are used.
"""

import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[1]
WRAPPER = ROOT / "scripts" / "with-clean-git-env.sh"
BASH = shutil.which("bash")


class CleanGitEnvironmentTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="arcentra-clean-git-")
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.env = {
            key: value for key, value in os.environ.items()
            if not key.startswith("GIT_") and key not in {"BASH_ENV", "ENV", "SHELLOPTS", "BASHOPTS"}
        }
        self.env["HOME"] = str(self.root / "home")
        self.env["XDG_CONFIG_HOME"] = str(self.root / "xdg")
        Path(self.env["HOME"]).mkdir()
        Path(self.env["XDG_CONFIG_HOME"]).mkdir()
        self.sentinel = self.repo("sentinel")
        self.fixture = self.repo("fixture")

    def command(self, *args, cwd=None, env=None, input=None):
        return subprocess.run(args, cwd=cwd or self.fixture, env=self.env if env is None else env,
                              input=input, text=True, capture_output=True, timeout=30)

    def git(self, repo, *args):
        result = subprocess.run(["git", "-C", str(repo), *args], env=self.env,
                                text=True, capture_output=True, timeout=30)
        self.assertEqual(result.returncode, 0, "temporary repository operation failed")
        return result.stdout.strip()

    def repo(self, name):
        repo = self.root / name
        repo.mkdir()
        self.git(repo, "init", "-q")
        self.git(repo, "config", "user.name", "Temporary Fixture")
        self.git(repo, "config", "user.email", "fixture@example.invalid")
        self.git(repo, "config", "commit.gpgsign", "false")
        hooks = self.root / (name + "-empty-hooks")
        hooks.mkdir()
        self.git(repo, "config", "core.hooksPath", str(hooks))
        (repo / "initial.txt").write_text(name + "\n")
        self.git(repo, "add", "initial.txt")
        self.git(repo, "commit", "-q", "-m", "initial")
        return repo

    def snapshot(self, repo):
        return {str(path.relative_to(repo)): path.read_bytes()
                for path in repo.rglob("*") if path.is_file()}

    def poisoned(self):
        gitdir = self.sentinel / ".git"
        return self.env | {
            "GIT_DIR": str(gitdir), "GIT_WORK_TREE": str(self.sentinel),
            "GIT_COMMON_DIR": str(gitdir), "GIT_INDEX_FILE": str(gitdir / "index"),
            "GIT_OBJECT_DIRECTORY": str(gitdir / "objects"),
            "GIT_ALTERNATE_OBJECT_DIRECTORIES": str(gitdir / "objects"),
            "GIT_CONFIG": str(gitdir / "config"),
            "GIT_CONFIG_GLOBAL": str(gitdir / "config"),
            "GIT_CONFIG_SYSTEM": str(gitdir / "config"), "GIT_CONFIG_NOSYSTEM": "1",
            "GIT_CONFIG_COUNT": "2", "GIT_CONFIG_KEY_0": "user.name",
            "GIT_CONFIG_VALUE_0": "inherited override", "GIT_CONFIG_KEY_1": "core.bare",
            "GIT_CONFIG_VALUE_1": "true", "GIT_CONFIG_PARAMETERS": "'core.bare=true'",
            "GIT_CEILING_DIRECTORIES": str(self.root), "GIT_NAMESPACE": "wrong-namespace",
            "GIT_AUTHOR_NAME": "wrong-author", "GIT_COMMITTER_EMAIL": "wrong@example.invalid",
            "GIT_TRACE": str(self.root / "must-not-create-trace"),
            "GIT_UNKNOWN_FUTURE_OVERRIDE": "synthetic-secret\nwith another line",
        }

    def test_fixture_commit_cannot_touch_sentinel_repository(self):
        sentinel_head = self.git(self.sentinel, "rev-parse", "HEAD")
        sentinel_config = (self.sentinel / ".git" / "config").read_bytes()
        sentinel = self.snapshot(self.sentinel)
        fixture_head = self.git(self.fixture, "rev-parse", "HEAD")
        code = """
import os
from pathlib import Path
import subprocess
import sys
if any(key.startswith('GIT_') for key in os.environ):
    raise SystemExit('Git overrides survived sanitation')
repo = Path(sys.argv[1]).resolve()
actual = subprocess.check_output(['git', 'rev-parse', '--show-toplevel'], text=True).strip()
if Path(actual).resolve() != repo:
    raise SystemExit('Git fixture selected a different repository')
(repo / 'fixture-only.txt').write_text('fixture change\\n')
subprocess.run(['git', 'add', 'fixture-only.txt'], check=True)
subprocess.run(['git', 'commit', '-q', '-m', 'fixture-only-commit'], check=True)
subprocess.run(['git', 'config', '--local', 'fixture.cleaned', 'true'], check=True)
"""
        result = self.command(BASH, str(WRAPPER), sys.executable, "-c", code,
                              str(self.fixture), env=self.poisoned())
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertNotEqual(self.git(self.fixture, "rev-parse", "HEAD"), fixture_head)
        self.assertEqual(self.git(self.fixture, "log", "-1", "--format=%s"), "fixture-only-commit")
        self.assertEqual(self.git(self.fixture, "config", "--local", "fixture.cleaned"), "true")
        self.assertEqual(self.git(self.sentinel, "rev-parse", "HEAD"), sentinel_head)
        self.assertEqual((self.sentinel / ".git" / "config").read_bytes(), sentinel_config)
        self.assertEqual(self.snapshot(self.sentinel), sentinel)
        self.assertFalse((self.root / "must-not-create-trace").exists())
        self.assertNotIn("synthetic-secret", result.stdout + result.stderr)

    def test_preserves_arguments_stdin_non_git_environment_and_exit_status(self):
        arguments = ["", "two words", "literal; command", "line\nbreak", "*", "中文"]
        code = """
import json, os, sys
print(json.dumps([sys.argv[1:], sys.stdin.read(), os.environ.get('KEEP_ME'),
                  sorted(key for key in os.environ if key.startswith('GIT_'))]))
raise SystemExit(73)
"""
        result = self.command(BASH, str(WRAPPER), sys.executable, "-c", code, *arguments,
                              env=self.poisoned() | {"KEEP_ME": "preserved"}, input="stdin still available\n")
        self.assertEqual(result.returncode, 73)
        self.assertEqual(json.loads(result.stdout), [arguments, "stdin still available\n", "preserved", []])
        self.assertEqual(result.stderr, "")

    def assert_not_started(self, env):
        marker = self.root / "command-was-started"
        code = "from pathlib import Path; import sys; Path(sys.argv[1]).write_text('started')"
        result = self.command(BASH, str(WRAPPER), sys.executable, "-c", code, str(marker), env=env)
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(marker.exists())
        self.assertNotIn("synthetic-secret", result.stdout + result.stderr)

    def test_invalid_git_variable_fails_closed_without_logging_value(self):
        self.assert_not_started(self.env | {"GIT_BAD-NAME": "synthetic-secret"})

    def test_readonly_git_variable_fails_closed_without_logging_value(self):
        startup = self.root / "readonly-startup.sh"
        startup.write_text("readonly GIT_DIR\n")
        self.assert_not_started(self.env | {"GIT_DIR": "synthetic-secret", "BASH_ENV": str(startup)})

    def test_exported_unset_function_cannot_bypass_builtin_sanitation(self):
        code = "import os; raise SystemExit(any(k.startswith('GIT_') for k in os.environ))"
        env = self.poisoned() | {"BASH_FUNC_unset%%": "() { return 0;\n}"}
        result = self.command(BASH, str(WRAPPER), sys.executable, "-c", code, env=env)
        self.assertEqual(result.returncode, 0)
        self.assertNotIn("synthetic-secret", result.stdout + result.stderr)

    def test_environment_enumeration_failure_never_starts_command(self):
        bindir = self.root / "bin"
        bindir.mkdir()
        executable = bindir / "env"
        executable.write_text("#!/bin/bash\nprintf 'GIT_DIR=synthetic-secret\\0'\nexit 9\n")
        executable.chmod(0o700)
        self.assert_not_started(self.env | {"PATH": str(bindir) + os.pathsep + self.env["PATH"]})

    def test_make_test_uses_wrapper_before_go(self):
        scripts = self.fixture / "scripts"
        scripts.mkdir()
        shutil.copyfile(WRAPPER, scripts / WRAPPER.name)
        shutil.copyfile(ROOT / "Makefile", self.fixture / "Makefile")
        bindir = self.root / "bin"
        bindir.mkdir()
        report = self.root / "go-invocation.json"
        probe = self.root / "probe.py"
        probe.write_text("import json, os, sys\nfrom pathlib import Path\n"
                         "Path(os.environ['TEST_REPORT']).write_text(json.dumps([sys.argv[1:], "
                         "sorted(k for k in os.environ if k.startswith('GIT_'))]))\n")
        executable = bindir / "go"
        executable.write_text('#!/bin/bash\nexec "$TEST_PYTHON" "$TEST_PROBE" "$@"\n')
        executable.chmod(0o700)
        sentinel = self.snapshot(self.sentinel)
        env = self.poisoned() | {"PATH": str(bindir) + os.pathsep + self.env["PATH"],
                                "TEST_PYTHON": sys.executable, "TEST_PROBE": str(probe),
                                "TEST_REPORT": str(report)}
        # The real entrypoint runs this prerequisite; skip it only inside this
        # disposable self-test copy to avoid recursively invoking ourselves.
        result = self.command("make", "-o", "test-git-env", "test", env=env)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads(report.read_text()), [["test", "-race", "-count=1", "./..."], []])
        self.assertEqual(self.snapshot(self.sentinel), sentinel)

    def test_tracing_cannot_print_inherited_values_or_command_arguments(self):
        result = self.command(BASH, "-x", str(WRAPPER), sys.executable, "-c",
                              "raise SystemExit(0)", "synthetic-secret-command-argument",
                              env=self.poisoned())
        self.assertEqual(result.returncode, 0)
        self.assertNotIn("synthetic-secret", result.stdout + result.stderr)

    def test_missing_command_is_usage_error(self):
        result = self.command(BASH, str(WRAPPER))
        self.assertEqual(result.returncode, 2)


if __name__ == "__main__":
    unittest.main()
