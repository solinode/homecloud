"""Unit tests that need no running container."""

from unittest import mock

from testcontainers_homecloud import HomeCloudContainer


def make(**kwargs):
    # Creating a DockerContainer connects to Docker; these tests only inspect options.
    with mock.patch("testcontainers.core.container.DockerClient"):
        return HomeCloudContainer("homecloud:test", **kwargs)


def test_caller_docker_options_are_kept():
    hc = make(mem_limit="1g", platform="linux/amd64")
    assert hc._kwargs["mem_limit"] == "1g"
    assert hc._kwargs["platform"] == "linux/amd64"
    assert hc._kwargs["network_mode"] == "host"


def test_host_network_cannot_be_overridden():
    assert make(network_mode="bridge")._kwargs["network_mode"] == "host"


def test_command_and_socket():
    hc = make(port=19090, docker_socket="/run/user/1000/docker.sock")
    assert hc._command == ["serve", "--data-dir", "/data", "--addr", "127.0.0.1:19090"]
    assert hc.volumes["/run/user/1000/docker.sock"]["bind"] == "/var/run/docker.sock"
