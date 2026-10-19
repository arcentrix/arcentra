#!/usr/bin/env bash
# Authorized only on an ephemeral GitHub-hosted Ubuntu 24.04 VM.
set -Eeuo pipefail
umask 077
cd "$(dirname "$0")/../.."
if [[ ${GITHUB_ACTIONS:-} != true || ${RUNNER_ENVIRONMENT:-} != github-hosted || ${RUNNER_OS:-} != Linux || $(uname -m) != x86_64 || $(id -u) == 0 || $(id -g) == 0 ]]; then
  echo 'This harness requires an unprivileged GitHub-hosted Linux x86_64 runner.' >&2
  exit 1
fi
# shellcheck source=/dev/null
source /etc/os-release
[[ $ID == ubuntu && $VERSION_ID == 24.04 ]] || { echo 'Ubuntu 24.04 is required.' >&2; exit 1; }
[[ ${GITHUB_RUN_ID:-} =~ ^[0-9]+$ && ${GITHUB_RUN_ATTEMPT:-} =~ ^[0-9]+$ ]] || exit 1
[[ -f /sys/fs/cgroup/cgroup.controllers ]] || { echo 'cgroup v2 is required; host settings will not be changed.' >&2; exit 1; }
# shellcheck source=containerd-pins.env
source scripts/acceptance/containerd-pins.env
EVIDENCE="$PWD/.acceptance/containerd-evidence"
mkdir -p "$EVIDENCE"
# Official pinned browser tools are prepared by this workflow before any daemon.
# No alternate script, inherited token, browser endpoint or remote URL is accepted.
BROWSER_CWD="$PWD/.acceptance/frontend"
BROWSER_NODE=$(command -v node)
[[ $BROWSER_NODE == /* && -x $BROWSER_NODE && ${PLAYWRIGHT_BROWSERS_PATH:-} == /* ]] || exit 1
[[ -f $BROWSER_CWD/tests/actual-copilot-execution-acceptance.mjs && -f $BROWSER_CWD/node_modules/playwright/package.json ]] || { echo 'Mandatory pinned Copilot browser fixture is missing.' >&2; exit 1; }
BASE=$(mktemp -d "/tmp/arc-cd.${GITHUB_RUN_ID}.${GITHUB_RUN_ATTEMPT}.XXXXXX")
SUPERVISOR=''
finish() {
  local status=$? clean=0
  trap - EXIT INT TERM
  if [[ -n $SUPERVISOR ]]; then
    touch "$BASE/control/stop"
    # Root supervisor has its own bounded lifetime and signal handler as well.
    if ! wait "$SUPERVISOR"; then status=1; fi
    if [[ -f $BASE/control/cleanup.json ]]; then
      cp "$BASE/control/cleanup.json" "$EVIDENCE/cleanup.json"
      if python3 - "$EVIDENCE/cleanup.json" <<'PY'
import json, sys
if json.load(open(sys.argv[1])).get("passed") is not True:
    raise SystemExit("Runtime cleanup was not clean; acceptance failed.")
PY
      then clean=1; else status=1; fi
    else
      echo 'Missing mandatory runtime cleanup receipt.' >&2
      status=1
    fi
  fi
  # Root-owned data is removed by the supervisor only after mount checks.
  # Never sudo rm a caller-supplied path or remove another run's directory.
  if [[ ( -z $SUPERVISOR || $clean == 1 ) && -d $BASE && $(stat -c %u "$BASE") == "$(id -u)" ]]; then
    rm -rf -- "$BASE" || status=1
  fi
  exit "$status"
}
trap finish EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
mkdir -p "$BASE/bin" "$BASE/control" "$BASE/work/tmp" "$BASE/work/home" "$BASE/work/journal" "$BASE/work/workspaces" "$BASE/work/job" "$BASE/work/fifo"
python3 scripts/acceptance/verify_containerd_image.py "$ARCENTRA_SANDBOX_TEST_IMAGE" > "$EVIDENCE/image.json"
curl -q --fail --silent --show-error --location --proto '=https' --tlsv1.2 --connect-timeout 10 --max-time 120 --retry 2 \
  "https://github.com/containerd/containerd/releases/download/v${CONTAINERD_VERSION}/containerd-${CONTAINERD_VERSION}-linux-amd64.tar.gz" -o "$BASE/containerd.tar.gz"
printf '%s  %s\n' "$CONTAINERD_SHA256" "$BASE/containerd.tar.gz" | sha256sum --check --strict
# Extract only the three verified regular upstream executable entries.
tar --extract --gzip --no-same-owner --file "$BASE/containerd.tar.gz" --directory "$BASE" \
  bin/containerd bin/ctr bin/containerd-shim-runc-v2
curl -q --fail --silent --show-error --location --proto '=https' --tlsv1.2 --connect-timeout 10 --max-time 120 --retry 2 \
  "https://github.com/opencontainers/runc/releases/download/v${RUNC_VERSION}/runc.amd64" -o "$BASE/bin/runc"
printf '%s  %s\n' "$RUNC_SHA256" "$BASE/bin/runc" | sha256sum --check --strict
chmod 0755 "$BASE/bin/"*
{
  "$BASE/bin/containerd" --version
  "$BASE/bin/runc" --version
  printf 'containerdArchiveSHA256=%s\nruncSHA256=%s\n' "$CONTAINERD_SHA256" "$RUNC_SHA256"
} > "$EVIDENCE/versions.txt"
# No inherited GitHub/cloud tokens, proxy variables, SSH agent or user HOME.
sudo -n env -i GITHUB_ACTIONS=true RUNNER_ENVIRONMENT=github-hosted PATH=/usr/bin:/bin \
  /usr/bin/python3 "$PWD/scripts/acceptance/containerd_runtime_guard.py" \
  --base "$BASE" --uid "$(id -u)" --gid "$(id -g)" > "$EVIDENCE/guard.log" 2>&1 &
SUPERVISOR=$!
for attempt in $(seq 1 45); do
  if [[ -f $BASE/control/ready ]]; then break; fi
  kill -0 "$SUPERVISOR" 2>/dev/null || { cat "$EVIDENCE/guard.log"; exit 1; }
  [[ $attempt != 45 ]] || { echo 'Isolated runtime readiness timed out.' >&2; exit 1; }
  sleep 1
done
TEST_ENV=(env -i
  "PATH=$BASE/bin:/usr/bin:/bin" "HOME=$BASE/work/home" "TMPDIR=$BASE/work/tmp"
  "USER=$(id -un)" GITHUB_ACTIONS=true RUNNER_ENVIRONMENT=github-hosted
  ARCENTRA_SANDBOX_ACCEPTANCE=1 ARCENTRA_SANDBOX_SESSION_ACCEPTANCE=1 ARCENTRA_DURABLE_SESSION_ACCEPTANCE=1
  ARCENTRA_CONTAINERD_ACCEPTANCE=1 ARCENTRA_REQUIRE_CONTAINERD_ACCEPTANCE=1
  "ARCENTRA_CONTAINERD_SOCKET=$BASE/run/containerd.sock" "ARCENTRA_SANDBOX_TEST_IMAGE=$ARCENTRA_SANDBOX_TEST_IMAGE"
  "ARCENTRA_CONTAINERD_RUNC_ROOT=$BASE/runc" "ARCENTRA_CONTAINERD_FIFO_DIR=$BASE/work/fifo"
  "ARCENTRA_DURABLE_SESSION_NAMESPACE=arcentra-durable-acceptance-${GITHUB_RUN_ID}-${GITHUB_RUN_ATTEMPT}"
  "ARCENTRA_DURABLE_SESSION_JOURNAL_ROOT=$BASE/work/journal" "ARCENTRA_DURABLE_SESSION_WORKSPACE_ROOT=$BASE/work/workspaces"
  "ARCENTRA_TEST_CONTAINERD_NAMESPACE=arcentra-containerjob-acceptance-${GITHUB_RUN_ID}-${GITHUB_RUN_ATTEMPT}"
  "ARCENTRA_TEST_CONTAINERD_ROOT=$BASE/work/job"
  ARCENTRA_REAL_STACK=1 ARCENTRA_REQUIRE_REAL_STACK=1
  ARCENTRA_COPILOT_EXECUTION_BROWSER=1 ARCENTRA_REQUIRE_COPILOT_EXECUTION_BROWSER=1
  "ARCENTRA_COPILOT_BROWSER_CWD=$BROWSER_CWD" "ARCENTRA_COPILOT_BROWSER_NODE=$BROWSER_NODE"
  "PLAYWRIGHT_BROWSERS_PATH=$PLAYWRIGHT_BROWSERS_PATH" "ARCENTRA_COPILOT_BROWSER_EVIDENCE=$EVIDENCE/copilot-browser"
  'ARCENTRA_TEST_MYSQL_DSN=root:ci-only-mysql-password@tcp(127.0.0.1:13306)/arcentra_acceptance_containerd?charset=utf8mb4&parseTime=True&loc=UTC'
  ARCENTRA_TEST_KAFKA_BROKERS=127.0.0.1:19092 ARCENTRA_TEST_REDIS_ADDRESS=127.0.0.1:16379
  ARCENTRA_TEST_S3_ENDPOINT=http://127.0.0.1:19000 ARCENTRA_TEST_S3_ACCESS_KEY=ci-only-s3-user ARCENTRA_TEST_S3_SECRET_KEY=ci-only-s3-password
)
run_test() {
  local binary=$1 expression=$2 duration=$3 name=$4 repetitions=$5
  shift 5
  "${TEST_ENV[@]}" "$PWD/.acceptance/containerd-bin/$binary.test" -test.v "-test.count=$repetitions" \
    "-test.run=$expression" "-test.timeout=$duration" 2>&1 | tee "$EVIDENCE/$name.log"
  # Check every repetition, including cancellation/deadline subtests; never accept Skip.
  python3 scripts/acceptance/verify_containerd_test_log.py "$EVIDENCE/$name.log" "$repetitions" "$@"
}
run_test sandbox '^TestContainerdAcceptance$' 3m sandbox 1 TestContainerdAcceptance
# Three repetitions and live-step restart share the original five-minute budget.
SESSION_LANE_STARTED=$SECONDS
run_test sandboxexec '^Test(SessionContainerdAcceptance|DurableControllerContainerdAcceptance|DurableControllerActiveRecoveryContainerdAcceptance)$' 5m sessions 3 \
  TestSessionContainerdAcceptance TestDurableControllerContainerdAcceptance \
  TestDurableControllerContainerdAcceptance/cancel TestDurableControllerContainerdAcceptance/timeout \
  TestDurableControllerActiveRecoveryContainerdAcceptance
# No additional daemon lifetime: new crash phases consume only the remainder.
SESSION_LANE_REMAINING=$((300 - (SECONDS - SESSION_LANE_STARTED)))
(( SESSION_LANE_REMAINING > 0 )) || { echo 'Session acceptance budget exhausted.' >&2; exit 1; }
(( SESSION_LANE_REMAINING <= 120 )) || SESSION_LANE_REMAINING=120
run_test sandboxexec '^TestLiveStepJournalRestartContainerdAcceptance$' "${SESSION_LANE_REMAINING}s" step-restart 1 \
  TestLiveStepJournalRestartContainerdAcceptance \
  TestLiveStepJournalRestartContainerdAcceptance/intent TestLiveStepJournalRestartContainerdAcceptance/admission \
  TestLiveStepJournalRestartContainerdAcceptance/consumed TestLiveStepJournalRestartContainerdAcceptance/failed \
  TestLiveStepJournalRestartContainerdAcceptance/successor-intent TestLiveStepJournalRestartContainerdAcceptance/successor-admission \
  TestLiveStepJournalRestartContainerdAcceptance/successor-consumed
run_test taskqueue '^TestRealContainerJobAcceptance$' 8m full-job 1 TestRealContainerJobAcceptance \
  TestRealContainerJobAcceptance/durable-retry-approval-approved-finite-plan/personal-inbox-request-delivery \
  TestRealContainerJobAcceptance/durable-retry-approval-approved-finite-plan/actual-browser-permanent-confirmation \
  TestRealContainerJobAcceptance/failed-step-approval/durable-retry-approval-approved-finite-plan/personal-inbox-request-delivery \
  TestRealContainerJobAcceptance/failed-step/same-live-session-immutable-inputs-ack-loss \
  TestRealContainerJobAcceptance/failed-step/earlier-background-refused \
  TestRealContainerJobAcceptance/failed-step/adopted-zombie-refused \
  TestRealContainerJobAcceptance/failed-step/cancel-before-successor \
  TestRealContainerJobAcceptance/failed-step/uncertain-resume-cleans \
  TestRealContainerJobAcceptance/failed-step-approval/durable-retry-approval-approved-finite-plan \
  TestRealContainerJobAcceptance/failed-step-approval/durable-retry-approval-cancel-pending \
  TestRealContainerJobAcceptance/failed-step-approval/durable-retry-approval-revoke-before-successor \
  TestRealContainerJobAcceptance/failed-step-approval/durable-retry-approval-revoke-after-CAS-before-RUNNING \
  TestRealContainerJobAcceptance/failed-step-approval/durable-retry-approval-lost-CAS-ACK-then-revoke
