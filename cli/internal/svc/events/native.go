package events

import (
	"errors"
	"slices"
	"sort"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi"
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
	"github.com/homecloudhq/homecloud/cli/internal/store"
)

// Native (console) routes for event buses and EventBridge Scheduler. They
// share their logic with the AWS handlers.

// nat converts an AWS API error to a native one.
func nat[T any](v T, err error) (T, error) {
	var ae *awsapi.Error
	if errors.As(err, &ae) {
		st := ae.Status
		switch ae.Code {
		case "ResourceNotFoundException":
			st = 404
		case "ConflictException", "ResourceAlreadyExistsException":
			st = 409
		}
		return v, core.Errf(st, ae.Code, "%s", ae.Message)
	}
	return v, err
}

func (s *Service) nativeBusRoutes(r *httpx.Router) {
	r.Handle("GET /api/v1/events/buses", "events:ListEventBuses", s.listBuses, httpx.Res("arn:aws:events:{region}:{account}:event-bus/*"))
	r.Handle("POST /api/v1/events/buses", "events:CreateEventBus", s.createBusRoute, httpx.Deferred())
	r.Handle("DELETE /api/v1/events/buses/{name}", "events:DeleteEventBus", s.deleteBusRoute, httpx.Res("arn:aws:events:{region}:{account}:event-bus/{name}"))
}

func (s *Service) listBuses(c *httpx.Ctx) (any, error) {
	all := append([]Bus{{Name: "default", ARN: s.busARN("default")}}, store.List[Bus](s.env.Store, cBuses)...)
	sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	return all, nil
}

func (s *Service) createBusRoute(c *httpx.Ctx) (any, error) {
	var in struct {
		Name          string `json:"name"`
		Description   string `json:"description"`
		DeadLetterARN string `json:"dead_letter_arn"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	b := createBusIn{Name: in.Name, Description: in.Description}
	if in.DeadLetterARN != "" {
		b.DeadLetterConfig = &struct{ Arn string }{Arn: in.DeadLetterARN}
	}
	if _, err := nat(s.createBus(c, b)); err != nil {
		return nil, err
	}
	bus, _ := s.bus(in.Name)
	return bus, nil
}

func (s *Service) deleteBusRoute(c *httpx.Ctx) (any, error) {
	return nat(s.deleteBus(c, c.Param("name")))
}

func (s *Service) nativeSchedulerRoutes(r *httpx.Router) {
	sch := httpx.Res("arn:aws:scheduler:{region}:{account}:schedule/{group}/{name}")
	r.Handle("GET /api/v1/scheduler/schedules", "scheduler:ListSchedules", s.nListSchedules, httpx.Res("arn:aws:scheduler:{region}:{account}:schedule/*/*"))
	r.Handle("PUT /api/v1/scheduler/schedule-groups/{group}/schedules/{name}", "scheduler:CreateSchedule", s.nPutSchedule, httpx.Deferred())
	r.Handle("GET /api/v1/scheduler/schedule-groups/{group}/schedules/{name}", "scheduler:GetSchedule", s.nGetSchedule, sch)
	r.Handle("DELETE /api/v1/scheduler/schedule-groups/{group}/schedules/{name}", "scheduler:DeleteSchedule", s.nDeleteSchedule, sch)
	grp := httpx.Res("arn:aws:scheduler:{region}:{account}:schedule-group/{group}")
	r.Handle("GET /api/v1/scheduler/schedule-groups", "scheduler:ListScheduleGroups", s.nListGroups, httpx.Res("arn:aws:scheduler:{region}:{account}:schedule-group/*"))
	r.Handle("POST /api/v1/scheduler/schedule-groups", "scheduler:CreateScheduleGroup", s.nCreateGroup, httpx.Deferred())
	r.Handle("DELETE /api/v1/scheduler/schedule-groups/{group}", "scheduler:DeleteScheduleGroup", s.nDeleteGroup, grp)
}

func (s *Service) nListSchedules(c *httpx.Ctx) (any, error) {
	all := store.List[storedSchedule](s.env.Store, cSchedules)
	sort.Slice(all, func(i, j int) bool {
		return schedKey(all[i].GroupName, all[i].Name) < schedKey(all[j].GroupName, all[j].Name)
	})
	out := []ScheduleDef{}
	for _, sc := range all {
		if g := c.Query("group"); g != "" && sc.GroupName != g {
			continue
		}
		sc.LastFired = sc.Fired
		out = append(out, sc.ScheduleDef)
	}
	return out, nil
}

// nPutSchedule creates the schedule, or replaces it when it exists.
func (s *Service) nPutSchedule(c *httpx.Ctx) (any, error) {
	var in ScheduleDef
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	in.GroupName = c.Param("group")
	name := c.Param("name")
	create := !store.Has(s.env.Store, cSchedules, schedKey(in.GroupName, name))
	return nat(s.putScheduleIn(c, name, create, in))
}

func (s *Service) nGetSchedule(c *httpx.Ctx) (any, error) {
	return nat(s.getScheduleIn(c, c.Param("group"), c.Param("name")))
}

func (s *Service) nDeleteSchedule(c *httpx.Ctx) (any, error) {
	return nat(s.deleteScheduleIn(c, c.Param("group"), c.Param("name")))
}

func (s *Service) nListGroups(c *httpx.Ctx) (any, error) {
	gs := append([]storedGroup{s.defaultGroup()}, store.List[storedGroup](s.env.Store, cScheduleGroups)...)
	sort.Slice(gs, func(i, j int) bool { return gs[i].Name < gs[j].Name })
	out := []ScheduleGroup{}
	for _, g := range gs {
		out = append(out, g.ScheduleGroup)
	}
	return slices.Clone(out), nil
}

func (s *Service) nCreateGroup(c *httpx.Ctx) (any, error) {
	var in struct {
		Name string            `json:"name"`
		Tags map[string]string `json:"tags"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	return nat(s.createScheduleGroupIn(c, in.Name, in.Tags))
}

func (s *Service) nDeleteGroup(c *httpx.Ctx) (any, error) {
	return nat(s.deleteScheduleGroupIn(c, c.Param("group")))
}
