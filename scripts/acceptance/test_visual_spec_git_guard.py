"""Real local Git receive-pack tests for the disposable visual-editor fixture."""
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

import visual_git_fixture as fixture

GUARD = Path(__file__).with_name('visual_spec_pre_receive.sh')


class VisualSpecGitGuardTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.work, self.remote = self.root / 'source', self.root / 'acceptance.git'
        self.env = {k: v for k, v in os.environ.items() if not k.startswith('GIT_')}
        self.env.update(GIT_CONFIG_NOSYSTEM='1', GIT_CONFIG_GLOBAL=os.devnull,
                        GIT_AUTHOR_NAME='Synthetic fixture', GIT_AUTHOR_EMAIL='fixture@example.invalid',
                        GIT_COMMITTER_NAME='Synthetic fixture', GIT_COMMITTER_EMAIL='fixture@example.invalid')
        self.git('init', '--initial-branch=main', str(self.work), cwd=self.root)
        (self.work / 'pipeline.json').write_text('{"namespace":"before"}\n')
        (self.work / 'source.txt').write_text('unchanged source\n')
        self.commit('baseline')
        self.git('clone', '--bare', str(self.work), str(self.remote), cwd=self.root)
        self.git('remote', 'add', 'origin', str(self.remote))
        hook = self.remote / 'hooks/pre-receive'
        shutil.copyfile(GUARD, hook)
        hook.chmod(0o700)
        self.baseline = self.git('rev-parse', 'HEAD').stdout.strip()
        self.git('update-ref', 'refs/arcentra/fixture-base', self.baseline, cwd=self.remote)
        self.git('config', 'receive.maxInputSize', '2097152', cwd=self.remote)

    def git(self, *args, cwd=None, check=True, input=None):
        return subprocess.run(['git', *args], cwd=cwd or self.work, env=self.env,
                              text=True, input=input, capture_output=True, check=check, timeout=10)

    def commit(self, message):
        self.git('add', '.')
        self.git('commit', '-m', message)

    def remote_head(self):
        return self.git('rev-parse', 'refs/heads/main', cwd=self.remote).stdout.strip()

    def assert_denied(self, *refs):
        result = self.git('push', 'origin', *refs, check=False)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('synthetic spec write rejected', result.stderr)
        self.assertEqual(self.remote_head(), self.baseline)

    def test_exact_single_regular_definition_commit_allowed(self):
        (self.work / 'pipeline.json').write_text('{"namespace":"after"}\n')
        self.commit('edit pipeline')
        expected = self.git('rev-parse', 'HEAD').stdout.strip()
        self.git('push', 'origin', 'HEAD:refs/heads/main')
        self.assertEqual(self.remote_head(), expected)
        self.assertEqual(self.git('show', 'HEAD:source.txt', cwd=self.remote).stdout, 'unchanged source\n')

    def test_second_successful_definition_update_is_rejected(self):
        (self.work / 'pipeline.json').write_text('{"namespace":"first"}\n')
        self.commit('first edit')
        self.git('push', 'origin', 'HEAD:refs/heads/main')
        self.baseline = self.remote_head()
        (self.work / 'pipeline.json').write_text('{"namespace":"second"}\n')
        self.commit('second edit')
        self.assert_denied('HEAD:refs/heads/main')

    def test_oversized_received_pack_rejected_before_persistence(self):
        import base64
        (self.work / 'pipeline.json').write_text('{"namespace":"changed"}\n')
        self.git('add', 'pipeline.json')
        tree = self.git('write-tree').stdout.strip()
        # A small final file cannot hide an unbounded incompressible commit message.
        message = base64.b64encode(os.urandom(3 * 1024 * 1024)).decode()
        commit = self.git('commit-tree', tree, '-p', self.baseline, input=message).stdout.strip()
        result = self.git('push', 'origin', commit + ':refs/heads/main', check=False)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('maximum allowed size', result.stderr)
        self.assertEqual(self.remote_head(), self.baseline)
        self.assertNotEqual(self.git('cat-file', '-e', commit, cwd=self.remote, check=False).returncode, 0)

    def test_wrong_branch_is_rejected(self):
        (self.work / 'pipeline.json').write_text('{}\n')
        self.commit('edit pipeline')
        self.assert_denied('HEAD:refs/heads/other')

    def test_source_changes_are_rejected(self):
        (self.work / 'pipeline.json').write_text('{}\n')
        (self.work / 'source.txt').write_text('changed source')
        self.commit('unapproved source edit')
        self.assert_denied('HEAD:refs/heads/main')

    def test_deletion_is_rejected(self):
        self.assert_denied(':refs/heads/main')

    def test_multiple_commits_are_rejected(self):
        for index in range(2):
            (self.work / 'pipeline.json').write_text('{"index":%d}\n' % index)
            self.commit('edit pipeline')
        self.assert_denied('HEAD:refs/heads/main')

    def test_multiple_refs_are_rejected(self):
        (self.work / 'pipeline.json').write_text('{}\n')
        self.commit('edit pipeline')
        self.assert_denied('HEAD:refs/heads/main', 'HEAD:refs/heads/other')

    def test_symlink_definition_is_rejected(self):
        (self.work / 'pipeline.json').unlink()
        (self.work / 'pipeline.json').symlink_to('source.txt')
        self.commit('replace with symlink')
        self.assert_denied('HEAD:refs/heads/main')

    def test_empty_definition_is_rejected(self):
        (self.work / 'pipeline.json').write_text('')
        self.commit('empty definition')
        self.assert_denied('HEAD:refs/heads/main')

    def test_executable_definition_is_rejected(self):
        (self.work / 'pipeline.json').chmod(0o755)
        self.commit('executable definition')
        self.assert_denied('HEAD:refs/heads/main')

    def test_oversized_definition_is_rejected(self):
        (self.work / 'pipeline.json').write_bytes(b'x' * (1048576 + 1))
        self.commit('oversized definition')
        self.assert_denied('HEAD:refs/heads/main')

    def test_deleted_definition_is_rejected(self):
        (self.work / 'pipeline.json').unlink()
        self.commit('delete definition')
        self.assert_denied('HEAD:refs/heads/main')

    def test_tag_update_is_rejected(self):
        self.assert_denied('HEAD:refs/tags/synthetic')

    def test_non_fast_forward_is_rejected_even_forced(self):
        self.git('checkout', '--orphan', 'unrelated')
        (self.work / 'pipeline.json').write_text('{"unrelated":true}\n')
        self.commit('unrelated root')
        self.assert_denied('+HEAD:refs/heads/main')


class VisualRepositoryPreparationTests(unittest.TestCase):
    def test_only_fixed_repo_enables_receive_pack_with_exact_guard(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            (root / 'repositories').mkdir()
            env = {k: v for k, v in os.environ.items() if not k.startswith('GIT_')}
            env.update(GIT_CONFIG_NOSYSTEM='1', GIT_CONFIG_GLOBAL=os.devnull)
            def run(name, command, cwd=None):
                return subprocess.run(command, cwd=cwd or root, env=env, capture_output=True, check=True, timeout=10)
            bare = fixture.prepare_visual_repository(root, run)
            self.assertEqual(bare, root / 'repositories/visual.git')
            self.assertEqual((bare / 'hooks/pre-receive').read_bytes(), GUARD.read_bytes())
            self.assertEqual((bare / 'hooks/pre-receive').stat().st_mode & 0o777, 0o700)
            self.assertEqual(run('read', ['git', 'config', '--get', 'daemon.receivepack'], bare).stdout.strip(), b'true')
            content = run('read', ['git', 'show', 'refs/heads/main:pipeline.json'], bare).stdout
            import json
            self.assertEqual(json.loads(content), fixture.INITIAL_SPEC)
            with self.assertRaises(ValueError):
                fixture.prepare_visual_repository(root, run)

    def test_symlink_parent_refused_before_git(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            (root / 'other').mkdir()
            (root / 'repositories').symlink_to(root / 'other', target_is_directory=True)
            with self.assertRaises(ValueError):
                fixture.prepare_visual_repository(root, lambda *args: self.fail('Git must not run'))
