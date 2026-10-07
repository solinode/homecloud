package runtime

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// RunOnce runs a short-lived container to completion and returns its output.
// A non-zero exit is an error (with the output in it). The container is removed.
func (d *Docker) RunOnce(ctx context.Context, s RunSpec) (string, error) {
	s.Start = false
	if s.Labels == nil {
		s.Labels = Labels("ec2", "helper", nil)
	}
	id, err := d.Run(ctx, s)
	if err != nil {
		return "", err
	}
	defer func() { _ = d.Remove(id) }()
	if err := d.C.StartContainerWithContext(id, nil, ctx); err != nil {
		return "", fmt.Errorf("start container: %w", err)
	}
	code, err := d.C.WaitContainerWithContext(id, ctx)
	out, _ := d.Logs(id, 200, time.Time{})
	if err != nil {
		return out, err
	}
	if code != 0 {
		return out, fmt.Errorf("exit %d: %s", code, strings.TrimSpace(out))
	}
	return out, nil
}
