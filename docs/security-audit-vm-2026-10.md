# Security audit of VM instances, October 2026

Review of the VM-backed EC2 instances (PR #82) before they run on public servers. Scope:
`svc/ec2/vm.go`, `vm_disks.go`, `vm_access.go`, `capabilities.go`, the terminal and run-command paths
they plug into, and `svc/ec2/vm/` (runner image, entrypoint and helper scripts, QEMU command line, guest
agent protocol, seccomp profile, cloud-init seed, image catalog). Threat model: a malicious guest (root
inside the VM) and a malicious HomeCloud user with limited IAM permissions. Every fix below has a
regression test and is on the `vm-security-review` branch.

Findings marked *fixed* are closed. *Accepted* findings are left on purpose, with the reasoning.

## Summary

| # | Severity | Finding | Status |
| --- | --- | --- | --- |
| 1 | High | A guest could make the server buffer guest agent replies without bound | Fixed |
| 2 | Medium | A data volume could point QEMU at files of the VM container (symbolic link as the disk) | Fixed |
| 3 | Medium | Disk tooling followed backing and data-file references in qcow2 headers written by users | Fixed |
| 4 | Medium | The guest's serial console filled Docker's log without bound | Fixed |
| 5 | Low | VM launches accepted extra volumes at the root disk's mount path or device | Fixed |
| 6 | Low | QEMU ran without its own seccomp sandbox | Fixed |
| 7 | Low | VM containers allow user namespaces and run without AppArmor confinement (passt) | Accepted |
| 8 | Low | `/dev/kvm` is passed through to VM containers | Accepted |
| 9 | Info | Browser terminal sessions are part of the console output | Accepted |
| 10 | Info | A guest can print the markers HomeCloud reads from its console | Accepted |
| 11 | Info | No quotas on VM disk space or instance count | Accepted |
| 12 | Info | A guest reaches the network a container instance reaches | Accepted |

## Fixed

### 1. Unbounded guest agent replies

Run-command and the CloudWatch guest metrics talk to qemu-guest-agent over its virtio-serial channel,
and `qgaCall` read each reply line with `bufio.Reader.ReadBytes`, without a limit. The guest owns its end
of the channel: root in the guest can stop the agent and write a line that never ends. The CloudWatch
collector samples every running VM every minute, so no user action was needed for one guest to exhaust
the server's memory and take HomeCloud down for every user. Lines are now capped at 16 MiB (qemu-guest-agent
itself caps captured output at 16 MiB per stream); a longer reply fails the call. Run-command output beyond
about 12 MiB fails instead of being returned. Test: `svc/ec2/vm_security_test.go` (`TestQGAReplyIsBounded`).

### 2. Symbolic links as data disks

An extra volume is a Docker volume whose `disk.img` QEMU opens as a raw disk. Volumes can be attached to
container instances, where the workload is root and can leave anything in them, including a symbolic
link. Attached to a VM afterwards, the link was followed inside the VM container (by `truncate` and by
QEMU), which handed the guest read-write access to files of that container, its own root disk's qcow2
header among them. With the header the guest controlled how QEMU opened the root disk at the next boot
(see 3). The entrypoint now refuses to boot when a data disk, the root disk or the UEFI variable store is
a symbolic link or not a regular file (`hc-vm-checkdisk raw`). Tests: `svc/ec2/vm/checkdisk_test.go`.

### 3. Untrusted qcow2 headers

QEMU and `qemu-img` follow a qcow2 image's backing file and external data file, which may name any path
or a protocol (`json:`, `nbd:`, ...). A VM's root volume outlives the instance when it is not deleted on
termination and can then be attached to a container instance and rewritten. Snapshots, images and
backups flatten a root volume with `qemu-img convert` in a helper container that sat on Docker's default
bridge network, so a crafted header could make it read files of that container or open connections to
services on the Docker host and the provider's network. Now:

- `hc-vm-checkdisk qcow2` reads the header itself (with `od`; `qemu-img info` would already open the data
  file) and refuses symbolic links, non-qcow2 files, external data files, and any backing file other than a
  plain file name directly in the image cache (`/images/<name>`).
- `hc-vm-flatten` checks the root disk before converting it, and the flatten helper has no network.
- The entrypoint checks the root disk at every boot and an AMI's disk before copying it.

Tests: `svc/ec2/vm/checkdisk_test.go` (crafted headers, symbolic links, wiring into the scripts),
`TestVMFlattenHasNoNetwork`. The checker was also run against images made by the runner's `qemu-img`
(QEMU 10.0): overlays on the cache pass; host-file, `json:`/NBD, `..` and data-file images are refused.

### 4. Unbounded console log

QEMU copies the serial console into the container's output, which Docker keeps with the daemon's
default logging (unbounded `json-file` on a stock install). A guest printing without pause filled the
Docker host's disk, and HomeCloud reads the whole log while it waits for a boot and when it explains why
the guest agent does not answer. VM containers now log to `json-file` rotated over two 16 MiB files
(`RunSpec.LogMaxBytes`). `GetConsoleOutput` keeps working; on a guest that floods its console the oldest
output is dropped. Test: `TestVMContainerConfinement`.

### 5. Extra volumes at the root disk's place

The native launch route took each extra volume's mount path from the request. A path of `/vm` (where the
root volume is mounted and where QEMU trusts `disk.qcow2`) was only stopped by Docker refusing duplicate
mount points, and a root device name was accepted for an extra volume. Extra volumes of a VM launch are
now named by a validated EBS device that is not a root device, placed at `/mnt/<device>` in the record
and mounted under `/disks/<volume ID>`, as `AttachVolume` already did. Test: `TestVMExtraVolumes`.

### 6. QEMU sandbox

QEMU is the component a guest attacks first. It now runs with
`-sandbox on,obsolete=deny,elevateprivileges=deny,spawn=deny,resourcecontrol=deny`: after start-up it
cannot fork, exec, change credentials or scheduling, on top of the container's seccomp profile.
Verified with the runner's QEMU (TCG, UEFI boot, QMP reset and power-down); the CI `vm` job covers KVM.
Tests: `svc/ec2/vm/vm_test.go`, `TestVMContainerConfinement`.

## Accepted risks

**7. User namespaces and AppArmor.** passt sandboxes itself in new user and mount namespaces, so the VM
container's seccomp profile is Docker's default plus `unshare`, `mount`, `umount2` and `pivot_root`, and the
container runs with `apparmor=unconfined` because Ubuntu's `docker-default` profile denies those mounts.
No capability is added and the default capability set is unchanged, so the mounts only work inside a user
namespace the process created; Docker's masked and read-only `/proc` paths still apply. What it does add
is the kernel's user namespace code to the attack surface of a compromised QEMU (narrowed by finding 6).
Administrators who want AppArmor confinement can load a profile that allows user namespaces, mount and
`pivot_root` and name it in `HC_VM_APPARMOR`.

**8. `/dev/kvm`.** Hardware acceleration exposes the host kernel's KVM code to the guest, as every
KVM-based cloud does. Only VM containers get the device. `HC_VM_ACCEL=tcg` runs guests emulated, without
it, at a large speed cost.

**9. Terminal sessions in the console output.** The browser terminal of a VM is its serial console, and
QEMU copies everything on the console into the container's log, which `ec2:GetConsoleOutput` returns. A
user with only that permission can read what was shown in someone else's terminal session, as with AWS's
serial console. Do not display secrets in the browser terminal; use SSH or run-command for that.

**10. Spoofed console markers.** HomeCloud reads cloud-init's completion line and the entrypoint's
`[homecloud]` messages from the same log the guest writes to. A guest can print them and so be reported
running early or switched to user-mode networking. The effect is limited to the guest's own instance; the
signals that act on other state (passt's death) come from files in the container, not from the log.

**11. Quotas.** Root and extra disks are sparse files of up to 16 TiB on the Docker host's disk, and
nothing limits the number of instances a user may launch, as for EBS volumes and container instances.
Size the host for its users and watch its free space.

**12. Guest network reach.** With passt or user-mode networking, the guest's connections leave from the
VM container's network namespace, so they pass the same security group egress rules and reach the same
places as a container instance: the VPC, published services and the Docker host's own addresses (see
accepted risk 21 of the October audit). `169.254.169.254` is routed to HomeCloud's metadata service in the
container right after it starts, before the guest has a network.

## Reviewed, not an issue

- **QMP, guest agent and serial sockets.** They are Unix sockets in the VM container's private `/run`, on
  no network. The guest only sees its end of the virtio-serial port; other containers cannot reach them;
  HomeCloud reaches them by `docker exec`. QMP is only driven with fixed commands (`hc-vm-ctl`).
- **Bind mounts, Docker socket, privileges.** VM containers get named volumes only (the image cache and
  an AMI's disk read-only), no bind mount of a host path, no Docker socket, no added capability, no host
  network or PID namespace, and only `/dev/kvm` as a device.
- **Image cache.** It is mounted read-write only into the download helpers. Images come over HTTPS and are
  checked against SHA-256/512 digests compiled into HomeCloud before first use and again before every
  launch and start, so a redirect or mirror cannot change them. The guest agent packages come from the
  distribution image's own signed apt repositories.
- **passt forwarding.** passt forwards every port, but only what the container's iptables INPUT chain
  (the security groups) admits arrives; user-mode networking forwards only the ports the groups allowed.
  Traffic from the guest to its gateway reaches the VM container's loopback, where only passt's own
  forwards listen.
- **Run-command.** Needs `ssm:SendCommand` on the instance. The command is one JSON-encoded argument of
  `/bin/sh -c`, so nothing is interpolated; timeouts are enforced in the guest and in the server.
- **Serial terminal.** Needs `ec2-instance-connect:SendSSHPublicKey` on the instance and gives a login
  prompt (cloud images have no passwords). QEMU serves one client at a time.
- **Cloud-init seed.** meta-data values are JSON-quoted (valid YAML), vendor-data is a constant, user data
  is the launcher's own, and instance IDs and host names are generated by HomeCloud.
- **Path handling.** Volume, instance and image IDs are generated; device names are validated; the
  entrypoint builds paths from volume IDs only.
- **AMIs and backups.** `VMDisk` is only set by `CreateImage`, from a flattened disk without a backing
  file. Backup flattening uses the same checked helper; restoring an archive is an administrator action.
- **Resources.** The container's memory limit is the guest's memory plus 768 MiB and its CPU quota the
  vCPUs plus 1.5; the QEMU arguments come from the instance type catalog.

## Reported outside the VM code (not changed)

Container instances also log with the daemon's default (unbounded) configuration and keep Docker's
default capabilities, including `NET_RAW`. Neither is new, and both could reuse `RunSpec.LogMaxBytes` and a
capability drop list.
