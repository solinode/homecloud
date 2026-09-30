# Design: multi-node clusters

Status: proposal. Tracks [issue #55](https://github.com/solinode/homecloud/issues/55). Written against `main` at the time of the PR; paths below are relative to the repository root. VM-backed instances (`svc/ec2/vm`, branch `ec2-vm-instances`) are not on `main` yet and are mentioned only where they change a decision.

## 1. Where we are

HomeCloud is one process (`homecloud serve`, `cli/internal/server/server.go`) talking to one Docker Engine (`cli/internal/runtime/docker.go`: `type Docker struct{ C *docker.Client }`, built with `docker.NewClientFromEnv()`). Everything a service needs from "the host" is reached through that one object:

- Containers, networks and volumes are created through `runtime.Docker`. Roughly 185 call sites in 20 files under `svc/` and `server/` use it, about 40 of them reach past the wrapper to the raw `env.Docker.C` client.
- State is `store.Store` (`cli/internal/store/store.go`): an in-memory map of JSON documents, flushed whole to `state.json` on every change. Its API is narrow (`Put`, `Get`, `List`, `Delete`, `Update` with optimistic retry, `Retain`).
- Identity of a resource on the host is a container/volume/network **name** derived from the resource ID (`svc.ContainerName`, `volumeName` in `svc/ec2/ec2.go`), so no record says *where* anything runs. `Instance.ContainerID` is a Docker ID valid on exactly one host.
- Every VPC is a Docker bridge on that host (`svc/vpc/vpc.go`, `createVPC`/`ensureNetwork`). IPs are allocated by the control plane from the subnet range (`allocate`), so they are already cluster-unique.
- Security groups are iptables rulesets loaded into each resource's own network namespace by a helper container (`svc/vpc/fw.go`, `runtime/netns.go` `RunInNetns`). Rules depend only on the resource's own namespace plus a member-address list, which makes them largely node-independent (good news for this design).
- Helper services are one container each, attached to every VPC network at a reserved address: DNS (`base+2`, `svc/route53`), IMDS (`svc/ec2/imds.go`, `homecloud-imds`), S3/MinIO (`base+3`).
- Background loops are singletons started in `server.Run` (`server.go`, `go cw.Run(ctx)` ... `go vpcSvc.RunFirewall(ctx)`), and several services keep authoritative state in memory (SQS messages, CloudWatch metrics, Lambda environment pools, async invoke queues, IMDS tokens, per-service `sync.Mutex` reconcilers such as `ecs.reconcile`).
- Workloads reach the API through `env.ContainerAPI` (`host.docker.internal:<port>`, `server.go` around line 119) and Lambda environments are invoked on `127.0.0.1:<published port>` (`svc/lambda/engine.go`).

The last three bullets matter most: they are why the architecture choice below is not primarily a database question.

## 2. Goals and non-goals

Goals (map to the checklist in #55):

1. Join additional Linux hosts to an installation and see them as nodes (`homecloud node ls`, console page).
2. Schedule instances, ECS tasks, RDS/ElastiCache databases, load balancers and Lambda execution environments across nodes, with placement constraints and capacity accounting.
3. VPCs span nodes: same CIDR, same private IPs, DNS, IMDS, security groups and load balancers keep working between workloads on different nodes.
4. Survive the loss of a worker without losing control-plane state, and reschedule what is safe to reschedule.
5. Optionally replicate control-plane state so the loss of the control node is recoverable in minutes, not hours (Phase 4).
6. Keep the single-binary, zero-dependency story: a one-node install must behave exactly as today, with no new daemons, no extra config and no performance regression.

Non-goals (explicitly, for the first releases):

- Active-active API servers or horizontal scaling of the control plane. One leader serves the API at a time.
- Live migration of instances between nodes, or replicated/shared block storage (Ceph, DRBD, Longhorn). EBS volumes stay node-local, like AZ-local EBS in AWS.
- Geo-distribution, or nodes across lossy WANs (see the edge design for that case).
- Kubernetes, Nomad or Swarm as a substrate. They would replace most of what the services already do (their own reconcilers, IP allocation, firewalling).
- Cross-region features. A cluster is one region; nodes provide availability zones.
- macOS/Windows worker nodes. Docker Desktop and OrbStack keep working as a single-node control plane (and as a control-only node with no workloads in a cluster); workers must be Linux because the overlay needs kernel VXLAN and WireGuard.

## 3. Architecture options

The decision is what replaces "one process owns the Docker socket and `state.json`".

| | A. Controller + agents | B. Active/passive controller (replicated state) | C. Raft-replicated control plane, all nodes equal | D. External store (etcd / Postgres / rqlite) |
| --- | --- | --- | --- | --- |
| Shape | One `serve` is the controller. Workers run `homecloud agent`, which executes node-local operations and reports state. | A + a standby `serve` that tails the store and is promoted on failure. | Every node runs `serve`; state in Raft; one elected leader runs the singleton loops. | `serve` on N nodes talking to a shared DB. |
| Store change | Interface extraction only | Mutation log streamed to standby | `store.Store` becomes the Raft FSM | Rewrite every `store.*` caller's semantics (Update/Retain/Put on JSON docs) |
| Control-plane HA | No (restore from backup) | Yes, with fencing | Yes | Yes, but the DB needs its own HA |
| New moving parts | Agent, mTLS, overlay | + replication, promotion, fencing | + Raft ops (quorum, snapshots, membership), leader-only service gating | + operating a DB |
| Fit with in-memory service state (SQS, metrics, Lambda pools) | Unchanged: all in one process | Lost or re-snapshotted on failover | Same problem: only the leader can own it | Same problem |
| Effort | ~3 person-months incl. overlay | +1 person-month | +1.5-2 person-months on top of A | Larger than C, no benefit over it |
| Single-node regression risk | None | Low | Medium (Raft in the hot path even for 1 node) | High (needs a DB for a home install) |

Why the store is the wrong place to start: making `state.json` highly available does not make HomeCloud highly available, because the singleton loops and in-memory queues (SQS, Lambda async, metrics, ELB health, ECS and ASG reconcilers, IMDS token table) still live in one process. Any "HA control plane" must therefore be active/passive with a single leader running those loops. That is B or C with leader gating, and either of them needs the node-execution layer (A) first. So **A is a prerequisite of everything else, and B/C are optional upgrades on top of it**.

### Recommendation

1. **Ship A first**: one controller, N agents. It delivers goals 1 to 4.
2. **If HA is wanted (Phase 4), prefer "C-lite": embed `hashicorp/raft` with the existing `store` as the FSM, three control-capable nodes, leader-only singleton loops.** Reasons: the store is already an in-memory map with a tiny mutating surface (`Put`/`Delete`/`Update`/`Retain`), so the FSM is a few hundred lines (log entry = collection, id, JSON or delete; snapshot = the map we already serialize). It removes the O(state) rewrite of `state.json` on every mutation, which is also a scaling issue above a few thousand resources. It needs no external dependency. Raft log/stable store can use `raft-boltdb` (bbolt is already a dependency for DynamoDB, `svc/dynamodb`).
3. **Do not adopt an external store**, and do not adopt SQLite+Litestream: Litestream gives asynchronous replication of a file with manual failover and needs the store rewritten on SQLite; it is B with worse fencing and a bigger migration than the Raft FSM.
4. Runtime-only state that is not in the store (SQS messages, metrics, Lambda pools) is **not** replicated in Phase 4: SQS already snapshots every 2 s to `sqs/<queue>.json`; replicate those snapshot files to the standby by the same Raft snapshot path or accept the window. Document it as "at-most-2-second message loss on control failover", which is honest and matches SQS-at-least-once expectations only partly (messages sent in the window are lost, not duplicated).

## 4. Node model, join and authentication

### Records

A `Node` document in a new `nodes` collection (new package `cli/internal/svc/node`):

```
id (n-0abc...), name, az (us-east-1a), arch, os, docker_version,
address (WireGuard/overlay IP), public_address, labels{}, taints[],
capacity{vcpus, memory_mb, disk_mb}, allocatable{...},
state: joining | ready | suspect | down | draining | cordoned,
last_heartbeat, agent_version, cert_serial, created_at
```

The control node is itself a node (`n-local`), registered at first start. A cluster of one is the current behaviour.

### Join

1. On the controller: `homecloud node token create --az b --labels disk=ssd --ttl 15m` creates a single-use token `hcj_<id>.<secret>` (secret stored as SHA-256, like access keys in `svc/iam`). The token output also carries the controller's URL and the CA fingerprint so the joiner can pin the CA (no trust-on-first-use).
2. On the worker: `homecloud agent join <controller-url> --token hcj_... [--ca-fingerprint sha256:...]`. The agent verifies the controller certificate against the pinned CA, generates a key pair locally, sends a CSR plus its facts (arch, cpus, memory, Docker version) over TLS authenticated by the token.
3. The controller validates the token (single use, TTL), creates the `Node`, and signs a client certificate (CN `node:<id>`, 30-day validity, SAN with the node's overlay address) using the private CA that already exists (`svc/acm` creates an ECDSA P-256 CA on first use). The CA is a dedicated intermediate ("HomeCloud cluster CA") so that revoking the cluster trust never affects user-issued ACM certificates.
4. The agent stores cert/key under its own data dir (0600), installs a systemd unit (`homecloud agent install-service`, reusing `cmd/service.go`), and from then on **dials the controller** (outbound only) and keeps a long-lived HTTP/2 stream. Commands flow down that stream, events and heartbeats flow up. Outbound-only means workers behind NAT and, later, edge devices work without opening ports; the controller needs one reachable port (the existing API port, path `/agent/v1`, mTLS required for that prefix).
5. Certificates renew at 2/3 of lifetime over the same channel. `homecloud node rm` revokes by serial (controller keeps a denylist; a short lifetime bounds exposure anyway).

### Agent surface

The agent is the same binary (`homecloud agent run`), with no state store and no service code. It exposes to the controller only operations the code needs today, in three groups:

- **Runtime**: what `runtime.Docker` does (create/start/stop/remove/inspect container, networks, volumes, exec with stdin/TTY, logs, stats, events, `CopyIn`, image pull/build, `RunInNetns`).
- **Network**: create/remove a VPC bridge and its VXLAN leg, update WireGuard peers and FDB entries (section 6), apply firewall jobs.
- **Volumes**: snapshot/copy/export helpers (section 7).

Decision to make in Phase 1: implement the runtime group as a **label-enforcing proxy of the Docker Engine API** (the agent rewrites create calls to force `homecloud.managed/account/node` labels and refuses privileged flags the controller did not ask for; `fsouza/go-dockerclient` can target it with a custom endpoint), or as **typed RPC**. The proxy lets us keep ~40 raw `env.Docker.C.*` call sites working on day one; typed RPC is cleaner and is the eventual goal. Recommendation: start with the proxy behind the `runtime.Host` interface introduced in Phase 0 so call sites migrate one service at a time, and treat the proxy's allow-list as the security boundary (section 11).

## 5. Scheduling

### Model

- **AZ = a group of nodes.** `Node.az` is a label set at join. Subnets already carry `AvailabilityZone` (`svc/vpc/vpc.go`, `Subnet`; default VPC creates `region+"a"` ... at line ~168). A workload launched in a subnet must run on a node in that subnet's AZ. The default cluster shape is one node per AZ (`us-east-1a`, `b`, `c`, ...) because that makes subnets, ASG spreading (`svc/autoscaling` already spreads launches across subnets) and "Multi-AZ" meaningful with no new concepts. Several nodes per AZ are allowed (capacity), a single node in several AZs is not.
- **A `Placement` gets a `Node`.** `vpc.Service.Place(subnetID, owner)` (vpc.go ~line 243) today returns VPC, subnet, IP and network name. It gains `Node` chosen by a new `sched.Pick(req)` and the workload record gains `NodeID`. `Instance`, ECS `Task`, RDS/ElastiCache `DBInstance`, ELB `LoadBalancer`, EBS `Volume`, EFS `FileSystem` and Lambda `environment` records all get `NodeID`.
- **Request** = {AZ (from subnet), vcpus, memory_mb, arch, required labels/taints tolerated, volumes already pinned (affinity), anti-affinity group (ASG/ECS service/Multi-AZ pair), devices (future)}. Instance type already gives vcpus/memory (`svc/ec2/catalog.go`: `VCPUs`, `MemoryMB`); ECS task size and RDS class have equivalents; Lambda uses memory.
- **Algorithm** (intentionally simple, 150 lines): filter (state==ready, AZ, arch, labels/taints, allocatable >= request, volume affinity), then score (spread across nodes for one ASG/service/DB group; else least-allocated for home labs that want even load, configurable to bin-pack to save power). Ties broken by node ID for determinism. Overcommit: CPU by ratio (default 4x, since instances are limited by CFS quota, `NanoCPUs`), memory not overcommitted by default.
- **Capacity accounting** is computed from records (sum of requests of non-terminated workloads per node), not from live stats, so it is deterministic and survives restarts. A node's `allocatable` = detected capacity minus a reserve (system + agent).
- **Explicit placement** for operators: `RunInstances` with `Placement.AvailabilityZone` (AWS API) maps to AZ, `--node n-abc` (native API/CLI) pins; a tag `hc:node` is not used (records, not tags).
- **Stateful pinning**: anything with a local volume (EBS volume, RDS data, EFS, S3 in single mode) is pinned to the volume's node. Scheduling for those is "where is my data".
- **Lambda environments** are placed by locality: prefer a node that already has an idle environment for the function (warm pool), else least-loaded node in the function's VPC (if VPC-attached) or any ready node. Invocation must then reach the environment remotely: replace the `127.0.0.1:port` published call (`svc/lambda/engine.go`, the `exec curl` fallback is the model) with an agent call `POST /agent/v1/containers/{id}/forward`, or publish the RIE port on the node's overlay address. Start with the forwarding call; it needs no extra ports.
- **Reconcilers stay on the controller** and gain node awareness: `ecs.reconcile` (5 s), `autoscaling.Run` (10 s), `rds.Run`, `elb.Run` health checks, `vpc.RunFirewall`. Each asks the scheduler instead of the local Docker.

## 6. Cross-node VPC networking

Requirement: a VPC's CIDR behaves as one L3 (practically L2) network across nodes, private IPs stay as allocated by `vpc.allocate`, and Docker's embedded DNS, the reserved DNS/IMDS/S3 addresses and per-namespace iptables keep working.

### Options considered

| Option | Verdict |
| --- | --- |
| Docker Swarm overlay networks | Works, and is "free", but requires Swarm mode (raft, manager quorum, its own CA and join flow) alongside ours, and static IPs (`--ip`) on attachable overlay networks are restricted. It would create two clusters to operate. Rejected. |
| Routed, per-node subnet slices over WireGuard | Simple, robust, no L2. But one subnet would no longer be one L2 segment; IPs would have to be allocated from a node-local slice, which breaks "stable IP moves with the resource" and the current allocator. Rejected. |
| **VXLAN per VPC, carried over a WireGuard mesh, attached to the existing Docker bridge** | Preserves the model as it is: same bridge per VPC, same allocator, same subnets. WireGuard gives authenticated, encrypted transport (VXLAN alone is cleartext and unauthenticated). Chosen. |

### Design

- **Underlay**: each node gets a WireGuard interface `hcwg0` and an overlay address from a cluster range (default `100.72.0.0/16`, configurable, must not overlap any VPC CIDR; checked at VPC creation like `overlapsAny`). The controller distributes peer public keys and endpoints; nodes reach each other directly (full mesh; fine up to ~20 nodes; beyond that use hub relay, out of scope). Keys are generated on the node, only the public key leaves it. The controller node is a peer and, for NAT'd workers, the relay of last resort (hub-and-spoke mode flag).
- **Per-VPC VXLAN**: `vni = 10000 + vpc ordinal`. On each node that hosts (or may host) a member of the VPC the agent creates the Docker bridge as today (`CreateNetwork` with an explicit bridge name via `com.docker.network.bridge.name`), plus `vxlan<vni>` (local = node overlay IP, `dstport 4789`, `nolearning`) enslaved to that bridge. FDB entries for every peer node are installed statically (`bridge fdb append 00:00:00:00:00:00 dev vxlan<vni> dst <peer overlay IP>`), so BUM traffic is unicast-replicated to peers and there is no multicast dependency. MAC learning stays on for unicast. MTU: bridge and containers get `underlay MTU - 80 (WireGuard) - 50 (VXLAN)`, normally 1370. Docker network creation passes `com.docker.network.driver.mtu`; existing single-node VPCs keep 1500.
- **Lazy attach**: the VPC bridge and VXLAN leg are created on a node when the first workload of that VPC is scheduled there and removed when the last leaves (agent reports membership), so a node that never hosts a VPC carries none of its traffic.
- **Addresses that exist once per VPC today** (gateway `.1`, DNS `base+2`, IMDS address, S3 `base+3`) become **node-local anycast**: every node's bridge carries the same addresses. To keep them node-local, the agent installs an `ebtables`/`nft bridge` rule that drops ARP and frames for those addresses arriving from or going out of the VXLAN port. Result: a container asks its own node's DNS, IMDS and NAT gateway. Risk (spike in Phase 2): duplicate-address behaviour and ARP flapping if the filter is wrong; fallback is per-node distinct gateway addresses taken from the reserved first-four range of the subnet (`.1` .. `.3`, which `allocate` already skips).
- **Egress/NAT**: Docker's bridge masquerade on each node, unchanged. Internet-facing ports are published on the node that hosts the workload, so `Instance.PublicHost` becomes the node's public address (new `Node.public_address`, default the address the node joined with), and `PublicPorts` remain host ports on that node. Port collisions are scheduled per node (today `ports.go` assumes one host).
- **Security groups** need almost nothing new, because enforcement is inside each container's namespace and depends only on the source/destination IP, which VXLAN preserves: `RegisterMembers` already returns members with addresses (`vpc/fw.go` `fwMembers`); it returns cluster-wide members, and `fwJobs` are routed to the agent of the member's node (`applyJob` calls the node's `RunInNetns`). The Docker `start` event watcher (`watchStarts`) moves into the agent and emits events to the controller. The "host as a source" rule (network gateway address) needs a per-node equivalent: traffic from the node's gateway is local by construction, traffic from other nodes arrives from container IPs, not the gateway. `bridge-nf-call-iptables` must be consistent between nodes; the agent checks and warns at join.
- **DNS**: `homecloud-dns` (CoreDNS, `svc/route53`) runs on every node that hosts a VPC, from the same rendered zone files; the controller pushes zone files over the agent stream (today written to a local dir and reloaded). Names of containers (`<id>.rds.internal`, `ip-…internal`) must resolve cluster-wide, and Docker's embedded DNS only knows local containers, so the zone renderer adds A records for every workload in the VPC (it already renders `<db>.rds.internal` and ECS `.ecs.internal` records; instance `ip-…internal` names move into it).
- **IMDS**: one `homecloud-imds` helper per node (as today, per host), forwarding to the controller's `/_imds/` with the caller's IP; the controller already identifies instances by private IP (VPC ranges never overlap), so nothing changes server-side except authenticating the forwarder as a node (node cert instead of the per-server secret). Workers need a route to the controller for that; it goes through the overlay (controller has an overlay address) or the agent channel.
- **Workloads calling the API** (`env.ContainerAPI`, `host.docker.internal`): the address becomes per node (`server.go` ~119-130 picks it once today). Workers get an agent-local forwarder listening on the bridge gateway that proxies to the controller over mTLS.
- **S3 (`s3.internal`, `base+3`)**: each node's anycast S3 address is a tiny TCP proxy to the node that runs MinIO (section 7).
- **Load balancers**: nginx per LB container stays on its placed node; targets are reached over the overlay. Health checks run from inside the VPC as today (`svc/elb`), so they traverse VXLAN with no change.
- **Platform caveat**: creating VXLAN/WireGuard links and bridge rules requires host privileges. The agent runs as root (systemd unit) and does it directly; it does not run these from a container. This is a deliberate exception to the "no host root" portability property in `docs/architecture.md` (the firewall helper still runs as a container). The exception applies to workers only; the controller in a cluster has the same requirement only if it also hosts workloads in a multi-node VPC.

## 7. Storage

| Resource | Behaviour in a cluster |
| --- | --- |
| EBS volumes (`svc/ec2/volumes.go`, Docker volumes `hc-<id>`) | Pinned to a node/AZ (`Volume.NodeID`, `AvailabilityZone` which the record has). `AttachVolume` requires the instance on the same node (AWS requires same AZ; with multiple nodes per AZ we require same node, documented). Detach + attach to another node's instance is not possible directly; use snapshot + create-volume-in-AZ. |
| Snapshots | Become regional objects: `CreateSnapshot` streams a tar of the volume to a system bucket in S3 (MinIO), and `CreateVolume --snapshot` on any node restores from it. Today `copyVolume` copies between local volumes via a helper (`volumes.go`); the helper keeps being the mechanism, with S3 as the transport. This also is the migration path to move data between nodes (a "move volume" operation = snapshot, create, swap). |
| Instance root disks | Container layers (or qcow2 on a volume for VM instances). Node-local, lost with the node unless the instance uses a data volume snapshotted regularly. Same as today's limitation, now with a bigger blast radius. |
| EFS (`svc/ec2/efs.go`, Docker volume shared by mounting) | Stops working across nodes. Phase 3: back a file system by an NFS server container (e.g. Ganesha or a kernel-NFS helper) on a home node, exposed at `<fs>.efs.internal` and mounted by instances with the Docker `local` driver's NFS options (`type=nfs`). Single-node file systems keep the direct volume mount. Consistent with "EFS is a network filesystem" semantics, and a single point of failure until replicated (non-goal). |
| RDS/ElastiCache/DocumentDB | Pinned to node by their volume. Snapshots are engine-native dumps (`rds-snapshots/*.bak`), which are relocatable, so restore-to-other-AZ works (they need to be in S3/controller storage, not a worker's disk; today they are in the data dir on the controller, fine). Real Multi-AZ (streaming replica on another node + promotion) is a follow-up, not in scope. |
| S3 (MinIO, `homecloud-s3-data` volume) | Phase 3 default: single MinIO on a chosen node (`--s3-node`), with periodic backup. Optional later: MinIO distributed mode needs >= 4 drives/nodes for erasure coding and its community distribution/licensing has changed recently: **verify current licensing and feature availability before committing**, and evaluate alternatives (Garage, SeaweedFS) in a one-week spike. Not needed for the first releases. |
| Control-plane data dir (`state.json`, `master.key`, Lambda code, logs, trail, `dynamodb.db`, SQS snapshots) | Lives on the controller. Workers hold no authoritative state, only containers and volumes. |
| Lambda code | Stays on the controller; the agent receives the package when starting an environment (`CopyIn` into the container, as done now), with a per-node cache keyed by code SHA. |
| DynamoDB (`dynamodb.db`, embedded bbolt) | Controller-local; it is part of the control process, not a workload. Not distributed in this design. |

**Backups** (`cli/cmd/backup.go`, `cli/internal/system/backup.go`): the controller backup streams the data dir plus every `hc-`/`homecloud-` volume by pausing containers briefly and copying. In a cluster it asks each agent for the volumes it owns, and the archive layout records `node_id` per volume. Restore places volumes back on the node that owned them if it is present, otherwise asks for a mapping (`--map n-old=n-new`). Backing up a node's volumes uses the same pause-and-copy on that node; volumes of a `down` node are skipped with a warning listing them.

## 8. Failure handling

### Detection

The agent heartbeats every 5 s on its stream with node stats (CPU, memory, disk, container count). Node states: `ready` -> `suspect` after 15 s without heartbeat -> `down` after 60 s (both configurable; slow edge links want higher values). `suspect` only flags; new placement avoids suspect nodes. Transition to `down` is logged to CloudTrail (native event `homecloud:NodeDown`) and emits a CloudWatch metric `HC/Node` `Up` (0/1) so alarms work with existing machinery (`svc/cloudwatch`).

### What happens to workloads on a down node

| Workload | Action |
| --- | --- |
| ECS task (service-managed) | Marked STOPPED; the service reconciler starts replacements on other nodes, exactly as it already replaces exited tasks (`ecs.reconcile`). |
| ASG instance | Marked terminated/unhealthy; the group launches replacements (existing loop in `svc/autoscaling`). |
| Standalone EC2 instance, RDS, EBS-pinned resources | **Not moved automatically.** State becomes `unknown` (surfaced as `impaired` in the EC2 API `DescribeInstanceStatus`) because their data is on the dead node; an operator can `homecloud node evacuate n-x --restore` to recreate instances from the last snapshot on another node, losing changes since. Auto-recovery for instances with no pinned local state and a `recover=true` attribute is optional. |
| Lambda environments | Dropped from the pool; new invocations pick another node (they are stateless). |
| Load balancers | Re-created on another node in the same AZ set when they have no pinned state (nginx config is rendered from records), or else flagged. |

### Split brain

With option A there is exactly one writer of cluster state (the controller), so there is no split brain of the store. The remaining hazard is a node that the controller considers `down` but is merely partitioned, whose containers keep running while replacements start elsewhere (duplicate ECS tasks, duplicate Lambda environments). Mitigations:

- Workloads carry a label `homecloud.epoch=<n>`; the controller increments the node's epoch when declaring it down. On reconnect the agent receives the new epoch and **reconciles by removing managed containers whose record was reassigned or whose epoch is stale**; that is the same label discipline the server uses at startup against foreign accounts (`server.go` ~105-107).
- Agents never schedule or start workloads on their own (except the explicit offline-autonomy mode in the edge design), so a partitioned worker cannot create anything new.
- A partitioned worker keeps its running containers (Docker restart policies handle reboots); this is better than stopping them: a blip in the controller should not take down applications.
- Controller HA (Phase 4) adds a Raft term to every command; agents reject commands with a term lower than the highest they have seen, which fences a stale leader.

### Controller loss

Phase 1-3: workers keep running everything already running; API is unavailable; ECS/ASG do not heal until the controller returns (document this). Recovery is restore-from-backup onto any host with `homecloud restore` and re-point the workers (`homecloud agent join --rejoin`, the agent cert is still valid because the CA key is in `master.key`-protected state restored with the backup). Phase 4 makes this automatic with a 3-node Raft.

## 9. Upgrades

- Version policy: controller and agents must be within one minor version; the agent stream handshake exchanges versions and the controller refuses `agent < controller - 1 minor`. Agents can be newer only by the same margin.
- Order: controller first, then agents. `homecloud upgrade` (`cli/cmd/upgrade.go`) already replaces the binary and restarts the service; `homecloud upgrade --cluster` walks nodes: cordon, upgrade agent, restart, wait for `ready`, uncordon. Containers are Docker's, so an agent restart does not restart workloads.
- Node maintenance: `homecloud node drain n-x` cordons, reschedules stateless workloads (ECS tasks, Lambda, ASG members), and for stateful ones prints what it cannot move. There is no live migration.
- State migrations (`server/migrate.go`) run on the controller only; new per-node fields default so old records read as `NodeID = "n-local"`. That is also the upgrade path for existing installations: their single host becomes node `n-local` on first start of the new version with no other change.
- Rolling back the controller requires a state backup made before the upgrade, as today. Adding `NodeID` fields is backward compatible for reading by older versions (unknown JSON fields are ignored), but an older controller does not know about agents, so do not roll back across Phase 1 without draining workers to the controller node.

## 10. Security

- **Transport**: every controller-agent and agent-agent link is authenticated and encrypted: mTLS for control (client cert CN `node:<id>`, issued by the cluster CA), WireGuard for the overlay. No cleartext VXLAN ever leaves a host.
- **Join tokens** are single-use, short-lived, SHA-256 at rest, bound to an AZ/labels chosen by the operator, and audited (CloudTrail event on create/use). The CA fingerprint pin removes trust-on-first-use.
- **Least privilege for agents**: a node identity may only call `/agent/v1/*` for its own node ID; it cannot call the public API, read secrets, or ask for another node's data. The controller sends workloads their secrets/env at launch (as today) and never ships `master.key`, KMS material, or IAM state to an agent.
- **Blast radius**: agents run as root and control Docker, so a compromised node is root on that node and can read everything of its workloads (same as today for the single host). What it must not gain is other nodes or the control plane: hence the label-enforcing Docker proxy with an allow-list (no `--privileged`, no host PID/network, no bind mounts outside the agent's data dir and HomeCloud-labelled volumes, unless the controller requested the specific capability for a helper such as the firewall).
- **Revocation**: `homecloud node rm` adds the cert serial to a denylist checked on every connection and removes the WireGuard peer from all nodes; short cert lifetime limits what revocation must cover.
- **Docker socket exposure**: the agent never listens on a TCP Docker port; the Docker socket stays local (`/var/run/docker.sock`), a contrast with "just enable Docker's remote TLS API", which would be root-equivalent and unfiltered.
- **API policy**: node management uses native IAM actions (`homecloud:CreateNodeToken`, `homecloud:DrainNode`, ...), root by default, so a user with EC2 rights cannot add hardware or choose a node unless granted.
- **Network exposure**: only two listeners are added on a worker: the agent needs none (outbound-only); WireGuard UDP (default 51820) between nodes. Document firewall rules.

## 11. What changes in the code

Package-level plan. "New" means a new package.

| Package | Change |
| --- | --- |
| `cli/internal/runtime` | Introduce `type Host interface` with the methods `*Docker` already has (`Run`, `Start`, `Stop`, `Remove`, `Inspect`, `Exec`, `CopyIn`, `Logs`, `Stats`, `CreateNetwork`, `Connect*`, `CreateVolume`, volume helpers, events). `*Docker` implements it for the local node; new `remote.go` implements it over the agent channel. Add `Hosts` (node ID -> `Host`, `Local()` for single node). The ~40 raw `env.Docker.C.*` uses are first wrapped into `Host` methods (mechanical, file-by-file, each PR green). |
| `cli/internal/svc` (`env.go`) | `Env.Docker *runtime.Docker` becomes `Env.Hosts`, with `Env.Docker` kept temporarily as an alias to the local host so services migrate incrementally. `ContainerAPI` becomes `Env.ContainerAPIFor(node)`. |
| `cli/internal/store` | Extract `type Backend interface` (`Put`, `Get`, `List`, `Delete`, `Update`, `Retain`); current file implementation stays default. Phase 4: Raft FSM implementation. |
| **New** `cli/internal/svc/node` | Node records, join tokens, cert issuance (uses `svc/acm` CA primitives), heartbeats, state machine, native API `/api/v1/nodes`, `homecloud node ...` commands. |
| **New** `cli/internal/sched` | Filter/score placement, capacity accounting, anti-affinity groups. Pure functions over records, unit-testable without Docker. |
| **New** `cli/internal/agent` (+ `cli/cmd/agent.go`) | Agent server/client, stream protocol, Docker-API proxy with label enforcement, network manager (WireGuard/VXLAN/FDB/ebtables), volume helpers, event forwarder, `homecloud agent join/run/install-service`. |
| `cli/internal/svc/vpc` | `Place()` returns a node; bridge create/remove becomes per node and lazy (`ensureNetwork`, `createVPC`, `DeleteVPC`, `detachInfra`); MTU; `fw.go`: members cluster-wide, jobs dispatched to the owning node's `Host`, `watchStarts` fed by agent events; overlay CIDR overlap check. |
| `cli/internal/svc/ec2` | `Instance.NodeID`; `Launch` asks scheduler; `volumes.go` pins and validates same-node attach, snapshot export/import through S3; `imds.go` per-node helper and node-authenticated forwarding; `ports.go` per-node port allocation; `PublicHost` from node. `vm/` runner image built per node arch. |
| `cli/internal/svc/ecs`, `autoscaling`, `rds` (incl. ElastiCache), `elb` | Scheduler integration; reconcilers handle `down` node; DB volumes pinned. |
| `cli/internal/svc/lambda` | Environment placement and remote invocation via agent forwarding; log following per node. |
| `cli/internal/svc/route53` | CoreDNS per node, zone push, cluster-wide record rendering. |
| `cli/internal/svc/cloudwatch` | Collector pulls stats from every node's agent (`Stats`), `HC/Node` metrics; container log groups read Docker logs via the owning host. |
| `cli/internal/svc/s3` | Choose MinIO node; per-node anycast proxy at `base+3`. |
| `cli/internal/server` | Mount `/agent/v1` with mTLS; start node monitor loop; per-node workload listener; migration defaulting `NodeID`. |
| `cli/internal/system` and `cli/cmd/backup.go` | Per-node volume streaming and restore mapping. |
| `cli/internal/web` (console) | Nodes page, node column and placement selector on instances, node health. |
| `docs/`, CI | Two-node live tests (two Docker-in-Docker hosts or two VMs on the runner) run nightly; unit tests for scheduler and join flow in regular CI. |

Testing note: the existing live tests (`*_live_test.go`) need Docker and skip otherwise. Cross-node tests need two Linux hosts; plan on Docker-in-Docker with `--privileged` and WireGuard-capable runners, or a self-hosted runner pair. Budget a week for this harness; without it the overlay work is unverifiable.

## 12. Phased plan

Effort is in person-weeks for one engineer familiar with the codebase; a two-person team can overlap phases 1 and 2.

| Phase | Scope | Effort | Exit criteria |
| --- | --- | --- | --- |
| **0. Seams** | `runtime.Host` interface and `Hosts`; wrap raw client uses; `store.Backend` interface; `NodeID` on records (defaults to `n-local`); node record for the local host; no behaviour change. | 3 to 4 | All tests and live tests green on a single node; binary size and start time unchanged; upgrade from previous release keeps state. |
| **1. Nodes and scheduling** | Agent, join tokens, cluster CA, heartbeat, `Host` over the agent channel, scheduler, AZ = node. Workloads of a VPC are confined to the node(s) where the VPC exists, **no overlay yet**: a VPC created in a subnet whose AZ maps to node B lives entirely on B; creating a subnet in a second AZ inside such a VPC is refused until Phase 2. Spreading works across VPCs and for ASG/ECS services that span VPCs' subnets in one VPC/AZ. Console and CLI for nodes. | 5 to 6 | Join a second Linux host; launch instances/tasks/DBs/Lambda into subnets in different AZs; drain a node. |
| **2. Cross-node VPC** | WireGuard mesh, VXLAN per VPC, FDB management, anycast DNS/IMDS/S3 addresses, cluster-wide SG membership, per-node IMDS/DNS/API forwarder, LB across nodes, MTU. Includes the test harness. | 5 to 6 | Instance in subnet-a pings and connects to an RDS in subnet-b on another node; SG references between nodes enforced; `s3.internal`, DNS and IMDS work from both nodes. |
| **3. Storage and failure handling** | Volume pinning and snapshot export via S3, EFS-over-NFS, MinIO placement, cluster backup and restore with node mapping, node-down handling and epoch reconciliation, evacuate/restore, cluster upgrade orchestration, `HC/Node` metrics. | 5 to 6 | Kill a worker: ECS/ASG recover elsewhere; `evacuate --restore` restores an instance from snapshot; cluster backup/restore round trip. |
| **4. HA control plane (optional)** | Raft FSM over `store`, three control nodes, leader-gated singleton loops, term fencing in the agent protocol, SQS/metrics snapshot shipping. | 6 to 8 | Kill the leader: API back in under 30 s, agents reconnect, no lost state documents. |

Totals: Phases 0-3, the credible "multi-node" release, are about **18 to 22 person-weeks** (4 to 5 months for one engineer, about 3 for two). Phase 4 roughly adds 2 months. Each phase ships independently and is useful alone. Phase 1 alone satisfies "join hosts" and "schedule across nodes" for the common case of several VPCs; Phase 2 is the largest technical risk (ARP/anycast and MTU behaviour) and should start with a two-week spike on two VMs before committing the estimate.

## 13. Open questions

1. Anycast reserved addresses vs distinct per-node addresses (section 6): decide after the spike.
2. Docker-API proxy vs typed RPC for the first agent (section 4): recommendation is proxy first; revisit if the allow-list grows unwieldy.
3. Do we support more than one node per AZ in Phase 1, or one-to-one only? Simpler is one-to-one; the scheduler API should not preclude the other.
4. MinIO distributed mode depends on upstream licensing and packaging (verify before relying on it).
5. Does the control node take workloads by default? Proposal: yes for a two or three node home lab (`--no-workloads` to opt out).
