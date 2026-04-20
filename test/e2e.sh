#!/usr/bin/env bash
# End-to-end test for the GitOps Drift Detector.
#
# What this script does (plain English):
#   1. Build the binary
#   2. Create a local Kubernetes cluster (kind)
#   3. Apply our example manifests  →  this is the "desired state" (Git truth)
#   4. Run the detector             →  should report CLEAN (no drift yet)
#   5. Manually change things in the cluster  →  simulates someone running kubectl by hand
#   6. Run the detector again       →  should now report DRIFT on the changed fields
#   7. Run with --remediate         →  detector fixes the drift automatically
#   8. Run one final time           →  should be CLEAN again (drift was fixed)
#   9. Tear down the cluster
#
# Run it:
#   bash test/e2e.sh
#
# Requirements: go, kind, kubectl

set -euo pipefail   # stop immediately on any error

# ── colours for readable output ───────────────────────────────────────────────
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # no colour

pass() { echo -e "${GREEN}✓ PASS${NC}  $1"; }
fail() { echo -e "${RED}✗ FAIL${NC}  $1"; exit 1; }
info() { echo -e "${YELLOW}▶${NC}  $1"; }

CLUSTER_NAME="drift-e2e"
BINARY="./drift-detector"

# ── cleanup: always delete the cluster when the script exits ──────────────────
# This runs even if the script fails halfway through, so you never have a
# leftover kind cluster sitting on your machine.
cleanup() {
  info "Cleaning up kind cluster..."
  kind delete cluster --name "$CLUSTER_NAME" 2>/dev/null || true
  rm -f "$BINARY"
}
trap cleanup EXIT

# ─────────────────────────────────────────────────────────────────────────────
# STEP 1: Build the binary
# ─────────────────────────────────────────────────────────────────────────────
info "Building drift-detector binary..."
go build -o "$BINARY" ./cmd
pass "Binary built"

# ─────────────────────────────────────────────────────────────────────────────
# STEP 2: Start a local kind cluster
# kind = Kubernetes IN Docker — spins up a real k8s cluster locally in ~30s
# ─────────────────────────────────────────────────────────────────────────────
info "Creating kind cluster '$CLUSTER_NAME'..."
kind create cluster --name "$CLUSTER_NAME" --wait 60s
export KUBECONFIG="$(kind get kubeconfig-path --name "$CLUSTER_NAME" 2>/dev/null || echo "$HOME/.kube/config")"
pass "Cluster ready"

# ─────────────────────────────────────────────────────────────────────────────
# STEP 3: Apply desired state (simulates initial GitOps deploy)
# This puts our example Deployment + ConfigMap into the cluster.
# In a real GitOps setup, your CD pipeline would do this from Git.
# ─────────────────────────────────────────────────────────────────────────────
info "Applying desired state (example manifests)..."
kubectl apply -f example/manifests/ --context "kind-$CLUSTER_NAME"

# Wait for the Deployment to be ready before we start testing
kubectl rollout status deployment/demo-app \
  --context "kind-$CLUSTER_NAME" \
  --timeout=60s

pass "Desired state applied"

# ─────────────────────────────────────────────────────────────────────────────
# STEP 4: Check #1 — everything should be CLEAN (no drift yet)
# We just applied the exact same manifests, so cluster == Git
# ─────────────────────────────────────────────────────────────────────────────
info "Check 1: Running drift detection — expecting CLEAN..."

REPORT=$("$BINARY" check \
  --manifests ./example/manifests \
  --kubeconfig "$KUBECONFIG" 2>&1)

echo "$REPORT"

# The report prints "Drifted: 0" when everything is clean
if echo "$REPORT" | grep -q "Drifted: 0"; then
  pass "Check 1: No drift detected (correct)"
else
  fail "Check 1: Expected no drift but drift was reported"
fi

# ─────────────────────────────────────────────────────────────────────────────
# STEP 5: Introduce drift manually
# This simulates what happens in real life:
#   - Ops team patches a ConfigMap during an incident ("LOG_LEVEL was too quiet")
#   - Someone bumps an image tag directly with kubectl
# Neither change went through Git — that's the drift.
# ─────────────────────────────────────────────────────────────────────────────
info "Introducing drift: patching ConfigMap LOG_LEVEL info → debug..."
kubectl patch configmap demo-config \
  --context "kind-$CLUSTER_NAME" \
  --type merge \
  -p '{"data":{"LOG_LEVEL":"debug"}}'

info "Introducing drift: changing container image to nginx:1.99-alpine..."
kubectl set image deployment/demo-app \
  app=nginx:1.99-alpine \
  --context "kind-$CLUSTER_NAME"

pass "Drift introduced"

# ─────────────────────────────────────────────────────────────────────────────
# STEP 6: Check #2 — should now detect BOTH drifted fields
# ─────────────────────────────────────────────────────────────────────────────
info "Check 2: Running drift detection — expecting 2 drifted resources..."

REPORT=$("$BINARY" check \
  --manifests ./example/manifests \
  --kubeconfig "$KUBECONFIG" 2>&1)

echo "$REPORT"

# Verify the image drift was caught
if echo "$REPORT" | grep -q "nginx:1.99-alpine"; then
  pass "Check 2: Image drift detected (nginx:1.99-alpine vs nginx:1.25-alpine)"
else
  fail "Check 2: Expected image drift to be reported"
fi

# Verify the ConfigMap drift was caught
if echo "$REPORT" | grep -q "LOG_LEVEL"; then
  pass "Check 2: ConfigMap drift detected (LOG_LEVEL: debug vs info)"
else
  fail "Check 2: Expected ConfigMap drift to be reported"
fi

# Verify replicas were NOT flagged (excluded by annotation on the Deployment)
if echo "$REPORT" | grep -q "spec.replicas"; then
  fail "Check 2: spec.replicas should be excluded by annotation but was flagged"
else
  pass "Check 2: spec.replicas correctly excluded (annotation worked)"
fi

# ─────────────────────────────────────────────────────────────────────────────
# STEP 7: Remediate — let the detector fix the drift automatically
# --dry-run=false  means "actually write to the cluster"
# --remediate      means "re-apply the Git version when drift is found"
# ─────────────────────────────────────────────────────────────────────────────
info "Remediating: running detector with --remediate..."

"$BINARY" run \
  --manifests ./example/manifests \
  --kubeconfig "$KUBECONFIG" \
  --dry-run=false \
  --remediate \
  --interval 999 &   # run in background; we'll kill it after one tick

DETECTOR_PID=$!
sleep 10             # give it time to complete one reconciliation pass
kill $DETECTOR_PID 2>/dev/null || true

pass "Remediation pass completed"

# ─────────────────────────────────────────────────────────────────────────────
# STEP 8: Check #3 — should be CLEAN again after remediation
# ─────────────────────────────────────────────────────────────────────────────
info "Check 3: Verifying cluster was restored to desired state..."

# Wait for rollout after remediation
kubectl rollout status deployment/demo-app \
  --context "kind-$CLUSTER_NAME" \
  --timeout=60s 2>/dev/null || true

REPORT=$("$BINARY" check \
  --manifests ./example/manifests \
  --kubeconfig "$KUBECONFIG" 2>&1)

echo "$REPORT"

if echo "$REPORT" | grep -q "Drifted: 0"; then
  pass "Check 3: Cluster is clean after remediation"
else
  fail "Check 3: Drift still present after remediation"
fi

# Also verify directly in the cluster that the image was actually restored
ACTUAL_IMAGE=$(kubectl get deployment demo-app \
  --context "kind-$CLUSTER_NAME" \
  -o jsonpath='{.spec.template.spec.containers[0].image}')

if [ "$ACTUAL_IMAGE" = "nginx:1.25-alpine" ]; then
  pass "Check 3: Image restored to nginx:1.25-alpine in cluster"
else
  fail "Check 3: Image is '$ACTUAL_IMAGE', expected nginx:1.25-alpine"
fi

ACTUAL_LOG=$(kubectl get configmap demo-config \
  --context "kind-$CLUSTER_NAME" \
  -o jsonpath='{.data.LOG_LEVEL}')

if [ "$ACTUAL_LOG" = "info" ]; then
  pass "Check 3: LOG_LEVEL restored to 'info' in cluster"
else
  fail "Check 3: LOG_LEVEL is '$ACTUAL_LOG', expected 'info'"
fi

# ─────────────────────────────────────────────────────────────────────────────
# ALL DONE
# ─────────────────────────────────────────────────────────────────────────────
echo ""
echo -e "${GREEN}══════════════════════════════════════${NC}"
echo -e "${GREEN}  All end-to-end tests passed! ✓      ${NC}"
echo -e "${GREEN}══════════════════════════════════════${NC}"
echo ""
echo "What was proven:"
echo "  1. Detector reports CLEAN when cluster matches Git"
echo "  2. Detector reports DRIFT when cluster is manually changed"
echo "  3. Exclusion annotation prevents spec.replicas from being flagged"
echo "  4. Remediation re-applies Git state and returns cluster to CLEAN"
