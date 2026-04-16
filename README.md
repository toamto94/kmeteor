# kmeteor

A Kubernetes operator that injects chaos into your cluster on a statistically realistic schedule — driven by a **Poisson process**.

Instead of firing incidents at fixed intervals ("every hour, delete a pod"), kmeteor fires them at _random_ times whose average rate you control. This mirrors how real-world failures actually occur and prevents your on-call team from developing a false rhythm around predictable drills.

---

## Table of Contents

- [How it works](#how-it-works)
  - [The Poisson process](#the-poisson-process)
  - [Sampling inter-arrival times](#sampling-inter-arrival-times)
  - [From math to CronJobs](#from-math-to-cronjobs)
  - [Worked example](#worked-example)
- [Architecture](#architecture)
- [Chaos actions and RBAC](#chaos-actions-and-rbac)
  - [Blast radius by design](#blast-radius-by-design)
  - [Built-in profiles](#built-in-profiles)
  - [Weighted selection](#weighted-selection)
- [Installation](#installation)
- [Multi-tenancy](#multi-tenancy)
- [Configuration reference](#configuration-reference)
- [CR reference](#cr-reference)

---

## How it works

### The Poisson process

A **Poisson process** is a mathematical model for events that occur independently, at a constant average rate, with no memory of when the last event happened. Classic examples include radioactive decay, phone calls arriving at a switchboard, and — in the SRE world — hardware failures and user-reported incidents.

Formally, a Poisson process with rate **λ** (lambda) has two equivalent characterisations:

1. The number of events **N** in any interval of length **T** follows a Poisson distribution:

   ```
   P(N = k) = (λT)^k · e^(−λT) / k!
   ```

   The expected number of events is **E[N] = λT**.

2. The time between consecutive events — the **inter-arrival time** — follows an **exponential distribution** with rate **λ**:

   ```
   P(T_inter > t) = e^(−λt)
   ```

   The expected gap between events is **E[T_inter] = 1/λ**.

These two characterisations are mathematically equivalent and give the process its two defining properties:

- **Memorylessness** — knowing that no event happened in the last hour tells you nothing about when the next one will arrive. The process has no "it's overdue" concept.
- **Independent increments** — what happens in one time window is independent of every other window.

Both properties make the Poisson process the right model for chaos engineering: incidents in real infrastructure are not correlated with each other, and they don't become "more likely" just because it has been a while.

### Sampling inter-arrival times

To generate a sequence of Poisson event times, kmeteor uses the **inverse transform method** on the exponential distribution.

Given a uniform random variable `U ~ Uniform(0, 1)`, the transformation:

```
T = −ln(U) / λ
```

produces a sample from `Exponential(λ)`. This is mathematically exact — no approximation is involved.

**Derivation.** The CDF of `Exponential(λ)` is `F(t) = 1 − e^(−λt)`. Setting `F(t) = U` and solving for `t`:

```
U = 1 − e^(−λt)
e^(−λt) = 1 − U
−λt = ln(1 − U)
t = −ln(1 − U) / λ
```

Since `1 − U` is also uniform on `(0, 1)` when `U` is, this simplifies to `t = −ln(U) / λ`, which is exactly what the controller computes:

```go
// internal/controller/kmeteor_controller.go
interArrival := -math.Log(rand.Float64()) / rate
```

### From math to CronJobs

The controller runs on a repeating reconcile loop with period equal to `spec.interval`. Each reconcile:

1. **Cleans up** all CronJobs created in the previous interval (identified by the label `kmeteor.io/owner`).
2. **Samples** a full sequence of event times for the _next_ interval using the inter-arrival method above, starting from `now`.
3. **Creates** one Kubernetes CronJob per sampled event time. Each CronJob has a one-shot cron schedule (`M H D Month *`) that fires exactly once at the target minute.
4. **Persists** the schedule summary in `status.scheduledJobs` and requeues after `spec.interval`.

The rate parameter used internally is derived from the user-facing parameters:

```
rate (events/second) = λ / interval_in_seconds
```

For example, `lambda: 3` and `interval: 1h` gives `rate = 3 / 3600 ≈ 0.000833 events/second`, meaning an average gap of about 20 minutes between events.

Events scheduled within the first minute of the interval are skipped, because Kubernetes needs at least ~1 minute of lead time to register and evaluate a cron schedule before its first fire time.

### Worked example

```
lambda:   3
interval: 1h
```

The controller samples inter-arrival times one by one, accumulating elapsed time until it exceeds 3600 seconds:

```
draw U1 = 0.72  →  T1 = −ln(0.72) / 0.000833 ≈  393 s  (6m 33s)  → skip (< 1m lead time? no — schedule it)
draw U2 = 0.14  →  T2 = −ln(0.14) / 0.000833 ≈ 2395 s  (39m 55s from T1)
draw U3 = 0.61  →  T3 = −ln(0.61) / 0.000833 ≈  587 s  (9m 47s from T2)
draw U4 = 0.88  →  T4 = −ln(0.88) / 0.000833 ≈  162 s  — cumulative now > 3600 s → stop
```

Result: 3 CronJobs are created, firing at times `now + 6m33s`, `now + 46m28s`, and `now + 56m15s`. Next reconcile is in 1 hour, at which point those CronJobs are cleaned up and a new sample is drawn.

The expected number of events per interval is always **λ** — in this case, 3 — but any given interval may produce 0, 1, 2, 4, 5, … events. This variability is intentional and is what makes the chaos schedule feel realistic.

---

## Architecture

```
┌─────────────────────────────────────────────────────────┐
│  kmeteor-system namespace                               │
│                                                         │
│  ┌────────────────────────────────────────────────────┐ │
│  │  kmeteor-controller-manager (Deployment)           │ │
│  │                                                    │ │
│  │  Reconcile loop (period = spec.interval):          │ │
│  │    1. Delete previous-interval CronJobs            │ │
│  │    2. Sample Poisson event times                   │ │
│  │    3. Create one CronJob per event time            │ │
│  │    4. Update status, requeue                       │ │
│  └────────────────────────────────────────────────────┘ │
│         │ watches & manages                             │
│         ▼                                               │
│  KMeteor CR (namespaced)                                │
│    spec.lambda    = 3                                   │
│    spec.interval  = 1h                                  │
│    spec.actions   = [...]                               │
└─────────────────────────────────────────────────────────┘
         │ creates CronJobs in CR's namespace
         ▼
┌──────────────────────────────────────────────────────────┐
│  Target namespace (e.g. default)                         │
│                                                          │
│  CronJob: example-1712345678-0  schedule: "33 14 7 4 *" │
│  CronJob: example-1712345678-1  schedule: "28 15 7 4 *" │
│  CronJob: example-1712345678-2  schedule: "15 16 7 4 *" │
│         │                                                │
│         │ at fire time → spawns Job → Pod               │
│         │               ServiceAccount: kmeteor-job     │
│         ▼                                                │
│  chaos script (kubectl delete pod / scale deploy / ...)  │
└──────────────────────────────────────────────────────────┘
```

**Two ServiceAccounts** are involved:

| ServiceAccount | Used by | RBAC |
|---|---|---|
| `kmeteor-controller-manager` | The operator pod | Fixed: manage `KMeteor` CRs and `CronJobs` |
| `kmeteor-job` | Every CronJob pod | Configurable via `jobRbac` in `values.yaml` |

The separation is intentional — the operator needs cluster-level CRD access, while the job SA is scoped strictly to what the chaos scripts actually need.

---

## Chaos actions and RBAC

### Blast radius by design

Each `ChaosAction` runs as a pod under the `kmeteor-job` ServiceAccount. That SA's RBAC rules are set in `values.yaml` under `jobRbac.rules`. The chaos script can only do what those rules permit — nothing more.

This means:

- You control the blast radius by configuring RBAC, not by trusting the script.
- If you give the job SA no permissions, the scripts will fail gracefully and the cluster is unaffected.
- Progressively widening `jobRbac.rules` lets you test whether your hardening is effective at each privilege level.

### Built-in profiles

Five profiles are documented in [helm/kmeteor/values.yaml](helm/kmeteor/values.yaml). Each profile lists the required `jobRbac.rules` and the matching `spec.actions` entry to put in the KMeteor CR.

| Profile | Scope | What it tests |
|---|---|---|
| **A — pod-chaos** | namespace | Workload self-healing (PodDisruptionBudgets, restartPolicy, liveness probes) |
| **B — deployment-chaos** | namespace | HPA behaviour, alert coverage for zero-replica deployments |
| **C — configmap-chaos** | namespace | Config-reload handling, watching for stale ConfigMap data |
| **D — networkpolicy-chaos** | namespace | Whether missing NetworkPolicies open unintended traffic paths |
| **E — node-chaos** | cluster | Pod eviction, rescheduling, node affinity, PodDisruptionBudgets |

### Weighted selection

When a CronJob fires, the controller has already selected one action from `spec.actions` at schedule time, using **weighted random selection**:

```
P(action_i is chosen) = weight_i / Σ weight_j
```

For example:

```yaml
actions:
  - name: delete-random-pod
    weight: 2        # chosen 2/3 of the time
    ...
  - name: scale-deployment-zero
    weight: 1        # chosen 1/3 of the time
    ...
```

Weights default to 1 when omitted. This lets you reflect the relative frequency of different failure modes in your environment.

---

## Installation

**Prerequisites:** `docker`, `helm`, `kubectl`, a running cluster (kind / minikube / real).

### 1. Build the operator image

```bash
docker build -t kmeteor:latest .
```

If using kind, load the image into the cluster:

```bash
kind load docker-image kmeteor:latest
```

If using minikube:

```bash
eval $(minikube docker-env)
docker build -t kmeteor:latest .
```

### 2. Install the Helm chart

```bash
# Install with default values (no chaos actions, no-op CronJobs)
helm install kmeteor ./helm/kmeteor

# Install with pod-chaos profile (namespace-scoped)
helm install kmeteor ./helm/kmeteor \
  --set jobRbac.clusterScoped=false \
  --set-json 'jobRbac.rules=[{"apiGroups":[""],"resources":["pods"],"verbs":["get","list","delete"]}]'
```

### 3. Apply a KMeteor CR

```bash
kubectl apply -f config/samples/kmeteor_v1alpha1_kmeteor.yaml
```

### 4. Watch the schedule

```bash
# See the CR status
kubectl get kmeteor example -o yaml

# Watch CronJobs appear and disappear
kubectl get cronjobs -w

# Follow operator logs
kubectl logs -n kmeteor-system deployment/kmeteor-controller-manager -f
```

---

## Multi-tenancy

In a multi-tenant cluster, tenants have no access to the operator namespace but can deploy KMeteor CRs into their own namespaces and define their own chaos scope.

### Security model

The key guarantee is that a tenant's CronJob pods run under a **ServiceAccount the tenant owns**, in their own namespace. The operator never creates that SA — it just references it. This means:

- A tenant cannot exceed the permissions of their own SA — privilege escalation is impossible by construction.
- Tenant A's blast radius is completely independent of Tenant B's.
- The cluster admin controls which tenants get access to the KMeteor API at all, via RoleBindings.

### Setup (cluster admin)

**1. Enable the tenant ClusterRole in the Helm chart:**

```bash
helm upgrade kmeteor ./helm/kmeteor --set tenantAccess.enabled=true
```

This creates a `ClusterRole` named `kmeteor-tenant` that grants `create/get/list/watch/update/patch/delete` on `kmeteors.kmeteor.io`.

**2. Bind it per tenant namespace — never cluster-wide:**

```bash
kubectl create rolebinding kmeteor-tenant \
  --clusterrole=kmeteor-tenant \
  --serviceaccount=<tenant-ns>:<tenant-user> \
  --namespace=<tenant-ns>
```

Using a `RoleBinding` (not `ClusterRoleBinding`) means the tenant can only manage KMeteor CRs in their own namespace.

### Setup (tenant)

**1. Create a ServiceAccount and Role for the chaos job pods:**

```yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: my-chaos-sa
  namespace: <tenant-ns>
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: my-chaos-role
  namespace: <tenant-ns>
rules:
  - apiGroups: [""]
    resources: ["pods"]
    verbs: ["get", "list", "delete"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: my-chaos-rolebinding
  namespace: <tenant-ns>
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: my-chaos-role
subjects:
  - kind: ServiceAccount
    name: my-chaos-sa
    namespace: <tenant-ns>
```

**2. Deploy a KMeteor CR referencing that SA:**

```yaml
apiVersion: kmeteor.io/v1alpha1
kind: KMeteor
metadata:
  name: my-chaos
  namespace: <tenant-ns>
spec:
  lambda: 2
  interval: 1h
  jobServiceAccountName: my-chaos-sa   # <── tenant's own SA
  actions:
    - name: delete-random-pod
      image: bitnami/kubectl:latest
      weight: 1
      command:
        - sh
        - -c
        - |
          kubectl get pods --field-selector=status.phase=Running -o name \
            | shuf -n1 | xargs -r kubectl delete --grace-period=0 --force
```

The operator schedules CronJobs in `<tenant-ns>` using `my-chaos-sa`. The blast radius is exactly what `my-chaos-role` allows — nothing else.

### What the operator's own SA does NOT need

The operator already has `create/list/watch/delete` on `CronJobs` cluster-wide (required to manage CRs in any namespace). It does **not** need any new permissions to support multi-tenancy — it simply passes the tenant-provided SA name through to the CronJob spec.

---

## Configuration reference

All values in `helm/kmeteor/values.yaml`:

| Key | Default | Description |
|---|---|---|
| `image.repository` | `kmeteor` | Operator image name |
| `image.tag` | `latest` | Operator image tag |
| `image.pullPolicy` | `Never` | Image pull policy |
| `operatorServiceAccount.name` | `kmeteor-controller-manager` | SA for the operator pod |
| `jobServiceAccount.name` | `kmeteor-job` | SA assigned to every CronJob pod |
| `jobRbac.clusterScoped` | `false` | `true` → ClusterRole, `false` → Role |
| `jobRbac.rules` | `[]` | RBAC policy rules for the job SA |
| `tenantAccess.enabled` | `false` | Create the `kmeteor-tenant` ClusterRole for per-namespace RoleBindings |
| `leaderElection` | `false` | Enable leader election (set `true` for multi-replica) |
| `resources.limits.cpu` | `500m` | CPU limit for the operator pod |
| `resources.limits.memory` | `128Mi` | Memory limit for the operator pod |
| `namespace` | `kmeteor-system` | Namespace for the operator deployment |

---

## CR reference

```yaml
apiVersion: kmeteor.io/v1alpha1
kind: KMeteor
metadata:
  name: example
  namespace: default       # CronJobs are created in this namespace
spec:
  lambda: 3                # Average number of events per interval
  interval: 1h             # Length of each scheduling window (Go duration string)
  jobServiceAccountName: my-chaos-sa  # Optional. Overrides the operator default SA.
                                      # Must exist in the CR's namespace.
                                      # Use this in multi-tenant clusters.
  actions:                 # Optional. If empty, CronJobs print "fired" (no-op).
    - name: my-action      # Human-readable label shown in logs
      image: bitnami/kubectl:latest
      command: ["sh", "-c", "echo hello"]
      args: []             # Optional extra arguments
      weight: 1            # Relative selection probability (default: 1, min: 1)
```

**`spec.lambda`** — the Poisson rate parameter. Interpreted as "expected number of events per `spec.interval`". Must be > 0.001.

**`spec.interval`** — the scheduling window. The controller re-samples at the start of every window. Accepts any Go duration string: `10m`, `1h`, `24h`, etc.

**`spec.actions`** — the pool of chaos workloads. One is selected per CronJob event. Selection is weighted by `weight`. The selected action's `image`, `command`, and `args` become the container spec for the CronJob pod.
