# GitOps Drift Detector

Detects when your Kubernetes cluster no longer matches your Git manifests — and optionally fixes it automatically.

---

## Quick Start

```bash
# 1. Build
go mod tidy && go build -o drift-detector ./cmd

# 2. Report drift (read-only, never changes anything)
./drift-detector check --manifests ./example/manifests

# 3. Watch continuously + open dashboard at http://localhost:7070
./drift-detector run --manifests ./example/manifests --dry-run --port 7070 --interval 30

# 4. Auto-fix drift
./drift-detector run --manifests ./example/manifests --dry-run=false --remediate
```

> See **RUNBOOK.md** for the full step-by-step walkthrough with a local cluster.

---

## What It Does

```
Every N seconds:
  1. Read YAML files from your manifests folder  →  desired state (Git = truth)
  2. Fetch the same resources from the cluster   →  live state
  3. Compare field by field
  4. Print a drift report
  5. If --remediate is on → re-apply the Git version to fix it
```

---

## Commands

| Command | What it does |
|---|---|
| `check` | Run once, print report, exit |
| `run` | Loop forever, check every N seconds |
| `run --dry-run` | Loop forever, never write to cluster |
| `run --remediate` | Loop forever, auto-fix drift when found |
| `run --port 7070` | Also serve live dashboard at that port |

**All flags:**

```bash
./drift-detector run \
  --manifests ./example/manifests \   # path to your YAML files
  --kubeconfig ~/.kube/config \       # optional, auto-detected by default
  --interval 60 \                     # seconds between checks (default: 60)
  --dry-run \                         # read-only mode (default: true)
  --remediate \                       # auto-fix drift (requires --dry-run=false)
  --port 7070                         # dashboard port (0 = disabled)
```

---

## What Drift Looks Like

```
=== DRIFT REPORT ===
[DRY RUN — no changes applied]
Total: 2  |  Drifted: 2  |  Missing: 0  |  Clean: 0

[DRIFTED]  Deployment default/demo-app
           ~ spec.template.spec.containers[app].image
             desired : "nginx:1.25-alpine"
             actual  : "nginx:1.99-alpine"

[DRIFTED]  ConfigMap default/demo-config
           ~ data.LOG_LEVEL
             desired : "info"
             actual  : "debug"
```

JSON output also available at `/api/report` when dashboard is running.

---

## Excluding Intentional Drift

Some differences are on purpose — like an auto-scaler changing replica count. Tell the tool to ignore them.

**Ignore a field everywhere** (global config):
```yaml
# example/config.yaml
globalIgnoreFields:
  - spec.replicas
```

**Ignore a field on one resource** (annotation in your Git YAML):
```yaml
metadata:
  annotations:
    drift-detector/ignore-fields: "spec.replicas"   # ignore this field
    drift-detector/ignore: "true"                    # skip entire resource
    drift-detector/remediate: "false"                # detect but never auto-fix
```

---

## How Auto-Fix Decides What to Touch

Three gates — all must pass before anything is written to the cluster:

```
1. --dry-run=false?                    if no  → never fix
2. --remediate flag set?               if no  → never fix
3. Resource has remediate: false?      if yes → never fix
                                       all pass → fix it
```

**Remediation is opt-in.** The default behaviour can never accidentally modify your cluster.

---

## How Drift Is Detected

**The problem:** You write a 10-line YAML. Kubernetes stores it with 40 fields (adds `resourceVersion`, `uid`, `status`, defaults...). A naive diff would always show fake drift.

**The solution — two steps:**

1. Strip Kubernetes-managed fields from the live copy before comparing:

| Stripped field | Why |
|---|---|
| `metadata.resourceVersion` | Changes on every save |
| `metadata.uid` | Assigned by Kubernetes, not you |
| `metadata.managedFields` | Internal bookkeeping |
| `metadata.generation` | Increments automatically |
| `status` | Written by controllers, not humans |

2. Walk only what's in your Git YAML — fields Kubernetes injected but you never wrote are invisible to the diff automatically.

**Named list matching:** Containers and env vars are matched by `name`, not by position — reordering never triggers false drift.

---

## Alert vs Auto-Fix

| Drift | Action | Why |
|---|---|---|
| Image tag changed | Auto-fix | Nobody should `kubectl set image` in prod |
| ConfigMap value changed | Auto-fix | Known correct value in Git |
| Resource deleted | Auto-fix | Re-creating from Git is always safe |
| Replica count changed | Alert only | HPA may have done it intentionally |
| Resource limits changed | Alert only | Platform team may have patched it |
| RBAC rules changed | Alert only | Security — always needs human review |
| `remediate: false` annotation | Alert only | Operator said hands off |

---

## Architecture

```
┌─────────────────────────────────────────────────────┐
│                  Reconciler (loop)                  │
│  ┌──────────┐    ┌──────────┐    ┌──────────────┐  │
│  │  Loader  │    │  Differ  │    │   Cluster    │  │
│  │ reads    │───▶│ compares │◀───│ fetches live │  │
│  │ manifests│    │ desired  │    │ state from   │  │
│  └──────────┘    └────┬─────┘    │ k8s API      │  │
│                       │          └──────────────┘  │
│                  ┌────▼─────┐    ┌──────────────┐  │
│                  │  Report  │    │  Remediator  │  │
│                  │ text/JSON│    │ server-side  │  │
│                  └──────────┘    │ apply        │  │
│                                  └──────────────┘  │
└─────────────────────────────────────────────────────┘
```

| Package | Does |
|---|---|
| `pkg/loader` | Reads `.yaml` files from a directory |
| `pkg/cluster` | Talks to the Kubernetes API |
| `pkg/differ` | Compares desired vs live, returns drifted fields |
| `pkg/report` | Formats the drift report as text or JSON |
| `pkg/reconciler` | The control loop — ties everything together |
| `pkg/server` | Serves the live dashboard |
| `cmd` | CLI with `run` and `check` subcommands |

---

## Running Tests

**Unit tests** (no cluster needed):
```bash
go test ./test/... -v
```

**End-to-end test** (needs Docker running):
```bash
bash test/e2e.sh
```

---

## In Scope / Out of Scope

| Supported | Not supported |
|---|---|
| Deployments, StatefulSets, DaemonSets | CRDs |
| ConfigMaps, Secrets | Helm-managed resources |
| Services, Ingresses | Multi-cluster |
| RBAC (ServiceAccount, ClusterRole) | Drift history / audit log |
| Dry-run + remediation | Cluster-scoped resources (Nodes, PVs) |

This is **not** a replacement for ArgoCD or Flux — it's a focused auditing tool for teams that want lightweight drift visibility.

---

## Design Decisions

### 1. What is drift?

Drift = any field in your Git manifest that differs from the cluster. We only check fields **you wrote** — never fields Kubernetes added automatically.

### 2. Exclusion mechanism

Two layers: global config file (applies everywhere) and per-resource annotation (applies to one resource). Limits: no wildcards, typos silently do nothing, no conditional exclusions.

### 3. Alert vs auto-remediate

Remediation is opt-in globally, opt-out per resource. Default is never fix. Three gates must all pass before anything is written to the cluster.

### 4. Kubernetes mutations

Strip server-managed fields from a copy of live state, then walk only what's in your Git YAML. Kubernetes-injected defaults are invisible automatically. Named-list matching prevents false positives from reordering.

We chose this "walk desired" approach over 3-way merge (kubectl's approach) because our tool is read-only by default and should work on resources it didn't create — it has no prior state to compare against.

### 5. Production deployment

Single-replica Deployment + git-sync sidecar + read-only ServiceAccount. Every failure skips rather than crashes. Stale manifests = skip the tick, never remediate with bad data. Immutable field conflicts = log and move on, never crash-loop.

---

## Assumptions

- Manifests are static YAML files — not Helm templates
- Single cluster only
- Each run is point-in-time — no drift history stored
- git-sync handles Git authentication separately
