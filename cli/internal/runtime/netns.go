package runtime

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	docker "github.com/fsouza/go-dockerclient"
	"github.com/homecloudhq/homecloud/cli/internal/core"
)

// BuildImage builds tag from a single Dockerfile unless the image already exists.
func (d *Docker) BuildImage(ctx context.Context, tag, dockerfile string) error {
	if _, err := d.C.InspectImage(tag); err == nil {
		return nil
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: "Dockerfile", Mode: 0o644, Size: int64(len(dockerfile)), ModTime: time.Now()}); err != nil {
		return err
	}
	if _, err := tw.Write([]byte(dockerfile)); err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	var out bytes.Buffer
	err := d.C.BuildImage(docker.BuildImageOptions{Name: tag, InputStream: &buf, OutputStream: &out, Context: ctx,
		RmTmpContainer: true, SuppressOutput: true, Labels: map[string]string{core.LabelManaged: "true"}})
	if err != nil {
		return fmt.Errorf("build %s: %w: %s", tag, err, strings.TrimSpace(out.String()))
	}
	return nil
}

// RunInNetns runs a short-lived container in the network namespace of a
// running container, with NET_ADMIN, after copying files (path -> content)
// into it under /. It returns the container's output; a non-zero exit is an
// error. This is how per-container firewall rules and routes are programmed
// from outside: it works the same on Linux, Docker Desktop and OrbStack and
// gives the target itself no extra capability.
func (d *Docker) RunInNetns(ctx context.Context, target, image string, cmd []string, files map[string][]byte) (string, error) {
	c, err := d.C.CreateContainer(docker.CreateContainerOptions{Context: ctx,
		Config:     &docker.Config{Image: image, Cmd: cmd, Labels: Labels("vpc", "sgfw", nil)},
		HostConfig: &docker.HostConfig{NetworkMode: "container:" + target, CapAdd: []string{"NET_ADMIN"}},
	})
	if err != nil {
		return "", err
	}
	defer func() { _ = d.Remove(c.ID) }()
	if len(files) > 0 {
		if err := d.CopyIn(ctx, c.ID, "/", files, 0o644); err != nil {
			return "", err
		}
	}
	if err := d.C.StartContainerWithContext(c.ID, nil, ctx); err != nil {
		return "", err
	}
	code, err := d.C.WaitContainerWithContext(c.ID, ctx)
	out, _ := d.Logs(c.ID, 50, time.Time{})
	if err != nil {
		return out, err
	}
	if code != 0 {
		return out, fmt.Errorf("exit %d: %s", code, strings.TrimSpace(out))
	}
	return out, nil
}
