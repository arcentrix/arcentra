#!/usr/bin/env python3
"""Own one disposable containerd daemon; never operate the system daemon.

Only run via the GitHub-hosted acceptance harness. Importing this module is safe
and its unit tests do not start a daemon or require privilege.
"""
import argparse
import json
import os
from pathlib import Path
import re
import shutil
import secrets
import signal
import stat
import subprocess
import time

BASE_PATTERN = re.compile(r"/tmp/arc-cd\.[0-9]+\.[0-9]+\.[A-Za-z0-9]{6}\Z")
NAMESPACE_PATTERN = re.compile(r"arcentra-(?:acceptance|session-acceptance|durable-acceptance|containerjob-acceptance)-[a-zA-Z0-9-]+\Z")
MAX_LIFETIME_SECONDS = 18 * 60


def validate_base(value):
    base = Path(value)
    if not BASE_PATTERN.fullmatch(value) or str(base.resolve()) != value:
        raise ValueError("refusing non-owned or symlink runtime directory")
    if len(str(base / "s")) > 41:
        raise ValueError("runtime path exceeds Linux shim socket length limit")
    return base


def inspect_fifo_directory(base, uid, gid):
    """Inspect the exact runner-owned FIFO root without following symlinks."""
    fifo = base / "work/fifo"
    if fifo.resolve() != fifo:
        raise RuntimeError("client FIFO directory must not contain symlinks")
    descriptor = os.open(fifo, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        info = os.fstat(descriptor)
        if info.st_uid != uid or info.st_gid != gid or stat.S_IMODE(info.st_mode) != 0o700:
            raise RuntimeError("client FIFO directory must be runner-owned mode 0700")
        return len(os.listdir(descriptor))
    finally:
        os.close(descriptor)


def daemon_config(base, uid, gid):
    """Return a config with no system sockets, CRI, NRI or metrics listener."""
    return f'''version = 4
root = "{base}/root"
state = "{base}/state"
temp = "{base}/daemon-tmp"
disabled_plugins = ["io.containerd.cri.v1.images", "io.containerd.cri.v1.runtime", "io.containerd.nri.v1.nri", "io.containerd.internal.v1.opt", "io.containerd.internal.v1.restart", "io.containerd.server.v1.grpc-tcp", "io.containerd.server.v1.debug", "io.containerd.server.v1.metrics"]
required_plugins = ["io.containerd.snapshotter.v1.overlayfs", "io.containerd.shim.v1.manager"]
[plugins."io.containerd.server.v1.grpc"]
  address = "{base}/run/containerd.sock"
  uid = {uid}
  gid = {gid}
[plugins."io.containerd.server.v1.ttrpc"]
  address = "{base}/run/containerd.ttrpc"
  uid = 0
  gid = 0
[plugins."io.containerd.shim.v1.manager"]
  socket_dir = "{base}/s"
'''


def owned_mounts(base, mountinfo):
    """Match whole path components, including escaped mountinfo paths."""
    result = []
    for line in mountinfo.splitlines():
        fields = line.split()
        if len(fields) < 7:
            continue
        target = re.sub(r"\\([0-7]{3})", lambda m: chr(int(m[1], 8)), fields[4])
        if target == str(base) or target.startswith(str(base) + "/"):
            result.append(target)
    return sorted(set(result), key=lambda x: (x.count("/"), len(x)), reverse=True)


def daemon_diagnostics(path):
    """Bounded warning/error lines only; never include env dumps or URL secrets."""
    if not path.is_file() or path.is_symlink():
        return []
    with path.open("rb") as stream:
        stream.seek(0, os.SEEK_END)
        stream.seek(max(0, stream.tell() - 32768))
        lines = stream.read(32768).decode("utf-8", errors="replace").splitlines()
    output = []
    for line in lines:
        if not re.search(r"level=(?:warn|warning|error|fatal)|panic:|Error:", line):
            continue
        line = re.sub(r"(https?://)[^/@\s]+@", r"\1[REDACTED]@", line)
        line = re.sub(r"(?i)\b(?:bearer|basic)\s+[a-z0-9/+_=.-]+", "[REDACTED_AUTH]", line)
        line = re.sub(r'(?i)((?:token|password|secret|authorization|credential)[\\"\']?[=:]\s*[\\"\']?)[^\s,\\"\']+', r"\1[REDACTED]", line)
        output.append(line[:1200])
    return output[-20:]


def pid_identity(pid):
    try:
        root = Path("/proc") / str(pid)
        # Field 22 after the final ')' is the process's start time.
        fields = (root / "stat").read_text().rsplit(")", 1)[1].split()
        return (os.readlink(root / "exe"), fields[19])
    except (FileNotFoundError, ProcessLookupError):
        return None


def write_receipt(directory_fd, name, value, uid, gid):
    """Atomically replace only a control-directory entry, never follow a link."""
    temporary = ".receipt-" + secrets.token_hex(12)
    fd = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600, dir_fd=directory_fd)
    try:
        os.fchown(fd, uid, gid)
        with os.fdopen(fd, "w", closefd=False) as stream:
            stream.write(value)
            stream.flush()
            os.fsync(fd)
        os.replace(temporary, name, src_dir_fd=directory_fd, dst_dir_fd=directory_fd)
    finally:
        os.close(fd)
        try:
            os.unlink(temporary, dir_fd=directory_fd)
        except FileNotFoundError:
            pass


def stop_pid(pid, identity):
    """Use a pidfd and verify start time/executable to exclude PID reuse."""
    if identity is None or pid_identity(pid) != identity:
        return
    try:
        fd = os.pidfd_open(pid)
    except ProcessLookupError:
        return
    try:
        if pid_identity(pid) != identity:
            return
        signal.pidfd_send_signal(fd, signal.SIGTERM)
        until = time.monotonic() + 3
        while pid_identity(pid) == identity and time.monotonic() < until:
            time.sleep(0.1)
        if pid_identity(pid) == identity:
            signal.pidfd_send_signal(fd, signal.SIGKILL)
            until = time.monotonic() + 5
            while pid_identity(pid) == identity and time.monotonic() < until:
                time.sleep(0.1)
            if pid_identity(pid) == identity:
                raise RuntimeError(f"owned PID {pid} remains after SIGKILL")
    except ProcessLookupError:
        pass
    finally:
        os.close(fd)


class Runtime:
    def __init__(self, base, uid=None, gid=None):
        self.base = base
        self.uid = os.getuid() if uid is None else uid
        self.gid = os.getgid() if gid is None else gid
        self.daemon = None
        self.identity = None
        self.namespaces = set()
        self.env = {"PATH": f"{base}/bin:/usr/bin:/bin", "HOME": str(base / "daemon-tmp"), "TMPDIR": str(base / "daemon-tmp")}

    def ctr(self, *args, namespace=None, check=True):
        command = [str(self.base / "bin/ctr"), "--address", str(self.base / "run/containerd.sock"), "--timeout", "8s"]
        if namespace:
            command.extend(["--namespace", namespace])
        result = subprocess.run(command + list(args), env=self.env, capture_output=True, text=True, timeout=12, check=False)
        if check and result.returncode:
            raise RuntimeError(f"ctr {' '.join(args[:2])} failed: {result.stderr[-500:]}")
        return result.stdout

    def discover(self):
        values = self.ctr("namespaces", "list", "--quiet").splitlines()
        if len(values) > 64 or any(not NAMESPACE_PATTERN.fullmatch(v) for v in values):
            raise RuntimeError("unexpected namespace in dedicated daemon")
        self.namespaces.update(values)
        return values

    def cleanup(self):
        leaks, errors = [], []
        try:
            fifo_entries = inspect_fifo_directory(self.base, self.uid, self.gid)
            if fifo_entries:
                leaks.append({"clientFIFOEntries": fifo_entries})
        except (OSError, RuntimeError) as exc:
            errors.append(f"cannot verify private client FIFO cleanup: {exc}")
        try:
            namespaces = self.discover()
            for ns in namespaces:
                tasks = self.ctr("tasks", "list", "--quiet", namespace=ns).splitlines()
                containers = self.ctr("containers", "list", "--quiet", namespace=ns).splitlines()
                snapshots = self.ctr("snapshots", "list", namespace=ns).splitlines()[1:]
                active = [s for s in snapshots if s.split()[-1].lower() == "active"]
                if tasks or containers or active:
                    leaks.append({"namespace": ns, "tasks": len(tasks), "containers": len(containers), "activeSnapshots": len(active)})
                for task in tasks:
                    self.ctr("tasks", "kill", "--signal", "SIGKILL", "--all", task, namespace=ns, check=False)
                    self.ctr("tasks", "delete", "--force", task, namespace=ns, check=False)
                for container in containers:
                    self.ctr("containers", "delete", container, namespace=ns, check=False)
                if self.ctr("tasks", "list", "--quiet", namespace=ns).strip() or self.ctr("containers", "list", "--quiet", namespace=ns).strip():
                    errors.append(f"runtime resources remain in {ns}")
        except Exception as exc:
            errors.append(str(exc))

        # Inspect before fallback cleanup; a successful rescue is not a test pass.
        mounts = owned_mounts(self.base, Path("/proc/self/mountinfo").read_text())
        if mounts:
            leaks.append({"mounts": len(mounts)})
        for ns in sorted(self.namespaces):
            cgroup = Path("/sys/fs/cgroup") / ns
            if cgroup.exists():
                children = list(cgroup.rglob("*"))
                dirs = [p for p in children if p.is_dir()]
                if dirs:
                    leaks.append({"namespace": ns, "cgroupChildren": len(dirs)})
                # Kill only PIDs in exact owned namespace cgroups, using pidfds.
                # No ancestor cgroup.kill, no recursive wildcard process signal.
                for p in [cgroup, *dirs]:
                    try:
                        values = (p / "cgroup.procs").read_text().split()
                    except FileNotFoundError:
                        continue
                    except OSError as exc:
                        errors.append(f"cannot inspect owned cgroup {p}: {exc}")
                        continue
                    if values:
                        leaks.append({"namespace": ns, "cgroupProcesses": len(values)})
                    for value in values:
                        pid = int(value)
                        try:
                            stop_pid(pid, pid_identity(pid))
                        except RuntimeError as exc:
                            errors.append(str(exc))

        if self.daemon:
            try:
                stop_pid(self.daemon.pid, self.identity)
            except RuntimeError as exc:
                errors.append(str(exc))
            try:
                self.daemon.wait(timeout=5)
            except subprocess.TimeoutExpired:
                errors.append("owned daemon did not exit")
        # A shim is owned only if its executable is this run's exact binary.
        shim_exe = str(self.base / "bin/containerd-shim-runc-v2")
        for item in Path("/proc").iterdir():
            if item.name.isdecimal():
                identity = pid_identity(int(item.name))
                if identity and identity[0] == shim_exe:
                    leaks.append({"orphanShim": int(item.name)})
                    try:
                        stop_pid(int(item.name), identity)
                    except RuntimeError as exc:
                        errors.append(str(exc))
        for target in owned_mounts(self.base, Path("/proc/self/mountinfo").read_text()):
            try:
                result = subprocess.run(["/usr/bin/umount", "--", target], capture_output=True, text=True, timeout=10)
                if result.returncode:
                    errors.append(f"owned mount remains: {target}")
            except (OSError, subprocess.TimeoutExpired) as exc:
                errors.append(f"cannot remove owned mount {target}: {exc}")
        for ns in sorted(self.namespaces):
            cgroup = Path("/sys/fs/cgroup") / ns
            if cgroup.exists():
                for item in sorted([p for p in cgroup.rglob("*") if p.is_dir()], key=lambda p: len(p.parts), reverse=True) + [cgroup]:
                    try:
                        item.rmdir()  # Empty owned groups only; never rm -rf cgroupfs.
                    except OSError as exc:
                        errors.append(f"owned cgroup remains: {item}: {exc}")
        if owned_mounts(self.base, Path("/proc/self/mountinfo").read_text()):
            errors.append("owned mounts remain after cleanup")
        if not leaks and not errors:
            for child in self.base.iterdir():
                if child.name != "control":
                    if child.is_dir() and not child.is_symlink():
                        shutil.rmtree(child)
                    else:
                        child.unlink()
        return {"passed": not leaks and not errors, "leaksBeforeFallback": leaks, "cleanupErrors": errors}


def serve(base, uid, gid):
    if os.geteuid() != 0 or uid <= 0 or gid <= 0:
        raise RuntimeError("root supervisor and non-root test UID:GID are required")
    if os.environ.get("GITHUB_ACTIONS") != "true" or os.environ.get("RUNNER_ENVIRONMENT") != "github-hosted":
        raise RuntimeError("this helper only supports the authorized disposable GitHub VM")
    if not Path("/sys/fs/cgroup/cgroup.controllers").is_file():
        raise RuntimeError("cgroup v2 is required; this helper will not change host settings")
    base = validate_base(str(base))
    if base.stat().st_uid != uid or stat.S_IMODE(base.stat().st_mode) != 0o700:
        raise RuntimeError("fresh private runtime directory must belong to the test user")
    for name in ("bin", "control", "work"):
        path = base / name
        if path.is_symlink() or not path.is_dir() or path.stat().st_uid != uid:
            raise RuntimeError("runtime inputs must be real directories owned by the test user")
    if inspect_fifo_directory(base, uid, gid):
        raise RuntimeError("fresh client FIFO directory must be empty")
    control_fd = os.open(base / "control", os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    os.chown(base, 0, gid)
    base.chmod(0o750)
    os.chown(base / "bin", 0, 0)
    (base / "bin").chmod(0o755)
    for child in (base / "bin").iterdir():
        if not child.is_file() or child.is_symlink() or child.stat().st_nlink != 1:
            raise RuntimeError("runtime binaries must be regular single-link files")
        os.chown(child, 0, 0)
        child.chmod(0o755)
    for name in ("root", "state", "daemon-tmp", "run", "s", "runc"):
        (base / name).mkdir(mode=0o700)
    os.chown(base / "run", 0, gid)
    (base / "run").chmod(0o750)
    config = base / "config.toml"
    config.write_text(daemon_config(base, uid, gid))
    config.chmod(0o600)
    runtime = Runtime(base, uid, gid)
    stop = False

    def request_stop(_signum, _frame):
        nonlocal stop
        stop = True

    signal.signal(signal.SIGTERM, request_stop)
    signal.signal(signal.SIGINT, request_stop)
    report = {"passed": False, "cleanupErrors": ["supervisor did not finish"]}
    failure = None
    try:
        with (base / "daemon.log").open("wb") as log:
            runtime.daemon = subprocess.Popen([str(base / "bin/containerd"), "--config", str(config)], stdin=subprocess.DEVNULL, stdout=log, stderr=log, env=runtime.env, start_new_session=True)
        runtime.identity = pid_identity(runtime.daemon.pid)
        ready_until = time.monotonic() + 30
        while True:
            if runtime.daemon.poll() is not None:
                raise RuntimeError("isolated daemon exited before readiness")
            try:
                runtime.ctr("version")
                break
            except (RuntimeError, subprocess.TimeoutExpired):
                if time.monotonic() >= ready_until:
                    raise RuntimeError("isolated daemon did not become ready")
                time.sleep(0.5)
        write_receipt(control_fd, "ready", "ready\n", uid, gid)
        deadline = time.monotonic() + MAX_LIFETIME_SECONDS
        while not stop and not (base / "control/stop").exists():
            if runtime.daemon.poll() is not None:
                raise RuntimeError("isolated daemon exited during acceptance")
            if time.monotonic() >= deadline:
                raise RuntimeError("isolated runtime exceeded 18-minute lifetime")
            runtime.discover()
            time.sleep(1)
    except Exception as exc:
        failure = str(exc)
    finally:
        diagnostics = daemon_diagnostics(base / "daemon.log")
        try:
            report = runtime.cleanup()
        except Exception as exc:
            report = {"passed": False, "cleanupErrors": [str(exc)]}
            # Even unexpected inventory errors cannot leave the daemon running.
            if runtime.daemon:
                try:
                    stop_pid(runtime.daemon.pid, runtime.identity)
                    runtime.daemon.wait(timeout=5)
                except Exception as stop_error:
                    report["cleanupErrors"].append(str(stop_error))
        if failure:
            report["passed"] = False
            report["supervisorError"] = failure
        if not report["passed"]:
            report["daemonWarningsAndErrors"] = diagnostics
        write_receipt(control_fd, "cleanup.json", json.dumps(report, indent=2) + "\n", uid, gid)
        os.close(control_fd)
        os.chown(base, uid, gid)
        base.chmod(0o700)
        print(json.dumps(report), flush=True)
    return 0 if report["passed"] else 1


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base", required=True)
    parser.add_argument("--uid", type=int, required=True)
    parser.add_argument("--gid", type=int, required=True)
    args = parser.parse_args()
    raise SystemExit(serve(Path(args.base), args.uid, args.gid))
