package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	docker "github.com/fsouza/go-dockerclient"
	"github.com/homecloudhq/homecloud/cli/internal/core"
)

// Info describes the Docker engine, for the system status page.
func (d *Docker) Info(ctx context.Context) (*docker.DockerInfo, error) {
	type result struct {
		info *docker.DockerInfo
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		i, err := d.C.Info()
		ch <- result{i, err}
	}()
	select {
	case r := <-ch:
		return r.info, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// ListManaged lists every container HomeCloud manages (all accounts), giving
// up when ctx ends.
func (d *Docker) ListManaged(ctx context.Context) ([]docker.APIContainers, error) {
	return d.C.ListContainers(docker.ListContainersOptions{All: true, Context: ctx,
		Filters: map[string][]string{"label": {core.LabelManaged + "=true"}}})
}

// VolumeUsage is the disk use of the managed volumes of this installation.
type VolumeUsage struct {
	Count int
	Bytes int64
}

// VolumeUsage asks Docker for the size of this account's managed volumes.
// Docker measures them by walking their files, so call it rarely.
func (d *Docker) VolumeUsage(ctx context.Context) (VolumeUsage, error) {
	u, err := url.Parse(d.C.Endpoint())
	if err != nil {
		return VolumeUsage{}, err
	}
	switch u.Scheme {
	case "unix", "npipe":
		u = &url.URL{Scheme: "http", Host: "docker"} // the client's dialer ignores the host
	case "tcp":
		u.Scheme = "http"
		if d.C.TLSConfig != nil {
			u.Scheme = "https"
		}
	}
	u.Path, u.RawQuery = "/system/df", "type=volume"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return VolumeUsage{}, err
	}
	resp, err := d.C.HTTPClient.Do(req)
	if err != nil {
		return VolumeUsage{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return VolumeUsage{}, fmt.Errorf("docker system df: %s", resp.Status)
	}
	var df struct {
		Volumes []struct {
			Labels    map[string]string
			UsageData *struct{ Size int64 }
		}
	}
	if err := json.NewDecoder(resp.Body).Decode(&df); err != nil {
		return VolumeUsage{}, err
	}
	var out VolumeUsage
	for _, v := range df.Volumes {
		if v.Labels[core.LabelManaged] != "true" || v.Labels[core.LabelAccount] != Account {
			continue
		}
		out.Count++
		if v.UsageData != nil && v.UsageData.Size > 0 {
			out.Bytes += v.UsageData.Size
		}
	}
	return out, nil
}

// Mine reports whether a managed object belongs to this installation (and not
// to another account or a running test).
func Mine(labels map[string]string) bool {
	a := labels[core.LabelAccount]
	return a == Account || (a == "" && !strings.HasPrefix(Account, core.TestAccountPrefix))
}

// pingTimeout bounds health probes of the Docker engine.
const pingTimeout = 3 * time.Second

// Ping reports whether the Docker engine answers.
func (d *Docker) Ping() error {
	ctx, cancel := context.WithTimeout(context.Background(), pingTimeout)
	defer cancel()
	return d.C.PingWithContext(ctx)
}
