---
title: Kubernetes
description: Configure and validate the native Kubernetes sandbox backend.
---

Stella does not provide a maintained production deployment chart. The native
Kubernetes sandbox backend below is available for local development and testing.
Production deployment manifests are operator-managed; Stella still supports only
one server replica.

## Native Pod sandbox (local/dev)

Set `STELLA_SANDBOX_BACKEND=kubernetes` to run each sandbox Session in a separate
Linux Pod. This backend requires one Stella replica, the same
namespace and node for Stella and its sandboxes, and a shared `ReadWriteOnce` PVC.
Use `Recreate` for the Stella Deployment. The local fixture lives in
`test/testbed/kubernetes/fixture.yaml`.
Cross-node placement, multiple replicas and automatic failover are not supported.
Kubernetes 1.35 is the tested version; earlier versions have not been validated.
When upgrading an experiment that used `STELLA_KUBERNETES_DEPLOYMENT`, stop its
old sandbox Pods first. Startup cleanup now groups Pods by PVC UID and does not
migrate the former deployment labels.

Stella discovers its Pod name, UID and namespace from the default projected
ServiceAccount token. Keep this token mounted on the server Pod; no Pod-name or
namespace environment variables are needed, even with a custom Pod hostname.
The home PVC is discovered from the direct read-write `/data` mount, without
`subPath` or `subPathExpr`. Set `STELLA_HOME` to `/data` or a directory beneath it.
If multiple containers mount different PVCs at `/data`, startup fails rather
than selecting an unrelated claim.

Optional overrides:

- `STELLA_KUBERNETES_IMAGE`: the tool runtime image for sandbox Pods, separate
  from the Stella server image. Defaults to `ghcr.io/cherryhq/stella-sandbox:<version>`
  for releases, or `stella-sandbox:dev` for development builds. A custom image must
  contain the same builtin bundle as stellad. Pin a digest when deploying.
- `STELLA_SANDBOX_SERVER_URL`: defaults to the server Pod IP and configured HTTP
  listening port (use a fixed, nonzero port). Override with a reachable HTTP(S) URL for a proxy or custom
  routing. The server must listen on the Pod interface, not only loopback.

The server reads its Pod UID and node from Kubernetes and uses the PVC UID to
group sandboxes across server replacements. This limits automatic orphan
cleanup to the same storage, just as Docker scopes cleanup to Stella Home or its
data volume. The owner Pod and boot identity determine which executions are old;
sharing a PVC alone does not make a sandbox orphaned.

Sandbox Pods inherit the server Pod's image pull secrets. RBAC needs namespace
Pod create/get/list/delete/patch, `pods/exec` create and read access to the one
PVC. Create an unprivileged `stella-sandbox` ServiceAccount. Sandbox Pods disable
token mounting and service environment injection, run as UID/GID 1000, drop all
capabilities and use a read-only image filesystem. The server's authorized data
roots must be writable by that UID. Each sandbox requests 100m CPU and 128Mi
memory, with limits of 2 CPU, 2Gi memory and 1Gi ephemeral storage. Configure a
finite kubelet `podPidsLimit` on the nodes; namespace quotas do not limit PIDs. Startup waits
at most 120 seconds by default (`STELLA_KUBERNETES_STARTUP_TIMEOUT`, e.g. `2m`). Apply namespace quotas to limit total consumption.

Only authorized PVC subdirectories are mounted. The image owns builtin tools;
per-principal mise directories stay on the PVC. Agent credentials travel through
exec stdin, not Pod environment variables or command URL arguments. Stella
checks the image bundle revision before accepting a Session.

NetworkPolicy must enforce default-deny for sandbox Pods. Select network modes
with `stella.cherryhq.io/network=allow_all` or `disabled`. Disabled means no
network, including DNS and Stella callbacks; the backend also removes the
callback URL. Allow only DNS, the Stella callback port and approved public
outbound destinations for normal sessions. Block database, Kubernetes API, node
management and metadata endpoints. Verify actual connections on your CNI;
creating a NetworkPolicy object does not prove enforcement. The fixture denies
all IPv6 traffic and restricts IPv4; adapt and re-test for your cluster.

A timeout, broken exec stream or unfinished process Close terminates the whole
Session Pod, including other background processes. Execution is never replayed.
Stella retains an execution-fence finalizer until Kubernetes reports termination.
API errors leave Close retryable and prevent generation replacement. A failed
runner Close keeps its cache slot and blocks that session from rebuilding;
owner deletion also waits for any runner still being constructed. Never force
delete Pods or remove this finalizer to bypass an unavailable node: first prove
execution has stopped. An uncertain Pod creation blocks further creation until a
server restart reconciles owned Pods. Unknown explicit backend names fail startup.
Single-replica upgrades interrupt service; back up the database and PVC before
upgrades because reverting an image does not reverse a migration.

To verify locally:

```bash
SANDBOX_IMAGE=stella-sandbox:kubernetes-test mise run sandbox:docker:build
mise run test:kubernetes -- --context orbstack --suite all
```

`SANDBOX_IMAGE` only names the image built by the local build task; it is not a
server setting. The test fixture selects that tag through
`STELLA_KUBERNETES_IMAGE`. Kubernetes nodes must be able to pull or find it locally.

The task owns a temporary namespace and runs storage/process contracts plus
existing testbed Agent/SSE journeys inside a trusted test Pod. `storage` and
`process` select the narrower contracts. Ordinary `mise run test` does not need
a Kubernetes cluster. The local fixture is test infrastructure; it is not a
production deployment manifest.
