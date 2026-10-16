#!/usr/bin/env python3
"""Bounded, no-push loader/entrypoint gate; NOT healthy-service acceptance."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import secrets
import shutil
import signal
import subprocess
import tempfile
import time

ROOT = Path(__file__).resolve().parents[2]
LABEL = "io.arcentra.install-smoke"
MISSING = "/conf.d/acceptance-missing.toml"
ERROR_MARKER = f"failed to read configuration file: open {MISSING}: no such file or directory"
ENTRYPOINTS = {
    "arcentra": ["/arcentra", "-conf", "/conf.d/config.toml", "-plugin-conf", "/conf.d/plugins.toml"],
    "arcentra-agent": ["/arcentra-agent", "-conf", "/conf.d/agent.toml"],
}
BINARIES = (*ENTRYPOINTS, "arcentra-cli")


def guard(env, machine=None, uid=None):
    required = {"ARCENTRA_INSTALL_IMAGE_SMOKE": "1", "GITHUB_ACTIONS": "true",
                "RUNNER_ENVIRONMENT": "github-hosted", "RUNNER_OS": "Linux", "RUNNER_ARCH": "X64"}
    if any(env.get(k) != v for k, v in required.items()):
        raise ValueError("requires explicit disposable GitHub-hosted Linux X64 opt-in")
    if (machine or platform.machine()) != "x86_64" or (os.getuid() if uid is None else uid) == 0:
        raise ValueError("requires non-root Linux amd64 runner")
    if not all(re.fullmatch(r"[0-9]+", env.get(k, "")) for k in ("GITHUB_RUN_ID", "GITHUB_RUN_ATTEMPT")):
        raise ValueError("requires CI run ID and attempt")
    if not re.fullmatch(r"[0-9a-f]{40}", env.get("GITHUB_SHA", "")):
        raise ValueError("requires full current CI checkout SHA")
    if any(env.get(k) for k in ("DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH")):
        raise ValueError("remote/custom Docker endpoints are forbidden")


def validate_build_metadata(output, sha):
    lines = output.splitlines()
    if not lines or not re.search(r": go1\.25\.6$", lines[0]):
        raise ValueError("artifact toolchain must be pinned go1.25.6")
    settings = {}
    for line in lines:
        match = re.match(r"\s*build\s+([^=]+)=(.*)$", line)
        if match:
            settings[match[1]] = match[2]
    if settings.get("GOOS") != "linux" or settings.get("GOARCH") != "amd64":
        raise ValueError("artifact build metadata must be linux/amd64")
    if settings.get("CGO_ENABLED") != "1" or settings.get("-trimpath") != "true":
        raise ValueError("artifact must use release CGO_ENABLED=1 and -trimpath")
    if settings.get("vcs") != "git" or settings.get("vcs.revision") != sha:
        raise ValueError("artifact must record the current git revision")
    if settings.get("vcs.modified") != "false":
        raise ValueError("artifact must record a clean git working tree (vcs.modified=false)")
    # Go 1.25.6 omits -ldflags from trimpath build metadata. Do not infer the
    # embedded application Version/GitCommit from absent linker flags.
    return {"toolchain": "go1.25.6", "cgo_enabled": True, "trimpath": True,
            "vcs": "git", "vcs_revision": settings["vcs.revision"],
            "vcs_modified": settings["vcs.modified"] == "true"}



def validate_image(info, target, owner):
    if info.get("Os") != "linux" or info.get("Architecture") != "amd64":
        raise ValueError("image architecture mismatch")
    if not re.fullmatch(r"sha256:[0-9a-f]{64}", info.get("Id", "")):
        raise ValueError("missing immutable image ID")
    config = info.get("Config", {})
    if config.get("Entrypoint") != ENTRYPOINTS[target] or config.get("Cmd"):
        raise ValueError("image entrypoint/Cmd differs from intended release image")
    if config.get("Labels", {}).get(LABEL) != owner:
        raise ValueError("image ownership label mismatch")
    return info["Id"]


def validate_case(kind, code, output, state, version, sha):
    if state.get("Status") != "exited" or state.get("OOMKilled") or state.get("Error"):
        raise ValueError("container did not cleanly reach an application exit")
    if state.get("ExitCode") != code:
        raise ValueError("container exit status mismatch")
    if re.search(r"error while loading shared libraries|GLIBC_[\d.]+.*not found|exec format error|exec /.*no such file", output, re.I):
        raise ValueError("loader failure is never application evidence")
    if kind == "missing":
        if code != 1 or ERROR_MARKER not in output:
            raise ValueError("missing precise application configuration failure")
        if "failed to load plugin config file" in output:
            raise ValueError("packaged plugin configuration failed to load")
    elif kind == "help":
        if code != 0 or "Usage of /arcentra" not in output or "-conf" not in output:
            raise ValueError("original entrypoint help failed")
    elif kind == "cli-help":
        if code != 0 or "Usage:" not in output or "arcentra-cli" not in output:
            raise ValueError("packaged CLI help failed")
    elif kind == "cli-version":
        if code != 0:
            raise ValueError("packaged CLI version failed")
        info = json.loads(output)
        if (info.get("version"), info.get("gitCommit"), info.get("platform")) != (version, sha, "linux/amd64"):
            raise ValueError("packaged CLI version/commit/platform mismatch")
    else:
        raise ValueError("unknown application case")


def run_bounded(args, *, cwd, env, text, stdout, stderr, timeout):
    """Own and reap the entire Docker client/buildx process group on interruption."""
    process = subprocess.Popen(args, cwd=cwd, env=env, text=text, stdout=stdout,
                               stderr=stderr, start_new_session=True)
    try:
        code = process.wait(timeout=timeout)
    except BaseException:
        try:
            os.killpg(process.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        process.wait(timeout=10)
        raise
    return subprocess.CompletedProcess(args, code, None)


class SmokeInterrupted(BaseException):
    """Stop admission immediately; only the outer cleanup handler catches this."""


class Smoke:
    def __init__(self, evidence, version, env):
        self.evidence, self.version, self.sha = evidence, version, env["GITHUB_SHA"]
        self.owner = "arc-install-" + secrets.token_hex(8)
        self.env = {k: env[k] for k in ("PATH", "HOME") if k in env}
        self.containers, self.images = [], []
        self.active_build = False
        self.deadline = time.monotonic() + 300
        self.serial = len(list(evidence.glob("*-command.json")))
        self.evidence_data = {"images": [], "cases": [], "diagnostics": []}
        self.env["DOCKER_BUILDKIT"] = "1"
        self.run_id, self.attempt = env["GITHUB_RUN_ID"], env["GITHUB_RUN_ATTEMPT"]

    def save_resources(self):
        record = {"owner": self.owner, "run_id": self.run_id, "attempt": self.attempt, "sha": self.sha,
                  "containers": self.containers, "images": self.images, "active_build": self.active_build}
        path = self.evidence / "resources.json"
        temporary = self.evidence / "resources.tmp"
        temporary.write_text(json.dumps(record, indent=2) + "\n")
        temporary.replace(path)

    def restore_resources(self):
        record = json.loads((self.evidence / "resources.json").read_text())
        if (record["run_id"], record["attempt"], record["sha"]) != (self.run_id, self.attempt, self.sha):
            raise ValueError("cleanup manifest belongs to another CI run")
        owner = record["owner"]
        if not re.fullmatch(r"arc-install-[0-9a-f]{16}", owner):
            raise ValueError("invalid cleanup owner")
        for kind, separator in (("containers", "-"), ("images", ":")):
            names = record[kind]
            if not isinstance(names, list) or len(names) > 16 or any(
                    not re.fullmatch(re.escape(owner + separator) + r"[a-z-]+", n) for n in names):
                raise ValueError("invalid cleanup resource names")
        self.owner, self.containers, self.images = owner, record["containers"], record["images"]
        self.active_build = bool(record.get("active_build"))

    def save_evidence(self):
        (self.evidence / "evidence.json").write_text(json.dumps(self.evidence_data, indent=2) + "\n")

    def command(self, args, timeout=60, check=True):
        if time.monotonic() >= self.deadline:
            raise RuntimeError("global smoke deadline exceeded")
        self.serial += 1
        record = {"argv": args}
        is_build = args[:4] == ["docker", "--host", "unix:///var/run/docker.sock", "build"]
        if is_build:
            self.active_build = True
            self.save_resources()
        try:
            with tempfile.TemporaryFile() as output:
                result = run_bounded(args, cwd=ROOT, env=self.env, text=True,
                                        stdout=output, stderr=subprocess.STDOUT,
                                        timeout=min(timeout, self.deadline - time.monotonic()))
                if result.stdout is None:
                    output.seek(0)
                    raw = output.read(4 * 1024 * 1024 + 1)
                    if len(raw) > 4 * 1024 * 1024:
                        raise RuntimeError("command output exceeds 4 MiB evidence bound")
                    result.stdout = raw.decode(errors="replace")
            if is_build:
                self.active_build = False
                self.save_resources()
            record.update(returncode=result.returncode, output=result.stdout[:16384], output_truncated=len(result.stdout) > 16384)
        except subprocess.TimeoutExpired as exc:
            record.update(timeout=True, output=(exc.stdout or b"").decode(errors="replace") if isinstance(exc.stdout, bytes) else exc.stdout)
            raise
        finally:
            (self.evidence / f"{self.serial:03d}-command.json").write_text(json.dumps(record, indent=2) + "\n")
        if check and result.returncode:
            raise RuntimeError(f"command failed ({result.returncode}): {' '.join(args[:3])}; see evidence")
        return result

    def docker(self, *args, **kwargs):
        return self.command(["docker", "--host", "unix:///var/run/docker.sock", *args], **kwargs)

    def container(self, image_id, suffix, args, entrypoint=None):
        name = f"{self.owner}-{suffix}"
        self.containers.append(name)
        self.save_resources()
        command = ["create", "--name", name, "--label", f"{LABEL}={self.owner}",
                   "--network", "none", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--memory", "512m", "--cpus", "1", "--pids-limit", "128"]
        if entrypoint:
            command += ["--entrypoint", entrypoint]
        self.docker(*command, image_id, *args)
        result = self.docker("start", "--attach", name, timeout=30, check=False)
        info = json.loads(self.docker("container", "inspect", name).stdout)[0]
        return result, info["State"]

    def run(self, context):
        failures = []
        for target in ENTRYPOINTS:
            try:
                tag = f"{self.owner}:{target}"
                self.images.append(tag)
                self.save_resources()
                self.docker("build", "--platform", "linux/amd64", "--build-arg", "TARGETARCH=amd64",
                            "--target", target, "--label", f"{LABEL}={self.owner}",
                            "--tag", tag, "--file", str(context / "Dockerfile.ci"), str(context), timeout=180)
                info = json.loads(self.docker("image", "inspect", tag).stdout)[0]
                image_id = validate_image(info, target, self.owner)
                self.evidence_data["images"].append({"target": target, "id": image_id, "os": info["Os"],
                                                    "architecture": info["Architecture"], "entrypoint": info["Config"]["Entrypoint"]})
                self.save_evidence()
                # Diagnostics never substitute for execution of the actual ENTRYPOINT.
                binary = "/" + target
                diagnostic = f"cat /etc/os-release; getconf GNU_LIBC_VERSION; ldd {binary}"
                if target == "arcentra":
                    diagnostic += "; ldd /usr/local/bin/arcentra-cli"
                diagnostic_result, diagnostic_state = self.container(image_id, target + "-diagnostic", ["-c", diagnostic], "/bin/sh")
                self.evidence_data["diagnostics"].append({"target": target, "exit_code": diagnostic_result.returncode,
                    "runtime": [line[:300] for line in diagnostic_result.stdout.splitlines()
                                if re.search(r"GLIBC_|glibc|ld-linux|libc\.so|libm\.so|not found|PRETTY_NAME", line)][:40]})
                self.save_evidence()
                cases = [("help", ["-h"], None), ("missing", ["-conf", MISSING], None)]
                if target == "arcentra":
                    cases += [("cli-help", ["--help"], "/usr/local/bin/arcentra-cli"),
                              ("cli-version", ["version"], "/usr/local/bin/arcentra-cli")]
                for kind, args, entrypoint in cases:
                    try:
                        result, state = self.container(image_id, target + "-" + kind, args, entrypoint)
                        self.evidence_data["cases"].append({"target": target, "case": kind, "exit_code": result.returncode,
                            "state": state.get("Status"), "oom_killed": bool(state.get("OOMKilled")),
                            "application_config_failure": ERROR_MARKER in result.stdout,
                            "loader_errors": re.findall(r"GLIBC_[0-9.]+[^\n]{0,120}|error while loading shared libraries[^\n]{0,120}", result.stdout)[:8]})
                        self.save_evidence()
                        validate_case(kind, result.returncode, result.stdout, state, self.version, self.sha)
                        self.evidence_data["cases"][-1]["passed"] = True
                        if kind == "cli-version":
                            self.evidence_data["cases"][-1]["version"] = {k: json.loads(result.stdout).get(k) for k in ("version", "gitCommit", "platform")}
                        self.save_evidence()
                    except (ValueError, RuntimeError, subprocess.TimeoutExpired) as exc:
                        failures.append(f"{target}/{kind}: {exc}")
            except (ValueError, RuntimeError, subprocess.TimeoutExpired) as exc:
                failures.append(f"{target}: {exc}")
                if self.active_build:
                    break  # A cancelled daemon solve must not be hidden by a later build.
        return failures

    def cleanup(self):
        failures = []
        if self.active_build:
            failures.append("build cancellation uncertain: Docker client process group reaped, but daemon solve termination is unverified; disposable runner destruction required")
        for kind, names in (("container", self.containers), ("image", self.images)):
            for name in reversed(names):
                try:
                    result = self.docker(kind, "inspect", name, check=False)
                    if result.returncode:
                        if "No such" in result.stdout:
                            continue
                        raise RuntimeError("cannot establish resource absence")
                    info = json.loads(result.stdout)[0]
                    if info.get("Config", {}).get("Labels", {}).get(LABEL) != self.owner:
                        raise RuntimeError("refusing cleanup of resource without own label")
                    self.docker(kind, "rm", "--force", name)
                    result = self.docker(kind, "inspect", name, check=False)
                    if result.returncode == 0 or "No such" not in result.stdout:
                        raise RuntimeError("resource absence not verified")
                except Exception as exc:
                    failures.append(f"cleanup {name}: {exc}")
        return failures


def inspect_elf(smoke, source):
    """Collect program headers and version requirements without the symbol table."""
    headers = smoke.command(["readelf", "--program-headers", str(source)]).stdout
    private = smoke.command(["objdump", "-p", str(source)]).stdout
    needed, versions = set(), set()
    section = None
    for line in private.splitlines():
        if line == "Dynamic Section:":
            section = "dynamic"
        elif line == "Version References:":
            section = "versions"
        elif line and not line[0].isspace():
            section = None
        elif section == "dynamic":
            match = re.fullmatch(r"\s+NEEDED\s+([A-Za-z0-9_.+-]{1,200})\s*", line)
            if match:
                needed.add(match[1])
        elif section == "versions":
            match = re.fullmatch(r"\s+0x[0-9a-fA-F]+\s+0x[0-9a-fA-F]+\s+[0-9]+\s+(GLIBC_[0-9.]+)\s*", line)
            if match:
                versions.add(match[1])
    return {"needed_libraries": sorted(needed), "required_glibc_versions": sorted(versions),
            "elf_interpreter": re.findall(r"Requesting program interpreter: ([^\]]{1,200})", headers)}


def prepare(smoke, context):
    if smoke.command(["git", "rev-parse", "HEAD"]).stdout.strip() != smoke.sha:
        raise ValueError("checkout differs from current CI SHA")
    dist = ROOT / "dist/linux/amd64"
    if dist.resolve() != dist or not dist.is_dir():
        raise ValueError("requires current checkout dist/linux/amd64 without symlinks")
    destination = context / "dist/linux/amd64"
    destination.mkdir(parents=True)
    metadata = {}
    for name in BINARIES:
        source = dist / name
        if source.is_symlink() or not source.is_file() or not os.access(source, os.X_OK):
            raise ValueError(f"missing regular executable artifact: {name}")
        raw = source.read_bytes()
        if raw[:6] != b"\x7fELF\x02\x01" or raw[18:20] != b"\x3e\x00":
            raise ValueError(f"artifact is not Linux x86-64 ELF: {name}")
        build = smoke.command(["go", "version", "-m", str(source)]).stdout
        build_facts = validate_build_metadata(build, smoke.sha)
        elf_facts = inspect_elf(smoke, source)
        metadata[name] = {"sha256": hashlib.sha256(raw).hexdigest(), "expected_version": smoke.version,
                          "application_runtime_version_verified": False, "platform": "linux/amd64", "go_build_metadata_checked": True, **build_facts,
                          **elf_facts}
        shutil.copy2(source, destination / name)
    (destination / "conf.d").mkdir()
    for source in sorted((ROOT / "conf.d").glob("*.toml")):
        supplied = dist / "conf.d" / source.name
        if supplied.is_symlink() or supplied.resolve() != supplied or supplied.read_bytes() != source.read_bytes():
            raise ValueError("dist configuration must exactly match checked-in default TOML")
        shutil.copy2(supplied, destination / "conf.d" / source.name)
    for required in ("config.toml", "agent.toml", "plugins.toml"):
        if not (destination / "conf.d" / required).is_file():
            raise ValueError("required packaged default configuration missing")
    shutil.copy2(ROOT / "Dockerfile.ci", context / "Dockerfile.ci")
    (smoke.evidence / "artifacts.json").write_text(json.dumps(metadata, indent=2) + "\n")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--evidence-dir", type=Path, required=True)
    parser.add_argument("--expected-version", default="cleanup")
    parser.add_argument("--cleanup-only", action="store_true")
    args = parser.parse_args()
    guard(os.environ)
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9.+-]{0,79}", args.expected_version):
        parser.error("invalid expected version")
    if args.cleanup_only and not (args.evidence_dir / "resources.json").exists():
        print("No owned resources manifest; nothing to clean")
        return 0
    if not args.cleanup_only and args.expected_version == "cleanup":
        parser.error("--expected-version is required for smoke execution")
    args.evidence_dir.mkdir(parents=True, exist_ok=args.cleanup_only)
    smoke = Smoke(args.evidence_dir.resolve(), args.expected_version, os.environ)
    failures = []
    def interrupted(signum, _frame):
        raise SmokeInterrupted(f"interrupted by signal {signum}")
    previous = signal.signal(signal.SIGTERM, interrupted)
    try:
        with tempfile.TemporaryDirectory(prefix=smoke.owner + "-") as temporary:
            # Isolate Docker auth/config; no registry login or inherited credentials.
            config = Path(temporary) / "docker-config"
            config.mkdir()
            smoke.env["DOCKER_CONFIG"] = str(config)
            context = Path(temporary) / "context"
            context.mkdir()
            try:
                if args.cleanup_only:
                    smoke.restore_resources()
                else:
                    prepare(smoke, context)
                    failures.extend(smoke.run(context))
            except (Exception, KeyboardInterrupt, SmokeInterrupted) as exc:
                failures.append(str(exc) or type(exc).__name__)
            finally:
                # Cleanup gets its own bounded budget, even after global timeout.
                smoke.deadline = time.monotonic() + 60
                failures.extend(smoke.cleanup())
    finally:
        signal.signal(signal.SIGTERM, previous)
    summary = {"scope": "loader-cli-entrypoint-config-failure-only", "healthy_service_tested": False,
               "commit": smoke.sha, "version": smoke.version, "passed": not failures, "failures": failures}
    (smoke.evidence / ("cleanup-summary.json" if args.cleanup_only else "summary.json")).write_text(json.dumps(summary, indent=2) + "\n")
    print(json.dumps(summary))
    return int(bool(failures))


if __name__ == "__main__":
    raise SystemExit(main())
