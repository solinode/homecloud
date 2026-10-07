package server

import (
	"errors"
	"net"
	"net/http"
	"time"
)

// workloadListenAddr picks where the plain-HTTP endpoint for workloads
// (containers of functions, tasks and instances) listens when the API itself
// serves TLS: workloads call https://host.docker.internal, a name no public
// certificate covers, so with built-in TLS they get their own HTTP endpoint
// that is not reachable from outside the machine.
//
//   - Linux: the Docker bridge gateway, the address host.docker.internal maps
//     to. It exists only inside the host; nothing outside can route to it.
//   - Docker Desktop and OrbStack (macOS, Windows): host.docker.internal
//     reaches the host's loopback.
//   - HomeCloud in a container (the official image): every address of the
//     container. Only the API port is published; this one is reached only
//     over the Docker networks HomeCloud shares with its workloads.
//
// The port is chosen by the kernel. ok is false when no safe address exists.
func workloadListenAddr(goos, bridgeGateway string, containerized bool) (addr string, ok bool) {
	if containerized {
		return ":0", true
	}
	if goos == "linux" {
		if bridgeGateway == "" {
			return "", false
		}
		return net.JoinHostPort(bridgeGateway, "0"), true
	}
	return "127.0.0.1:0", true
}

// serveWorkloads serves h on l until the server is closed; the returned
// function stops it.
func serveWorkloads(l net.Listener, h http.Handler, logf func(string, ...any)) (stop func()) {
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute, MaxHeaderBytes: 256 << 10}
	go func() {
		if err := srv.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logf("workload endpoint %s: %v", l.Addr(), err)
		}
	}()
	return func() { _ = srv.Close() }
}
