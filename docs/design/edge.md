# Design: edge compute and hardware integrations

Status: proposal. Tracks [issue #56](https://github.com/solinode/homecloud/issues/56) (roadmap phase 4). It depends on the [multi-node design](multi-node.md) for anything that runs on a second device; sections and items marked **(speculative)** are ideas we have not validated and should not be promised.

## 1. What "edge" should mean here

HomeCloud's audience runs it at home or in a small office. "Edge" for that audience is concrete and modest:

1. **My hardware, used by my cloud.** A GPU for inference, a Zigbee/Z-Wave USB dongle, a capture card, a serial adapter, GPIO on a Raspberry Pi, exposed to an instance or container with IAM and quotas around it.
2. **Small machines as nodes.** A Raspberry Pi, an Intel NUC or an old laptop joins the cluster and runs Lambda functions, ECS tasks or plain containers close to the sensors and devices it is wired to, even when its link to the main server is unreliable.
3. **Devices as first-class resources.** Something like AWS IoT Core: a registry of "things" with certificates and policies, an MQTT endpoint they talk to, rules that route messages into HomeCloud's Lambda/SQS/SNS/DynamoDB/EventBridge, and per-device shadows.
4. **Deployment to a fleet.** Something like Greengrass or ECS Anywhere: declare "run this component on all devices in group X" and have each device converge on it, including while offline.

The first item is single-node and cheap. The second builds on multi-node. The third is independent of multi-node and is the most recognisable AWS-shaped feature. The fourth is mostly a packaging of ECS plus the second.

Non-goals: fleet management of OS images, device hardware certification, cellular/LoRaWAN gateways, a Kubernetes distribution for the edge, and anything that requires HomeCloud to run on microcontrollers (those are MQTT clients of HomeCloud, not nodes).

## 2. What exists today that we build on

- `runtime.RunSpec` (`cli/internal/runtime/docker.go`) already carries `CapAdd`, `Mounts`, `Ports`, `NanoCPUs`, `MemoryMB`, `Restart`. It has **no** device, GPU, privileged or cgroup-rule fields yet (verified: there is no `Devices`, `Privileged` or device runtime field), so passthrough is a small, additive change.
- Instance types come from a static catalog (`cli/internal/svc/ec2/catalog.go`: `VCPUs`, `MemoryMB`). Instance launch, including volumes, images and metadata, is in `svc/ec2/ec2.go` (`Launch`, `runSpec`).
- Lambda runs in AWS's official base images pulled for the host's architecture (`svc/lambda/runtimes.go`, `engine.go`); AWS publishes arm64 variants, so functions can already run on ARM hosts. ECS task definitions and reconcilers are in `svc/ecs`; EventBridge targets (Lambda/SQS/SNS) in `svc/events`; IAM policy evaluation in `svc/iam`; a private CA in `svc/acm`; secrets in `svc/secrets`.
- VM-backed instances (branch `ec2-vm-instances`, `svc/ec2/vm`) run QEMU inside a container; relevant for VFIO passthrough below.
- Everything runs on one Docker host; multi-node is designed but unbuilt ([multi-node.md](multi-node.md)).

## 3. Pieces, in the order we recommend

### E1. Device passthrough for containers and instances (single node; small)

**What.** Let a user attach host devices to an instance or ECS task:

- Character/block devices by path or stable selector: `/dev/ttyUSB0`, `/dev/bus/usb/...`, `/dev/video0`, `/dev/gpiochip0`, `/dev/i2c-1`, `/dev/dri/renderD128`.
- NVIDIA GPUs via the NVIDIA Container Toolkit (Docker `--gpus` / device requests, or CDI `nvidia.com/gpu=all`), AMD/Intel GPUs via `/dev/dri` and `/dev/kfd`.

**How (concrete).**

- `runtime.RunSpec` gains `Devices []DeviceMapping` (host path, container path, permissions), `DeviceRequests` (driver `nvidia`, count or IDs, capabilities) and `DeviceCgroupRules` (`c 189:* rmw` makes hot-plugged USB devices usable without restart). Mapped onto go-dockerclient's `HostConfig.Devices`, `DeviceRequests`, `DeviceCgroupRules`. About 40 lines in `runtime/docker.go`, plus tests in the style of `runtime/bindings_test.go`.
- New native concept **device inventory**: the server scans the host (`/dev`, `lsusb`-equivalent via sysfs `/sys/bus/usb/devices`, `nvidia-smi -L` or `/proc/driver/nvidia`, `/sys/class/drm`) and lists them at `GET /api/v1/devices` and in the console, with a stable identity (USB vendor:product plus serial, PCI address, GPU UUID), because `/dev/ttyUSB0` changes across reboots. A user-defined **alias** (`zigbee-stick`) maps to the stable identity; launches refer to aliases; HomeCloud resolves the alias to the current path at start.
- **Exclusivity and accounting.** A device (or GPU) is allocated to at most one instance unless marked shareable (video devices, `/dev/dri` render nodes). Allocation is a record (`device_allocations` collection) released on terminate, like IP allocations in `svc/vpc`.
- **EC2 surface.** Native API: `devices: ["zigbee-stick", "gpu:1"]` in `RunInput` (`svc/ec2/ec2.go`). AWS-compatible surface: a family of instance types that carry devices (`g-small`-style names; we should not claim real `g5.xlarge` semantics), so Terraform can request them with no new API. ECS task definitions get `linuxParameters.devices` (already part of the AWS schema) and `resourceRequirements` of type `GPU`, both of which are real AWS fields.
- **IAM and safety.** Passing a device into a container is close to host access, so require a new action (`homecloud:AttachDevice`, resource = the device alias ARN), default-denied for non-root. Never allow raw paths from non-root users, only aliases that an admin defined. Reject `/dev/mem`, `/dev/kmem`, `/dev/sd*` unless an admin alias explicitly names them.
- **Platform reality.** Works on Linux Docker only. Docker Desktop and OrbStack run containers in a VM that does not see host USB (USB/IP is a workaround, **speculative**). GPU passthrough on macOS does not exist for this path. The console and API should say so when the host cannot offer devices rather than failing at launch. On Apple Silicon this feature is simply unavailable.
- **VM instances.** QEMU-in-container can take a character device (serial) or a USB device (`-device usb-host,vendorid=...`) through the same device mappings, with `--device /dev/bus/usb` on the runner container. PCI/GPU passthrough to a VM via VFIO needs IOMMU, vfio-pci binding, host kernel parameters and usually a dedicated GPU; it is a large support burden and is **speculative**: do not schedule it until E1 containers are in use.

**Why first.** It is single-node (no dependency on multi-node), a few weeks of work, and the most immediately useful to a homelab: local LLM inference in a container with the GPU, Home Assistant-style workloads with a Zigbee dongle, cameras. It also creates the device/resource vocabulary the scheduler needs later.

### E2. IoT registry, MQTT endpoint and rules (single controller; medium)

**What.** An AWS IoT Core analogue. Scope cut for a first version:

| Part | In scope | Notes |
| --- | --- | --- |
| Registry | Things, thing types, thing groups, attributes; `CreateThing`, `ListThings`, `DescribeThing`, groups membership | New package `cli/internal/svc/iot`, native API plus AWS JSON/REST protocol for the `iot` control plane so `aws iot ...` and Terraform work; fits the pattern in `svc/*/aws.go`. |
| Identity | X.509 device certificates issued by a dedicated CA, `AttachThingPrincipal` | Reuse CA primitives in `svc/acm` (ECDSA P-256); separate CA so device trust never overlaps user certs. Also support registering a device's own certificate. |
| Policies | IoT policies (`iot:Connect`, `iot:Publish`, `iot:Subscribe`, `iot:Receive`) with topic resources and `${iot:Connection.Thing.ThingName}` variables | Reuse the IAM evaluator in `svc/iam` (policy documents, wildcards, conditions) rather than writing another one; add the IoT context variables. |
| MQTT | MQTT 3.1.1 (and 5 if the library does) over TLS with mutual auth on 8883, optional WebSocket on the API port | Embed a Go broker library (for example mochi-mqtt) through its hook interface for authentication, ACLs and message interception, versus supervising a Mosquitto/EMQX container. **To evaluate in the first week: embedding keeps the single-binary story and gives in-process rule execution; a container is faster to start but adds a moving part and needs a bridge for rules. Verify the chosen library's licence, maintenance status and hooks before deciding.** |
| Rules engine | `SELECT <fields> FROM '<topic filter>' WHERE <cond>` with a useful subset of the IoT SQL (field selection, `topic(n)`, `timestamp()`, comparison, `AND/OR`, simple functions) and actions: Lambda, SQS, SNS, EventBridge, DynamoDB put, republish to another topic, CloudWatch metric | Actions call the target services in-process with the rule creator's identity and the creation-time authorization, the way EventBridge and SNS targets already do (architecture.md, "Delivery targets are authorized when they are configured"). `svc/events/pattern.go` shows the matching style to follow. |
| Shadows | Named and unnamed JSON shadows, versioned, delta computation, `$aws/things/<name>/shadow/...` topics, plus the `iot-data` HTTPS API | Stored in the store (a document per shadow). Delta/merge rules are the substantial part; scope to classic shadows first. |
| Out of scope at first | Fleet provisioning, jobs, device defender, device advisor, Greengrass core device APIs, retained message persistence beyond what the library offers, MQTT bridging | |

**Where it runs.** On the controller, inside `homecloud serve`, as one more service with its listener (`:8883`) and a per-server secret for in-process use. Broker session and subscription state lives in memory and is rebuilt on reconnect (MQTT clients reconnect anyway). Offline-queued QoS1 messages for persistent sessions would be lost on restart unless snapshotted like SQS (`sqs/<queue>.json`, every 2 s); start with clean-session semantics documented.

**Effort.** Registry + certs + policies about 4 weeks; broker integration with auth/ACL about 3; rules engine about 4; shadows about 3; AWS wire-protocol compatibility and console pages about 3. Roughly 15 to 17 person-weeks for the full first version; a useful slice (registry, certs, MQTT with policies, republish and Lambda/SQS actions) is about 8.

**Why second.** It is the feature people think of when they say "IoT like AWS", needs nothing from multi-node, and composes with everything already built (Lambda/SQS/SNS/EventBridge/DynamoDB, CloudWatch, IAM). Its risk is scope creep, hence the cut above.

### E3. Edge nodes: small devices as cluster nodes (needs multi-node Phases 0 to 2)

**What.** A Raspberry Pi (arm64), a mini PC or an old laptop runs `homecloud agent` and joins the cluster as described in [multi-node.md](multi-node.md) (join token, mTLS, outbound-only connection). The scheduler can place Lambda environments, ECS tasks and containers on it. Requirements specific to edge nodes:

- **Architecture awareness.** Mixed amd64/arm64 clusters. `svc/lambda/runtimes.go` already pulls per-host-architecture images; generalise so the image is chosen for the target node's arch. Images pushed to ECR must be multi-arch or tagged per arch; ECS task placement must filter by `attribute:ecs.cpu-architecture`. Instance types and AMIs get an arch attribute (the VM work already models `aarch64` vs `x86_64`). Nodes with no KVM or too little memory are marked so the scheduler will not place VM instances on them.
- **Resource-constrained nodes.** The agent must be light: no service code, no state store, idle memory in the tens of MB. Node `allocatable` accounts for small memory (1 to 4 GB), a node class label (`edge=true`, `class=pi`) and the scheduler gets taints so ordinary workloads do not land on edge nodes by accident (workloads opt in with a toleration or `--node-class edge`).
- **Not part of the L2 overlay by default.** The multi-node design stretches a VPC over VXLAN+WireGuard, which assumes decent, stable links. A device on home Wi-Fi or LTE should not become a member of a VPC's L2 network (broadcast flooding, MTU, latency). Edge nodes get workloads in an **edge network**: a node-local bridge with NAT, reaching the cloud services through the HomeCloud API/endpoints (`AWS_ENDPOINT_URL`) over the agent channel or a WireGuard hub-and-spoke link. A VPC member workload on an edge node is an explicit opt-in **(speculative)**, to be tested with real links before supporting.
- **Lambda at the edge.** A function with `placement: {node_class: edge}` (native API; no AWS equivalent, Greengrass being the nearest concept) runs its execution environments on edge nodes. Invocation from the controller goes over the agent channel (the multi-node design's container-forward call); invocation from local devices is better served by the local broker (E4). Code packages are pulled from the controller and cached per node.
- **Intermittent links.** The multi-node design treats a missing heartbeat as `suspect`/`down` and reschedules stateless work. For edge nodes the policy flips: they carry `offline=autonomous`, **workloads keep running and are not rescheduled** when the node goes down, and heartbeat thresholds are minutes, not seconds. The node reconciles with the controller when it returns (epoch check from the multi-node design; work created on the node while offline is discarded unless it was part of a local deployment, see E4).
- **Provisioning.** `homecloud agent join` with a one-line installer (`curl | sh` fetching the matching arm64/amd64 binary with checksum verification, installing the systemd unit). A prebuilt Raspberry Pi OS / DietPi image is **speculative**, only worthwhile if people ask.

**Why third.** It has the largest dependency (multi-node Phases 0 to 2) and a real semantic change (offline autonomy, no L2). Do it once the multi-node agent exists; before then, nothing in E3 is testable. It is mostly scheduler labels, arch-aware image selection and policy flags on top of the agent.

### E4. Fleet deployments and local autonomy (Greengrass-like; needs E2 and E3)

**What.** A *deployment* says: "these components, this version, these configuration values, to this target (thing group or node class)". A *component* is what HomeCloud can already run: a container image with env and ports, or a Lambda function, or an ECS task definition revision. The agent stores the desired state locally, reconciles it with Docker, reports status, and continues to run it with no connection to the controller.

Concretely this is the ECS service model (`svc/ecs`: revision, reconciler, placement) with a per-node reconciler that can run standalone, plus:

- **DAEMON scheduling** (one task per matching node), which is a real ECS scheduling strategy and therefore the lowest-effort first delivery: **ECS Anywhere-style** "run this task on every edge node" requires only the scheduler from E3 plus the `DAEMON` strategy and placement constraints (`memberOf(attribute:...)`), both real ECS API fields. Start here. A separate deployment API can be added later if ECS semantics turn out to be too heavy.
- **Local broker on the node** bridging selected topics with the controller's broker (MQTT bridge), so local devices can talk to a local Lambda with low latency and with no cloud link. **(speculative)** Needs topic-level bridging semantics (loop prevention, ordering, QoS) designed up front; consider a Mosquitto container on the node with a generated bridge config as the simplest version.
- **Local shadow sync** (shadow on the node, synced when online). **(speculative)**, because conflict resolution is nontrivial.
- **Staged rollouts, health-gated, rollback.** ECS services already replace tasks one at a time (`ecs.reconcile`); extend with a health gate and rollback for deployments to device groups. Jobs (run-once fleet operations like "rotate credentials") can be modelled as a one-shot deployment **(speculative)**.

**Why last.** It only makes sense once there are nodes (E3) and devices to target (E2), and the ECS-on-edge slice gives most of the value early without the full deployment model.

## 4. Order of work and why

| # | Piece | Depends on | Effort | Value |
| --- | --- | --- | --- | --- |
| E1 | Device passthrough (containers first, USB/serial/GPU) | nothing | 2 to 3 weeks | High for homelabs, small, unlocks ML and smart-home workloads |
| E2 | IoT registry, certs, policies, MQTT, rules, shadows | nothing (uses IAM, ACM CA, Lambda/SQS/SNS/EventBridge) | ~8 weeks for a useful slice, 15 to 17 for the full first version | Highest "AWS recognisability", independent of the cluster work |
| E3 | Edge nodes: arm64, node classes, taints, offline autonomy | multi-node Phases 0 to 2 | 4 to 5 weeks after multi-node Phase 1 (+ overlay phase as needed) | Turns cheap hardware into capacity |
| E4a | ECS `DAEMON` + placement constraints on edge nodes | E3 | 2 to 3 weeks | Greengrass-like result at ECS cost |
| E4b | Local broker bridge, local shadows, rollouts/jobs | E2, E3, E4a | 6+ weeks, **speculative** | Real offline edge, large design surface |

E1 and E2 can be done in parallel with multi-node work by different people because they touch different packages (`runtime` and `ec2` for E1; a new `svc/iot` for E2). E3 must wait for the multi-node agent; its design constraints are fed back into the multi-node plan so the agent does not hard-code assumptions (recommendations: `Node` carries `arch`, `labels`, `taints`, `offline_policy`; heartbeat thresholds are per node; the agent protocol tolerates long disconnects).

## 5. How it builds on the multi-node design

- **Agent and join flow**: edge nodes *are* agents; nothing new except installer and `offline_policy`.
- **Scheduler**: add `devices` (from E1's inventory, reported by each agent), `arch` and node class to the placement request. The device vocabulary from E1 is node-local in the single-node release and becomes per-node in the cluster without an API change.
- **Networking**: edge nodes stay out of VPC L2 overlays by default (section 3, E3); they use the agent channel for control traffic.
- **Storage**: no shared storage with edge nodes; volumes are node-local; snapshots go to S3 over the agent link, which must tolerate intermittent connectivity (resumable uploads; not in the first cut).
- **Security**: a device node is physically exposed, so its cert has a short lifetime and narrower rights than a server node (may only run workloads the controller assigned, cannot be used as an S3 or MinIO target, cannot host the control plane). Secrets are delivered at launch and are at risk on a stolen device; document this.
- **Observability**: edge nodes send metrics/logs to the controller when online; buffering on the node while offline (bounded ring buffer) is needed for CloudWatch continuity and should be part of the agent, not of E3 only.

## 6. Risks and open questions

- **Platform mismatch.** Many homelab users run HomeCloud on macOS (Docker Desktop/OrbStack): device passthrough and VXLAN do not apply there. The features must degrade gracefully and say why.
- **Broker choice for E2** (embed vs container; licence and maintenance) is the main technical unknown; spike first.
- **AWS IoT compatibility surface is huge.** Commit to a named subset and list it in `docs/aws-compat.md`, the way other services do; do not claim IoT Core compatibility.
- **Security review of passthrough** (`--device`, cgroup rules, `--gpus`): each widens the container's reach on the host. Admin-defined aliases and default-deny IAM are the mitigation.
- **Demand.** None of this has been validated with users. Before starting E3 and E4, collect concrete use cases (which boards, which workloads, offline requirements); E1 and E2 are justified without that.
- **Energy and thermals on small boards** (Pi with SD cards): write amplification from Docker and logs on SD media is a real failure source; recommend SSD/USB boot and log rotation in the install guide.

## 7. Phased plan

| Phase | Content | Effort |
| --- | --- | --- |
| Edge 0 | Spike: MQTT library choice, NVIDIA toolkit/CDI behaviour on a real GPU host, a Pi agent prototype against the multi-node agent once it exists | 1 to 2 weeks |
| Edge 1 | E1: `RunSpec` devices and GPU requests, device inventory and aliases, IAM action, EC2/ECS surface, console page, docs | 2 to 3 weeks |
| Edge 2 | E2 slice: registry, certificates, policies, MQTT on 8883, republish/Lambda/SQS/SNS rules, console page; then shadows, `iot-data` API, AWS wire protocol | ~8 weeks, then ~8 more |
| Edge 3 | E3: arch-aware images, node class/taints, offline policy, installer, Pi docs (after multi-node Phase 1; overlay not required) | 4 to 5 weeks |
| Edge 4 | E4a: ECS `DAEMON` and placement constraints; then decide on E4b from user feedback | 2 to 3 weeks, then TBD |

Everything from Edge 3 onward is gated on multi-node Phase 1. Edge 1 and Edge 2 can start immediately.
