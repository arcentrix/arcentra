"""Offline fixture checks only: no services, sockets or browser are started."""

import base64
from pathlib import Path
import tempfile
import tomllib
import unittest

import run_actual_app as app


class ActualAppFixtureTests(unittest.TestCase):
    def test_requires_explicit_disposable_runner_opt_in(self):
        valid = {"ARCENTRA_REAL_STACK": "1", "GITHUB_ACTIONS": "true",
                 "RUNNER_ENVIRONMENT": "github-hosted"}
        app.validate_environment(valid)
        for key in valid:
            with self.subTest(key=key), self.assertRaises(ValueError):
                app.validate_environment({k: v for k, v in valid.items() if k != key})
        with self.assertRaises(ValueError):
            app.validate_environment(valid | {"RUNNER_ENVIRONMENT": "self-hosted"})

    def test_refuses_every_dependency_override(self):
        valid = {"ARCENTRA_REAL_STACK": "1", "GITHUB_ACTIONS": "true",
                 "RUNNER_ENVIRONMENT": "github-hosted"}
        for key in app.DEPENDENCIES:
            with self.subTest(key=key), self.assertRaises(ValueError):
                app.validate_environment(valid | {key: "production.example.invalid"})

    def test_http_never_leaves_fixed_loopback_ports(self):
        for url in (app.BACKEND_URL + "/ready", app.AGENT_URL + "/live"):
            app.validate_http_url(url)
        for url in ("http://example.invalid:18080", "http://localhost:18080",
                    "http://127.0.0.1:80", "https://127.0.0.1:18080",
                    "http://user:secret@127.0.0.1:18080", "http://127.0.0.1:18080/#fragment"):
            with self.subTest(url=url), self.assertRaises(ValueError):
                app.validate_http_url(url)

    def test_configs_preserve_authorization_tls_and_separate_identity(self):
        root = Path('/tmp/fixture with spaces')
        control = tomllib.loads(app.control_config(root, "synthetic-jwt"))
        agent = tomllib.loads(app.agent_config(root, "art_synthetic"))
        self.assertTrue(control["authz"]["enabled"])
        self.assertTrue(control["grpc"]["tls"]["enabled"])
        self.assertEqual(control["database"]["mysql"]["dsn"], app.DEPENDENCIES["ARCENTRA_TEST_MYSQL_DSN"])
        self.assertTrue(agent["grpc"]["tls"]["enabled"])
        self.assertEqual(agent["grpc"]["tls"]["serverName"], "arcentra-acceptance.local")
        self.assertEqual(agent["grpc"]["tls"]["caFile"], str(root / "ca.pem"))
        self.assertEqual(agent["agent"]["mode"], "baremetal")
        self.assertTrue(agent["agent"]["identityPersistence"]["enabled"])
        self.assertEqual(agent["agent"]["identityPersistence"]["directory"], str(root / "identity"))
        self.assertEqual(agent["agent"]["workspaceDir"], str(root / "workspaces"))
        self.assertEqual(tomllib.loads(app.agent_config(root, ""))["grpc"]["registrationToken"], "")
        for config in (control, agent):
            self.assertEqual(config["taskQueue"]["messageFormat"], "sonic")
            self.assertEqual(config["taskQueue"]["messageCodec"], "sonic")
            for listener in ("http", "metrics", "pprof"):
                self.assertEqual(config[listener]["host"], "127.0.0.1")
            self.assertFalse(config["metrics"]["enable"])
            self.assertFalse(config["pprof"]["enable"])

    def test_child_does_not_inherit_host_credentials_or_proxies(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            env = app.child_environment({"PATH": "/bin", "HOME": "/fixture-original",
                                         "AWS_SECRET_ACCESS_KEY": "not-for-child", "GH_TOKEN": "not-for-child",
                                         "HTTPS_PROXY": "not-for-child", "SSH_AUTH_SOCK": "not-for-child"}, root)
            for name in ("AWS_SECRET_ACCESS_KEY", "GH_TOKEN", "HTTPS_PROXY", "SSH_AUTH_SOCK"):
                self.assertNotIn(name, env)
            self.assertEqual(env["HOME"], str(root / "home"))
            self.assertEqual(env["PLAYWRIGHT_BROWSERS_PATH"], "/fixture-original/.cache/ms-playwright")
            self.assertEqual(env["GIT_CONFIG_NOSYSTEM"], "1")

    def test_source_and_all_steps_require_actual_agent_execution(self):
        spec = app.pipeline_spec()
        self.assertEqual(spec["namespace"], "acceptance")
        job = spec["jobs"][0]
        self.assertEqual(job["source"], {"type": "git", "repo": app.REPO_URL, "branch": "main"})
        self.assertTrue(all(step["runOnAgent"] for step in job["steps"]))
        self.assertIn("test -f acceptance-marker", job["steps"][0]["args"]["run"])
        self.assertEqual(job["steps"][1]["args"]["paths"], [app.ARTIFACT_NAME])
        self.assertEqual(base64.b64decode(app.encoded(app.PASSWORD)).decode(), app.PASSWORD)


if __name__ == "__main__":
    unittest.main()
