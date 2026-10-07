# Install HomeCloud on a server

This guide takes you from a fresh VPS or home server to a HomeCloud you can reach over HTTPS, that
starts at boot, is backed up and can be upgraded. It targets **Ubuntu 24.04** and **Debian 12** on
**amd64** and **arm64**; other systemd-based distributions work the same way once Docker is installed.

Everything below uses commands and flags that exist in the `homecloud` binary
(`homecloud <command> --help` lists them). For a laptop install, the [quick start](../README.md#-quick-start)
is enough.

## Contents

1. [What you need](#1-what-you-need)
2. [Install Docker](#2-install-docker)
3. [Install HomeCloud](#3-install-homecloud)
4. [First start](#4-first-start)
5. [Run it as a service](#5-run-it-as-a-service)
6. [Expose it safely](#6-expose-it-safely)
7. [Firewall](#7-firewall)
8. [DNS](#8-dns)
9. [Use it from your laptop, the AWS CLI and Terraform](#9-use-it-from-your-laptop-the-aws-cli-and-terraform)
10. [Backups and restore](#10-backups-and-restore)
11. [Upgrades](#11-upgrades)
12. [KVM and VM instances](#12-kvm-and-vm-instances)
13. [Security notes](#13-security-notes)
14. [Troubleshooting](#14-troubleshooting)

## 1. What you need

HomeCloud is one Go binary. Every service it offers (instances, S3, databases, functions, queues, ...)
runs as a container on the Docker Engine of the same machine, so the machine's size is the size of
everything you run in it.

| | Minimum to try it | Comfortable |
| --- | --- | --- |
| CPU | 2 vCPUs | 4+ vCPUs |
| Memory | 4 GB | 8-16 GB |
| Disk | 30 GB | 100 GB+ SSD (images, volumes, S3 objects and database data all live in Docker) |
| Architecture | amd64 or arm64 | |

These are recommendations, not limits the software enforces. The only checks are in `homecloud doctor`,
which fails when Docker reports less than 2 GB of memory and advises at least 4 GB for databases and
functions. The smallest instance type, `t3.nano`, is 0.5 vCPU and 512 MB; `r5.large` is 2 vCPUs and
16 GB. Add up the instances, databases and function environments you plan to run.

Other requirements:

- A 64-bit Linux with systemd, and `curl`.
- Outbound internet access. HomeCloud pulls container images (MinIO for S3, database engines, the
  AWS Lambda runtime images, OS images for instances) and, on first use, builds a small helper image
  used to enforce security groups.
- A DNS name pointing at the server if you want HTTPS with a real certificate (see [DNS](#8-dns)).
- One HomeCloud installation per Docker host: the server refuses to start against containers that
  belong to another HomeCloud account.

## 2. Install Docker

HomeCloud needs Docker Engine (not Podman). Install it from Docker's package repository, or use the
convenience script that the HomeCloud installer itself suggests:

```bash
curl -fsSL https://get.docker.com | sudo sh
```

To follow the repository route instead, see Docker's guides for
[Ubuntu](https://docs.docker.com/engine/install/ubuntu/) and
[Debian](https://docs.docker.com/engine/install/debian/).

Let your normal user talk to Docker (log out and back in afterwards, or run `newgrp docker`):

```bash
sudo usermod -aG docker "$USER"
sudo systemctl enable --now docker
docker run --rm hello-world
```

Run HomeCloud as this normal user rather than as root: the data directory is created under the user's
home (`~/.homecloud`) and the service (below) runs as that user.

## 3. Install HomeCloud

```bash
curl -fsSL https://homecloud.pages.dev/scripts/install.sh | sh
homecloud version
```

The script detects `linux` and `amd64`/`arm64`, downloads the latest release from GitHub, verifies its
SHA-256 checksum, and installs `/usr/local/bin/homecloud` (using `sudo` when that directory is not
writable). Without Docker it still installs the CLI, and says that the server needs Docker. Two
environment variables change its behavior:

```bash
# pin a release, or install somewhere else
curl -fsSL https://homecloud.pages.dev/scripts/install.sh | HOMECLOUD_VERSION=v0.3.0 INSTALL_DIR="$HOME/.local/bin" sh
```

Then check the machine:

```bash
homecloud doctor
```

At this point it reports the server as not running, which is expected; the Docker section should be
green.

### Docker

Instead of the binary, you can run the official image, `ghcr.io/solinode/homecloud` (linux/amd64 and
linux/arm64; tags `latest` and one per release, e.g. `0.4.0`). It runs `homecloud serve` and manages
the host's Docker through the mounted socket, so the services it starts (MinIO, functions, instances,
databases) are containers next to it on the same host:

```bash
docker run -d --name homecloud --restart unless-stopped \
  -p 127.0.0.1:8080:8080 \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v homecloud-data:/data \
  ghcr.io/solinode/homecloud
docker logs homecloud                     # first start: the root console password, shown once
eval "$(docker exec homecloud homecloud aws-env)"   # AWS CLI and SDKs on this machine
docker exec homecloud homecloud ec2 ls    # the homecloud CLI, inside the container
```

What to know:

- **The socket is root on the host.** Whoever controls the container controls the Docker host, exactly
  like the `homecloud` binary run by a member of the `docker` group. The entrypoint runs the server as
  the unprivileged `homecloud` user (uid 10001), added to the group that owns the socket; set
  `HOMECLOUD_RUN_AS_ROOT=1` to keep root.
- **Data** lives in `/data` (the `homecloud-data` volume above, or a bind mount). It holds the state,
  the credentials file and `config.json`; flags you pass after the image name (`... ghcr.io/solinode/homecloud
  serve --public-url https://cloud.example.com`) are saved there as with the binary. The services' own data
  is in Docker volumes on the host, as usual.
- **Address.** Inside the container the API listens on `0.0.0.0:8080` (`HOMECLOUD_ADDR`), and the `-p`
  flag decides who reaches it. Publish on `127.0.0.1` unless other machines need it; to expose it, put
  a reverse proxy in front ([section 6](#6-expose-it-safely)) and set `--public-url`, or publish
  `0.0.0.0:8080:8080` with `--tls-self-signed`. If you publish on another host port
  (`-p 127.0.0.1:9090:8080`), pass `--public-url http://localhost:9090` so links HomeCloud generates
  (API Gateway, function URLs, queue URLs) point at it.
- **Workloads reach the API** at the HomeCloud container's own address: it joins every VPC network at a
  reserved address (the third-to-last of the VPC range) and the default bridge, and functions, tasks and
  instances get `host.docker.internal` mapped to it. Nothing extra needs publishing. HomeCloud reaches
  MinIO, the registry, the DNS server and function environments directly over those networks.
- **Published ports** of instances, load balancers, MinIO (`127.0.0.1:9500`), the registry
  (`localhost:5500`) and DNS (`127.0.0.1:8053`) are on the host, as with the binary.
- **Host networking** (`--network host`) also works: HomeCloud then behaves exactly like the binary on the
  host. Pass `serve --addr 127.0.0.1:8080` there unless you want the API on every host address.
- **One installation per Docker host.** Like the binary, the container refuses to start when the host
  runs another installation's containers.
- **Upgrade:** `docker pull ghcr.io/solinode/homecloud`, then recreate the container with the same
  volume; data is migrated at start.
- **Backup and restore** ([section 10](#10-backups-and-restore)) stream through stdin and stdout. The
  backup includes the Docker volumes (S3 objects, databases, registry):

  ```bash
  docker exec homecloud homecloud backup -o - > homecloud-backup.tar.gz
  # restore with the HomeCloud container stopped; --force moves the existing
  # data directory aside (to /data.before-restore-<time>) and replaces volumes:
  docker stop homecloud
  docker run -i --rm -v /var/run/docker.sock:/var/run/docker.sock -v homecloud-data:/data \
    ghcr.io/solinode/homecloud restore --force - < homecloud-backup.tar.gz
  docker start homecloud
  ```
- The `homecloud service` and `homecloud upgrade` commands are for the binary; with the image, Docker's
  restart policy and image tags do their jobs.

## 4. First start

Start the server once in the foreground, with the settings you want to keep. Settings passed as flags
are saved to `<data-dir>/config.json` and reused on every later start (including by the service), so you
only pass them once. The ones you need on a server are `--public-url` (when the API is reached through
a reverse proxy or on a non-default port), `--public-host` and, when you are not putting a reverse proxy
in front, `--addr`:

```bash
homecloud serve --public-url https://cloud.example.com
```

`--public-url` is the base URL clients reach the API at. Every link HomeCloud generates for clients
(API Gateway endpoints, Lambda function URLs, queue URLs, static website URLs, Cognito issuers,
presigned S3 URLs) starts with it, so they point at your proxy on 443 rather than at the API's own port.
Without it they are built as `<scheme>://<public-host>:<api port>`, with `https` when `--tls-cert` or
`--tls-self-signed` is used. Containers you run keep using the internal address for `AWS_ENDPOINT_URL`.

`--public-host` is the name clients use to reach what HomeCloud publishes on host ports: published
instance and database ports, load balancer ports, the DNS name server. It defaults to `localhost`, which is
wrong for any remote use; when `--public-url` is set and `--public-host` is not, the host name of the URL
is used. Use a DNS name (or the server's IP address).

On the very first start HomeCloud creates the account and prints, once:

```
first start: created account 123456789012
  console sign-in   user: root   password: ...
  CLI credentials written to /home/you/.homecloud/credentials
  (the password is shown only once; reset it with `homecloud serve --reset-root-password`, or choose one with `homecloud admin set-root-password` while the server is stopped)
```

Copy the password now, or set one you prefer (see [Security notes](#13-security-notes)). The CLI
credentials (an access key for the `root` user) are written to `~/.homecloud/credentials` (mode 0600).
When the server listens on a wildcard address such as `0.0.0.0:8080`, the file records `127.0.0.1` (or
the `--public-url`) as the endpoint. The data directory defaults to `~/.homecloud`; override it with
`--data-dir` or the `HOMECLOUD_DATA_DIR` environment variable.

By default the API and console listen on `127.0.0.1:8080` only, so nothing is reachable from outside
yet. You can check from the server:

```bash
curl http://127.0.0.1:8080/api/v1/health
```

Press Ctrl-C to stop it, then continue with the service. (If you forget the password, see
[Troubleshooting](#14-troubleshooting).)

## 5. Run it as a service

Install a systemd system unit. Run it with `sudo` from your normal user; under `sudo` the data directory
defaults to the invoking user's `~/.homecloud` (the one you just initialized), not root's. If that user's
home cannot be found the command stops and asks for `--data-dir`.

```bash
sudo homecloud service install --system
```

This writes `/etc/systemd/system/homecloud.service`, runs `systemctl daemon-reload`, and
`enable --now`. The unit starts at boot after `docker.service`, restarts on failure, and runs as the
user who invoked `sudo` (taken from `SUDO_USER`; that user must be in the `docker` group). It runs
`homecloud serve --data-dir <dir>`; every other setting is read from `config.json`. Use `--dry-run`
first to see the unit without installing it.

```bash
homecloud service status                 # or: systemctl status homecloud
journalctl -u homecloud -f               # logs
sudo systemctl restart homecloud
sudo homecloud service uninstall         # removes the unit, keeps your data
```

If you ever need to change a setting, run `homecloud serve` with the new flag once (with the service
stopped) and start the service again, or edit `config.json` while it is stopped.

Without `--system`, `homecloud service install` creates a per-user unit that starts when you log in;
on a server you would also need `sudo loginctl enable-linger $USER`. The system unit is the better fit.

## 6. Expose it safely

The API and console speak plain HTTP on `127.0.0.1:8080` by default. **Do not simply bind it to
`0.0.0.0` over the open internet without TLS**: sign-in tokens and AWS access keys would travel in the
clear. Choose one of these.

### Option A: a reverse proxy with automatic HTTPS (recommended on a VPS)

Keep HomeCloud on `127.0.0.1:8080` and let [Caddy](https://caddyserver.com) obtain and renew a Let's
Encrypt certificate. Install Caddy from its
[official package repository](https://caddyserver.com/docs/install#debian-ubuntu-raspbian), then put this
in `/etc/caddy/Caddyfile`:

```
cloud.example.com {
    reverse_proxy 127.0.0.1:8080
}
```

```bash
sudo systemctl reload caddy
```

Ports to forward or open to the internet for this setup:

- **80/tcp** and **443/tcp** to Caddy (port 80 is needed for the certificate challenge and redirect).
- Nothing else for the console and API. HomeCloud's API stays bound to loopback, so port 8080 is not
  reachable from outside.

Your DNS name must already point at the server. Tell HomeCloud where clients reach it and that it may
believe the proxy's forwarding headers (Caddy runs on the same machine, so it connects from loopback):

```bash
homecloud serve --public-url https://cloud.example.com --trusted-proxies 127.0.0.1/32
```

Containers you run (functions, instances, tasks) keep reaching the API directly over plain HTTP on the
Docker bridge, which is unaffected by the proxy.

What the two settings do when a proxy terminates TLS:

- `--public-url` makes every generated link (function URLs, static website URLs, Cognito issuers, API
  Gateway endpoints, queue URLs, presigned S3 URLs) use `https://cloud.example.com` instead of
  `<public-host>:8080`. Function URLs follow the URL's scheme.
- `--trusted-proxies` takes a comma-separated list of CIDRs (default: none). When a request comes from
  one of them, the client address is taken from `X-Forwarded-For` (the rightmost entry that is not itself
  a trusted proxy, so entries a client forges on the left are ignored) and the scheme from
  `X-Forwarded-Proto`. IAM conditions on `aws:SourceIp` and `aws:SecureTransport`, the audit trail and
  the sign-in throttle then see the real client. Trust only proxies you control; without the flag the
  headers are ignored ([details](aws-compat.md#policies-and-authorization)).

### Option B: HomeCloud's built-in TLS

`serve` can terminate TLS itself, either with your own certificate or with a generated self-signed one.
Both need the address to listen on:

```bash
# your own PEM certificate and key (e.g. from certbot)
homecloud serve --addr 0.0.0.0:8443 --public-host cloud.example.com \
  --tls-cert /etc/letsencrypt/live/cloud.example.com/fullchain.pem \
  --tls-key  /etc/letsencrypt/live/cloud.example.com/privkey.pem

# or a self-signed certificate (good for a home LAN or Tailscale)
homecloud serve --addr 0.0.0.0:8443 --public-host homelab.tailnet.ts.net --tls-self-signed
```

- `--tls-cert` and `--tls-key` must be given together, and the service user must be able to read both
  files. The certificate is loaded when the server starts, so restart the service after a renewal.
- `--tls-self-signed` creates `<data-dir>/tls/cert.pem` and `key.pem` once (valid for 5 years) for
  `localhost`, `127.0.0.1` and the `--public-host` value. Pass `--public-host` in the same command that
  first creates it; if you change the host later, delete `<data-dir>/tls` and restart.
- With a self-signed certificate created at the first start, the CLI credentials record the certificate
  as `ca_file`, so `homecloud` and `homecloud aws-env` (which exports `AWS_CA_BUNDLE`) trust it on this
  machine. Browsers will warn until you trust the certificate.
- Ports below 1024 need extra privileges for an unprivileged user, so use a high port such as 8443, or
  use Option A for 443.
- With TLS on, functions, tasks and instances do not call the HTTPS endpoint (a certificate for your
  public name cannot cover `host.docker.internal`). HomeCloud opens a second, plain-HTTP listener for
  them on a random port, bound only to the Docker bridge gateway on Linux (an address that exists only
  inside the machine) or to loopback on Docker Desktop and OrbStack, and gives workloads that address as
  `AWS_ENDPOINT_URL` (`http://host.docker.internal:<port>`). Nothing outside the machine can reach it.
  Requests from workloads therefore have `aws:SecureTransport` false, like any request over plain HTTP.
- Set `--public-url` too when clients reach the server at a name or port other than
  `<public-host>:<api port>`.

Open the chosen port (8443 above) in your firewall. The rest of the flow is the same as for Option A.

### Tailscale or a LAN

On a private network you can skip the internet altogether: bind to the LAN or Tailscale address (for
example `--addr 0.0.0.0:8080 --public-host homelab.tailnet.ts.net`) and, if you like, add
`--tls-self-signed`. Only devices on that network can reach it.

## 7. Firewall

Two things are exposed on a HomeCloud host: the front door (the API and console) and whatever HomeCloud
publishes on host ports.

| Port | What | Bound to |
| --- | --- | --- |
| 22/tcp | SSH (yours) | |
| 80, 443/tcp | Caddy, if you use Option A | all interfaces |
| 8080/tcp (`--addr`) | API, console and AWS endpoint | `127.0.0.1` by default |
| 9500/tcp (`--s3-port`) | S3 endpoint (MinIO) | `127.0.0.1` (`--s3-bind`) |
| 9501/tcp (`--s3-console-port`) | MinIO console | `127.0.0.1` (`--s3-bind`) |
| 8053/udp and tcp (`--dns-port`) | DNS for public Route 53 zones | `127.0.0.1` (`--dns-bind`) |
| 5500/tcp | Container registry (ECR) | `127.0.0.1` only |
| dynamic | ports of instances, load balancers and public databases that security groups open | all interfaces |

The API's S3 operations already go through the main endpoint; the separate S3 and MinIO console ports
are not needed by the AWS CLI, the SDKs or the console (presigned URLs made by the console are served
through the API too, under `/_s3/`). Because Docker publishes ports around `ufw`, HomeCloud publishes
MinIO, its console and the DNS server on `127.0.0.1` by default. To expose them, pass
`--s3-bind 0.0.0.0` (or one address) and `--dns-bind 0.0.0.0`; both are saved to `config.json`. Existing
installations get their MinIO and DNS containers recreated with the new binding at the next start
(their data lives in volumes and the zone records in the state file).

**Docker and ufw.** Docker publishes container ports by editing iptables directly, so `ufw` rules do
not filter them. `ufw` is still right for everything that is not a Docker-published port:

```bash
sudo ufw default deny incoming
sudo ufw default allow outgoing
sudo ufw allow OpenSSH
sudo ufw allow 80,443/tcp      # only with Caddy (Option A)
sudo ufw enable
```

If you did expose 9500, 9501 or 8053 with `--s3-bind` / `--dns-bind` and want them limited, the
simplest and most reliable place is your provider's network firewall (security group). On the host
itself, drop them in Docker's `DOCKER-USER` chain (replace `eth0` with your public interface, and
repeat per port; add `-p udp` for 8053). The same applies to the ports of instances, load balancers and
public databases, which Docker publishes on all interfaces:

```bash
sudo iptables -I DOCKER-USER -i eth0 -p tcp -m conntrack --ctorigdstport 9501 --ctdir ORIGINAL -j DROP
```

Those rules are not persistent across reboots by themselves; use `iptables-persistent` or your
provider's firewall. Then use HomeCloud's own security groups to decide which instance and database
ports are published (see [architecture](architecture.md#security-notes)).

## 8. DNS

- **For the console and API:** create an `A` (and `AAAA`) record for `cloud.example.com` pointing at
  the server. That name is what you give Caddy, `--public-host` and the AWS endpoint.
- **For Route 53 hosted zones inside HomeCloud:** public zones are answered on HomeCloud's DNS port
  (`--dns-port`, default 8053, UDP and TCP), and inside your VPCs by each VPC's resolver. The port is
  published on `127.0.0.1` unless you pass `--dns-bind`: to serve other machines, use
  `--dns-bind 0.0.0.0` (or the server's public IP). You can then test a zone from another machine with
  `dig @<server-ip> -p 8053 www.example.test`.
- **Real delegation** needs port 53, because resolvers on the internet query only that port:

  ```bash
  homecloud serve --dns-port 53 --dns-bind <server public IP>
  ```

  On Ubuntu and Debian `systemd-resolved` listens on `127.0.0.53:53`, which blocks a wildcard bind, so
  bind to the public address as above, or turn off its stub listener (`DNSStubListener=no` in
  `resolved.conf`, then restart `systemd-resolved`). If something already holds the port, HomeCloud
  refuses to start the DNS server and says which port and what to change. Docker's daemon (root)
  publishes the port, so the HomeCloud service does not need to run as root. Then create the NS and glue
  records for your domain at your registrar pointing at the server, and open 53/udp and 53/tcp.
- Instance private DNS names (`ip-10-88-0-4.internal`, `<db>.rds.internal`) resolve only inside the VPC.

## 9. Use it from your laptop, the AWS CLI and Terraform

The same endpoint serves the console, the native API and the AWS wire protocols. You need an access
key: the one in `~/.homecloud/credentials` on the server belongs to `root`; for everyday use, create a
user (see [Security notes](#13-security-notes)).

**With the `homecloud` CLI on your laptop** (install it with the same install script):

```bash
homecloud configure --endpoint https://cloud.example.com   # prompts for the access key ID and secret
# a server with a self-signed certificate: copy its <data-dir>/tls/cert.pem here first
homecloud configure --endpoint https://homelab:8443 --ca-file ./homelab-cert.pem
homecloud whoami
eval "$(homecloud aws-env)"        # prints export lines; add --fish for fish syntax
aws s3 ls
```

`homecloud aws-env` prints `AWS_ENDPOINT_URL`, `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` and
`AWS_REGION` (and `AWS_CA_BUNDLE` when the credentials name a CA file). `homecloud configure` keeps
the region and `ca_file` already in the credentials file unless you pass `--region` or `--ca-file`. The environment variables
`HOMECLOUD_ENDPOINT`, `HOMECLOUD_ACCESS_KEY_ID` and `HOMECLOUD_SECRET_ACCESS_KEY` override the
credentials file.

**Without it**, set the variables yourself:

```bash
export AWS_ENDPOINT_URL=https://cloud.example.com
export AWS_ACCESS_KEY_ID=HCIA...
export AWS_SECRET_ACCESS_KEY=...
export AWS_REGION=us-east-1
aws sts get-caller-identity
```

**Terraform and OpenTofu** use the standard `hashicorp/aws` provider with its `endpoints {}` block
pointing at HomeCloud; the credentials come from the same environment variables. Use a host name, not an
IP address, in the endpoint. See [`examples/terraform/shop`](../examples/terraform/shop/README.md) and
[aws-compat.md](aws-compat.md) for the supported services.

## 10. Backups and restore

`homecloud backup` downloads a backup from the running server through the API, to the machine where you
run it (the server itself, or your laptop after `homecloud configure`). It contains the data directory
(state, `master.key`, function code, logs) and every HomeCloud Docker volume: databases, S3 objects,
registry images, EBS volumes and file systems. Containers are paused briefly while their volume is
copied. Instance root disks are not included, so keep data that matters on volumes.

```bash
homecloud backup -o /var/backups/homecloud-$(date +%F).tar.gz
homecloud backup --no-volumes -o state-only.tar.gz     # data directory only
```

The archive holds the master key and credentials: store it encrypted or somewhere private, and copy it
off the server. A nightly cron entry for the user that owns the credentials works (`%` must be escaped
in crontab):

```
0 3 * * * homecloud backup -o /var/backups/homecloud-$(date +\%F).tar.gz
```

HomeCloud does not delete old backups; add your own rotation.

**Restore** (on the same or a new server; Docker and the binary must be installed first):

```bash
sudo systemctl stop homecloud
homecloud restore homecloud-backup.tar.gz --data-dir "$HOME/.homecloud"
sudo systemctl start homecloud
```

`restore` refuses to run while a server answers at the address saved in the data directory's
`config.json`. It unpacks the data directory and recreates the Docker volumes. Use `--force` to move an
existing data directory aside (to `<data-dir>.before-restore-<time>`) and replace existing volumes, and
`--no-volumes` to restore only the data directory. On a new machine, run `homecloud serve --public-host
<name>` once if the host name changed, then reinstall the service (`sudo homecloud service install
--system --data-dir ...`).

## 11. Upgrades

```bash
homecloud upgrade --check            # is a newer release available?
homecloud backup -o before-upgrade.tar.gz
sudo homecloud upgrade               # verified download from GitHub releases; needs sudo for /usr/local/bin
sudo systemctl restart homecloud
homecloud version
homecloud doctor
```

`upgrade` verifies the release's SHA-256 checksum before replacing the binary; `--version v0.3.0`
installs a specific release. Data is migrated automatically when the new version starts. After an
upgrade, the first start may need network access to rebuild the security-group helper image.

## 12. KVM and VM instances

The VM images (`ami-ubuntu-24-04-vm`, `ami-debian-12-vm`) boot a real virtual machine under QEMU, inside a
container on the instance's VPC network. Container images need nothing of this.

- **KVM.** With `/dev/kvm` on the Docker host, guests run hardware-accelerated; without it they are
  emulated (works, but boots take minutes). Prefer bare metal, a home server, or a VPS with nested
  virtualization; check with `ls -l /dev/kvm` (`sudo apt install cpu-checker && kvm-ok` tells whether the
  CPU and firmware support it). Docker Desktop and OrbStack on macOS run guests emulated.
- **Networking.** passt gives the guest the instance's private address. Where passt cannot sandbox
  itself, notably Ubuntu 24.04 hosts (`kernel.apparmor_restrict_unprivileged_userns=1`), guests fall back
  to QEMU user-mode networking: NAT, no private address of their own, only the ports the security groups
  allow are forwarded. The instance reports `vm_network: user` and the server log says why
  ([#90](https://github.com/solinode/homecloud/issues/90)).
- **Disk.** Cloud images (about 600 MB each) are downloaded on first use into the `hc-vm-images` volume.

## 13. Security notes

- **HomeCloud is root on the host.** It controls Docker through its socket, which is root-equivalent.
  Treat everyone with HomeCloud administrator access as an administrator of the server, and do not run
  untrusted workloads with the expectation that the container boundary is a VM boundary.
- **Change the initial `root` password** (the one printed at first start) and keep the root access
  key out of daily use. With the service stopped, either choose one
  (`homecloud admin set-root-password`, which asks twice without echo, or reads one line from a pipe:
  `printf '%s\n' "$PW" | homecloud admin set-root-password`; it is never taken as an argument) or generate a
  random one with `homecloud serve --reset-root-password`. You can also manage the user's password on
  its IAM page in the console.
- **Create IAM users instead of using the root keys.** For example, an administrator for yourself and
  a limited user for a CI job:

  ```bash
  homecloud iam create-user alice --password 'a-long-passphrase' --policy AdministratorAccess
  homecloud iam create-access-key alice          # the secret is shown once
  homecloud iam create-user ci --policy S3ReadOnlyAccess
  homecloud iam create-access-key ci
  ```

  Then run `homecloud configure` with the new key on your laptop. `~/.homecloud/credentials` on the
  server holds the root key: keep it mode 0600 and do not copy it around.
- **Keep `master.key` safe.** `<data-dir>/master.key` encrypts secrets, SecureStrings, KMS key material
  and private CA keys. Losing it makes that data unreadable; leaking it (it is inside every backup)
  exposes it. Back it up with the rest, and protect the backups.
- **Always use TLS** for anything that leaves a trusted network (see [Expose it safely](#6-expose-it-safely)).
  Console sign-in is throttled after 10 failures per client IP in 5 minutes, but that is no substitute.
- Keep the API on loopback behind a proxy where you can, and firewall the ports HomeCloud publishes
  ([Firewall](#7-firewall)).
- **Outbound requests made for users** (SNS and alarm webhooks, API Gateway `HTTP_PROXY` integrations)
  never reach loopback, link-local (including the provider's `169.254.169.254` metadata service), this
  host's own addresses or other metadata endpoints, checked when the connection is made. Private LAN
  ranges stay reachable because homelabs call their own services; on a multi-user VPS set
  `HOMECLOUD_DENY_PRIVATE_TARGETS=1` in the service environment to block RFC 1918 and unique-local
  addresses too.
- **`homecloud:CreateBackup` is account-root.** A backup contains `master.key` and everything it
  decrypts, so grant that action (and `homecloud:*`) only to administrators, and encrypt backups before
  they leave the machine.
- Keep the host patched (`unattended-upgrades` on Ubuntu and Debian) and upgrade HomeCloud regularly.

## 14. Troubleshooting

Start with:

```bash
homecloud doctor
```

It checks that Docker is reachable (and warns about low memory), that CLI credentials exist and are
accepted, that the server answers at the address in `config.json`, and that the S3, MinIO console and
registry ports are free or owned by HomeCloud. Each failure prints a suggested fix. If you use a
non-default data directory, run it with `HOMECLOUD_DATA_DIR` set to that directory so it reads the
right `config.json` and credentials.

| Symptom | What to check |
| --- | --- |
| `permission denied` on `/var/run/docker.sock` | The user running HomeCloud is not in the `docker` group (`sudo usermod -aG docker <user>`, then restart the service). A system unit runs as the `sudo` user that installed it. |
| `doctor` says credentials are missing | Run `homecloud serve` once, or `homecloud configure` to point at a remote server. |
| Service does not start | `systemctl status homecloud` and `journalctl -u homecloud -n 100`. Confirm `docker.service` is running. |
| `port ... is in use` | Another program uses the S3 (9500), MinIO console (9501) or DNS (8053) port. Pick others with `--s3-port`, `--s3-console-port`, `--dns-port` (saved to `config.json`). For port 53 see [DNS](#8-dns). |
| `this Docker host already runs HomeCloud account ...` | Start with the `--data-dir` of that installation, or remove its containers first. One Docker host runs one HomeCloud. |
| Lost the root password | Stop the service, then run `homecloud admin set-root-password` (pick one) or `homecloud serve --reset-root-password` (random; copy it, stop with Ctrl-C), and start the service. |
| Browser cannot connect from outside | Confirm the proxy/port is open in the firewall and provider security group, and that `curl -I https://cloud.example.com/api/v1/health` works from your machine. |
| CLI or AWS CLI reports a certificate error | Use a real certificate (Caddy, or `--tls-cert`), or for a self-signed one copy the server's `<data-dir>/tls/cert.pem` to the client and run `homecloud configure --ca-file <file>` (AWS tools: `AWS_CA_BUNDLE`). |
| First instance or database is slow to start | Images are pulled and the security-group helper image is built on first use; this needs internet access. |
| `restore` says a server is running | Stop the service first (`sudo systemctl stop homecloud`). |

If none of that explains it, the server log (`journalctl -u homecloud`) is the place to look; open an
issue at <https://github.com/solinode/homecloud/issues> and include the output of `homecloud doctor`.
