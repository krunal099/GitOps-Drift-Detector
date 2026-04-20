# RUNBOOK — GitOps Drift Detector

> Run this against a real local Kubernetes cluster in ~15 minutes.
> Every step shows the exact command and exactly what you should see.

---

## Before Anything — Install These 3 Tools

> Skip any tool you already have installed.

```bash
brew install go
```
```bash
brew install kind
```
```bash
brew install kubectl
```

> **Docker must be running.** Open Docker Desktop before continuing.

Quick check — run this to confirm all three work:

```bash
go version && kind version && kubectl version --client
```

---

## The 5 Commands That Do Everything

> Want to skip the walkthrough? Run these in order and you're done.

```bash
# 1. Build
go mod tidy && go build -o drift-detector ./cmd

# 2. Start cluster
kind create cluster --name drift-demo --wait 60s

# 3. Deploy example app
kubectl apply -f example/manifests/ --context kind-drift-demo

# 4. Run the detector with dashboard
./drift-detector run --manifests ./example/manifests --dry-run --port 7070 --interval 30

# 5. Open in browser
open http://localhost:7070
```

> Full walkthrough with explanations below.

---

## Step 1 — Build the Binary

```bash
go mod tidy && go build -o drift-detector ./cmd
```

**You should see:** No output = success. Then confirm:

```bash
./drift-detector --help
```

---

## Step 2 — Start a Local Kubernetes Cluster

> This creates a real Kubernetes cluster inside Docker on your laptop. Takes ~30 seconds.

```bash
kind create cluster --name drift-demo --wait 60s
```

**You should see:**
```
✓ Ensuring node image
✓ Starting control-plane
Set kubectl context to "kind-drift-demo"
```

---

## Step 3 — Deploy the Example App

> This puts a Deployment and ConfigMap into the cluster — your "desired state from Git."

```bash
kubectl apply -f example/manifests/ --context kind-drift-demo
```

**You should see:**
```
configmap/demo-config created
deployment.apps/demo-app created
```

Wait for it to be ready:

```bash
kubectl rollout status deployment/demo-app --context kind-drift-demo
```

**You should see:**
```
deployment "demo-app" successfully rolled out
```

---

## Step 4 — Check for Drift (Should Be Clean)

```bash
./drift-detector check --manifests ./example/manifests --kubeconfig ~/.kube/config
```

**You should see:**
```
=== DRIFT REPORT ===
[DRY RUN — no changes applied]
Total: 2  |  Drifted: 0  |  Missing: 0  |  Clean: 2  |  Ignored: 0
```

> Everything matches Git. No drift yet.

---

## Step 5 — Introduce Drift (Simulate a Real Incident)

> Simulate someone changing the cluster directly without updating Git.

**Change 1** — patch a config value:
```bash
kubectl patch configmap demo-config \
  --context kind-drift-demo \
  --type merge \
  -p '{"data":{"LOG_LEVEL":"debug"}}'
```

**Change 2** — bump the image tag:
```bash
kubectl set image deployment/demo-app \
  app=nginx:1.99-alpine \
  --context kind-drift-demo
```

---

## Step 6 — Detect the Drift

```bash
./drift-detector check --manifests ./example/manifests --kubeconfig ~/.kube/config
```

**You should see:**
```
Total: 2  |  Drifted: 2  |  Missing: 0  |  Clean: 0  |  Ignored: 0

[DRIFTED]  ConfigMap default/demo-config
           ~ data.LOG_LEVEL
             desired : "info"
             actual  : "debug"

[DRIFTED]  Deployment default/demo-app
           ~ spec.template.spec.containers[app].image
             desired : "nginx:1.25-alpine"
             actual  : "nginx:1.99-alpine"
```

> Notice `spec.replicas` is NOT reported — it's excluded by annotation on the Deployment (HPA exclusion working correctly).

---

## Step 7 — Fix the Drift Automatically

```bash
./drift-detector run \
  --manifests ./example/manifests \
  --kubeconfig ~/.kube/config \
  --dry-run=false \
  --remediate \
  --interval 30
```

> Wait one tick (up to 30 seconds), then press `Ctrl-C`.

Verify it was fixed:

```bash
kubectl get configmap demo-config --context kind-drift-demo \
  -o jsonpath='{.data.LOG_LEVEL}'
```
**Should print:** `info`

```bash
kubectl get deployment demo-app --context kind-drift-demo \
  -o jsonpath='{.spec.template.spec.containers[0].image}'
```
**Should print:** `nginx:1.25-alpine`

---

## Step 8 — Open the Live Dashboard

```bash
./drift-detector run \
  --manifests ./example/manifests \
  --kubeconfig ~/.kube/config \
  --dry-run \
  --port 7070 \
  --interval 30
```

Then open in browser:

```bash
open http://localhost:7070
```

> Dashboard auto-refreshes every 30 seconds. Introduce drift in another terminal — watch the cards turn red.

---

## Step 9 — Run the Automated End-to-End Test

> One command does everything above automatically.

```bash
bash test/e2e.sh
```

**You should see at the end:**
```
✓ PASS  Check 1: No drift detected
✓ PASS  Check 2: Image drift detected
✓ PASS  Check 2: ConfigMap drift detected
✓ PASS  Check 2: spec.replicas correctly excluded
✓ PASS  Check 3: Cluster clean after remediation

══════════════════════════════════════
  All end-to-end tests passed! ✓
══════════════════════════════════════
```

---

## Step 10 — Run Unit Tests

```bash
go test ./test/... -v
```

**You should see 8 tests pass:**
```
--- PASS: TestDiff_Clean
--- PASS: TestDiff_ImageDrift
--- PASS: TestDiff_GlobalIgnore_Replicas
--- PASS: TestDiff_AnnotationIgnore
--- PASS: TestDiff_MissingField
--- PASS: TestDiff_LabelDrift
--- PASS: TestDiff_ToolingAnnotationsIgnored
--- PASS: TestDiff_ServerFieldsIgnored
PASS
```

---

## Step 11 — Clean Up

```bash
kind delete cluster --name drift-demo
```

---

## Troubleshooting

| Problem | Fix |
|---|---|
| `Cannot connect to Docker` | Open Docker Desktop and wait for it to fully start |
| `kind: command not found` | Run `brew install kind`, then open a new terminal |
| kind cluster hangs > 2 min | Docker needs more memory — Docker Desktop → Settings → Resources → 4GB+ |
| `no context kind-drift-demo` | Cluster not created yet — go back to Step 2 |
| `connection refused` on detector | Run `kubectl config use-context kind-drift-demo` |
| Port 7070 already in use | Try `--port 7171` instead |

---

## What's Deliberately Not Supported

- Custom Resource Definitions (CRDs)
- Helm-managed resources
- Multi-cluster
- Drift history over time (each run is point-in-time)
- Private Git repos (git-sync handles auth separately in production)
