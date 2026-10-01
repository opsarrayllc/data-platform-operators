#!/usr/bin/env bash
# Recreate the local kind cluster, apply the local samples, and run the operators in the foreground.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
CLUSTER="${KIND_DEV_CLUSTER:-data-platform-dev}"
KUBECONFIG_FILE="${KIND_KUBECONFIG:-${ROOT}/bin/kubeconfig-${CLUSTER}}"

export PLATFORM_UP=1
make -C "${ROOT}" kind-up

export KUBECONFIG="${KUBECONFIG_FILE}"

echo "==> Checking operators"
make -C "${ROOT}" manifests generate fmt vet
make -C "${ROOT}/data-analytics-operator" manifests generate fmt vet
make -C "${ROOT}/data-orchestration-operator" manifests generate fmt vet

echo "==> Applying local DataLake, Analytics, and Orchestration samples"
kubectl apply -f "${ROOT}/config/samples/dataplatform_v1alpha1_local.yaml"
kubectl apply -f "${ROOT}/data-analytics-operator/config/samples/dataplatform_v1alpha1_local.yaml"
kubectl apply -f "${ROOT}/data-orchestration-operator/config/samples/dataplatform_v1alpha1_local.yaml"

echo "==> Starting operators (Ctrl+C stops them and leaves the cluster running)"
exec python3 -u - "${ROOT}" "${ROOT}/data-analytics-operator" "${ROOT}/data-orchestration-operator" <<'PY'
import os
import signal
import subprocess
import sys
import threading
import time

if len(sys.argv) != 4:
    sys.stderr.write("usage: platform-up <lake-dir> <analytics-dir> <orchestration-dir>\n")
    sys.exit(2)

operators = [("lake", sys.argv[1]), ("analytics", sys.argv[2]), ("orchestration", sys.argv[3])]
procs = []
threads = []
lock = threading.Lock()
stopping = False
interrupted = False


def pump(tag, proc):
    stream = proc.stdout
    if stream is None:
        return
    for line in stream:
        with lock:
            sys.stdout.write("[%s] %s" % (tag, line))
            sys.stdout.flush()


def stop(signum=None, _frame=None):
    global stopping, interrupted
    if signum == signal.SIGINT:
        interrupted = True
    if stopping:
        return
    stopping = True
    for proc in procs:
        try:
            os.killpg(proc.pid, signal.SIGTERM)
        except (ProcessLookupError, PermissionError):
            pass


signal.signal(signal.SIGINT, stop)
signal.signal(signal.SIGTERM, stop)

for tag, cwd in operators:
    proc = subprocess.Popen(
        ["go", "run", "./cmd"],
        cwd=cwd,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        start_new_session=True,
        text=True,
        bufsize=1,
    )
    procs.append(proc)
    thread = threading.Thread(target=pump, args=(tag, proc))
    thread.start()
    threads.append(thread)

code = 0
while not stopping:
    for proc in procs:
        rc = proc.poll()
        if rc is not None:
            if rc != 0:
                code = rc
            stop()
            break
    else:
        time.sleep(0.2)
        continue
    break

deadline = time.time() + 15
for proc in procs:
    remaining = max(0.1, deadline - time.time())
    try:
        proc.wait(timeout=remaining)
    except subprocess.TimeoutExpired:
        try:
            os.killpg(proc.pid, signal.SIGKILL)
        except (ProcessLookupError, PermissionError):
            pass
        proc.wait()

for thread in threads:
    thread.join(timeout=2)

sys.exit(130 if interrupted else code)
PY
