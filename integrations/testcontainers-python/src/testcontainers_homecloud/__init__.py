"""testcontainers module for HomeCloud, a self-hosted AWS.

    from testcontainers_homecloud import HomeCloudContainer

    with HomeCloudContainer() as hc:
        s3 = hc.get_client("s3")
        s3.create_bucket(Bucket="test")

HomeCloud starts each service's backing containers (MinIO for S3, Lambda runtimes,
databases) on the Docker host, so the container mounts the Docker socket and uses
host networking: it reaches those containers on the host's loopback ports and they
call it back through host.docker.internal. That works on Linux, OrbStack and Docker
Desktop with host networking enabled.

Only one HomeCloud runs per Docker host at a time (its helper containers have fixed
names and host ports): don't run tests that use it in parallel.
"""

from __future__ import annotations

import re
import shlex
import time
import urllib.request
from typing import Any, Iterable, NamedTuple, Optional

from testcontainers.core.container import DockerContainer

__all__ = ["HomeCloudContainer", "Credentials", "DEFAULT_IMAGE", "DEFAULT_PORT", "remove_resources"]

DEFAULT_IMAGE = "ghcr.io/solinode/homecloud:latest"
# Not HomeCloud's usual 8080, which is often taken on developer machines.
DEFAULT_PORT = 18080
DEFAULT_DOCKER_SOCKET = "/var/run/docker.sock"

_DATA_DIR = "/data"
_ACCOUNT_LABEL = "homecloud.account"
# Log lines of the services that start containers in the background.
_READY_LINES = {
    "s3": "s3: MinIO ready",
    "ecr": "ecr: registry ready",
    "route53": "route53: DNS ready",
}
_ACCOUNT_RE = re.compile(r"first start: created account (\d+)")


class Credentials(NamedTuple):
    access_key_id: str
    secret_access_key: str
    region: str


class HomeCloudContainer(DockerContainer):
    """A HomeCloud server for tests.

    :param image: HomeCloud image; any image whose entrypoint is the ``homecloud`` binary.
    :param port: port the API listens on, on the Docker host's 127.0.0.1.
    :param docker_socket: path of the Docker socket on the Docker host.
    :param wait_for_services: background services to wait for besides the API
        ("s3", "ecr", "route53"); every other service is ready with the API.
    :param startup_timeout: seconds to wait for all of it.
    """

    def __init__(
        self,
        image: str = DEFAULT_IMAGE,
        port: int = DEFAULT_PORT,
        docker_socket: str = DEFAULT_DOCKER_SOCKET,
        wait_for_services: Iterable[str] = ("s3",),
        startup_timeout: float = 180,
        **kwargs: Any,
    ) -> None:
        # Host networking is required (see the module docstring); every other
        # Docker option the caller passes (platform, mem_limit, ...) is kept.
        kwargs["network_mode"] = "host"
        super().__init__(image, **kwargs)
        self.port = port
        self.wait_for_services = [s.lower() for s in wait_for_services]
        self.startup_timeout = startup_timeout
        self.account_id: Optional[str] = None
        self._credentials: Optional[Credentials] = None
        self.with_command(["serve", "--data-dir", _DATA_DIR, "--addr", f"127.0.0.1:{port}"])
        self.with_env("HOMECLOUD_DATA_DIR", _DATA_DIR)
        self.with_volume_mapping(docker_socket, "/var/run/docker.sock", "rw")

    # Lifecycle

    def start(self) -> "HomeCloudContainer":
        super().start()
        deadline = time.monotonic() + self.startup_timeout
        wanted = ["listening on"] + [_READY_LINES[s] for s in self.wait_for_services if s in _READY_LINES]
        while True:
            logs = self._logs()
            m = _ACCOUNT_RE.search(logs)
            if m:  # known as early as possible, so stop() cleans up after a failed start too
                self.account_id = m.group(1)
            if all(w in logs for w in wanted):
                break
            self.reload()
            if self.status not in ("created", "running"):
                raise RuntimeError(f"HomeCloud exited during startup:\n{logs}")
            if time.monotonic() > deadline:
                raise TimeoutError(f"HomeCloud not ready after {self.startup_timeout}s (waiting for {wanted}):\n{logs}")
            time.sleep(0.5)
        self._wait_healthy(deadline)
        self._credentials = self._read_credentials()
        return self

    def stop(self, force: bool = True, delete_volume: bool = True) -> None:
        """Stop HomeCloud and remove every container, network and volume it created."""
        # Stop HomeCloud first so it does not recreate what is being removed;
        # super().stop() then only closes the Docker client.
        if self._container:
            self._container.remove(force=force, v=delete_volume)
            self._container = None
        try:
            if self.account_id:
                remove_resources(self.get_docker_client().client, self.account_id)
        finally:
            super().stop(force=force, delete_volume=delete_volume)

    # Connection details

    def get_endpoint(self) -> str:
        """The API URL, e.g. http://127.0.0.1:18080 (what AWS_ENDPOINT_URL should be)."""
        host = self.get_container_host_ip()
        if host == "localhost":
            host = "127.0.0.1"
        return f"http://{host}:{self.port}"

    def get_credentials(self) -> Credentials:
        """The root access key HomeCloud created, and its region."""
        if self._credentials is None:
            raise RuntimeError("HomeCloud is not started")
        return self._credentials

    def aws_env(self) -> dict[str, str]:
        """AWS_* environment variables for the AWS CLI, Terraform or a subprocess."""
        c = self.get_credentials()
        return {
            "AWS_ENDPOINT_URL": self.get_endpoint(),
            "AWS_ACCESS_KEY_ID": c.access_key_id,
            "AWS_SECRET_ACCESS_KEY": c.secret_access_key,
            "AWS_REGION": c.region,
            "AWS_DEFAULT_REGION": c.region,
        }

    def _session_kwargs(self) -> dict[str, Any]:
        c = self.get_credentials()
        return {
            "endpoint_url": self.get_endpoint(),
            "aws_access_key_id": c.access_key_id,
            "aws_secret_access_key": c.secret_access_key,
            "region_name": c.region,
        }

    def get_client(self, service: str, **kwargs: Any) -> Any:
        """A boto3 client for service, pointed at HomeCloud (S3 uses path-style addressing)."""
        import boto3
        from botocore.config import Config

        if service == "s3" and "config" not in kwargs:
            kwargs["config"] = Config(s3={"addressing_style": "path"})
        return boto3.client(service, **{**self._session_kwargs(), **kwargs})

    def get_resource(self, service: str, **kwargs: Any) -> Any:
        """A boto3 resource (s3, dynamodb, sqs, ...) pointed at HomeCloud."""
        import boto3
        from botocore.config import Config

        if service == "s3" and "config" not in kwargs:
            kwargs["config"] = Config(s3={"addressing_style": "path"})
        return boto3.resource(service, **{**self._session_kwargs(), **kwargs})

    # Internals

    def _logs(self) -> str:
        out, err = self.get_logs()
        return out.decode(errors="replace") + err.decode(errors="replace")

    def _wait_healthy(self, deadline: float) -> None:
        url = self.get_endpoint() + "/api/v1/health"
        last: Exception = RuntimeError("not tried")
        while time.monotonic() < deadline:
            try:
                with urllib.request.urlopen(url, timeout=2) as r:
                    if r.status == 200:
                        return
            except Exception as e:  # noqa: BLE001
                last = e
            time.sleep(0.25)
        raise TimeoutError(f"{url} not reachable (does this Docker setup support host networking?): {last}")

    def _read_credentials(self) -> Credentials:
        code, out = self.exec(["homecloud", "aws-env"])
        text = out.decode(errors="replace")
        if code != 0:
            raise RuntimeError(f"homecloud aws-env exited {code}: {text}")
        env: dict[str, str] = {}
        for line in text.splitlines():
            line = line.removeprefix("export ")
            key, sep, value = line.partition("=")
            if sep:
                env[key] = (shlex.split(value) or [""])[0]
        if not env.get("AWS_ACCESS_KEY_ID") or not env.get("AWS_SECRET_ACCESS_KEY"):
            raise RuntimeError(f"homecloud aws-env printed no credentials: {text}")
        return Credentials(env["AWS_ACCESS_KEY_ID"], env["AWS_SECRET_ACCESS_KEY"], env.get("AWS_REGION") or "us-east-1")


def remove_resources(client: Any, account_id: str) -> None:
    """Remove the containers, networks and volumes HomeCloud created for account_id.

    ``client`` is a ``docker.DockerClient``. HomeCloudContainer.stop() calls this;
    call it yourself to clean up after a test run that was killed.
    """
    flt = {"label": f"{_ACCOUNT_LABEL}={account_id}"}
    for c in client.containers.list(all=True, filters=flt):
        c.remove(force=True, v=True)
    for n in client.networks.list(filters=flt):
        n.remove()
    for v in client.volumes.list(filters=flt):
        v.remove(force=True)
