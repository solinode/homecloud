// Package svc holds what every HomeCloud service shares at runtime.
package svc

import (
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/runtime"
	"github.com/homecloudhq/homecloud/cli/internal/store"
)

type Env struct {
	Cfg       core.Config
	Store     *store.Store
	Docker    *runtime.Docker
	AccountID string
	// ContainerAPI is the HomeCloud API endpoint as seen from containers
	// (AWS_ENDPOINT_URL for functions, tasks and instances). Containers that use
	// it need runtime.HostAlias in their ExtraHosts.
	ContainerAPI string
}

func (e *Env) ARN(service, resource string) string { return core.ARN(e.AccountID, service, resource) }

// ContainerName is the Docker name for a HomeCloud resource, e.g. "hc-ec2-i-0abc".
func ContainerName(service, id string) string { return "hc-" + service + "-" + id }
