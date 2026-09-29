// Package cloudwatch implements metrics, logs and alarms. A collector samples
// every HomeCloud-managed container on a fixed interval, services publish their
// own metrics (e.g. Lambda invocations) and users can put custom metrics.
package cloudwatch

import (
	"context"
	"encoding/json"
	"log"
	"math"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
	"github.com/homecloudhq/homecloud/cli/internal/svc"
)

const (
	retention      = 24 * time.Hour
	sampleInterval = 30 * time.Second
)

type Point struct {
	T time.Time `json:"t"`
	V float64   `json:"v"`
}

type Series struct {
	Namespace  string            `json:"namespace"`
	Name       string            `json:"name"`
	Dimensions map[string]string `json:"dimensions"`
	Unit       string            `json:"unit"`
	Points     []Point           `json:"points,omitempty"`
}

func seriesKey(ns, name string, dims map[string]string) string {
	keys := make([]string, 0, len(dims))
	for k := range dims {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	sb.WriteString(ns + "|" + name)
	for _, k := range keys {
		sb.WriteString("|" + k + "=" + dims[k])
	}
	return sb.String()
}

// Notifier delivers alarm notifications to an SNS topic ARN or webhook URL.
type Notifier func(target, subject, message string)

type Service struct {
	env    *svc.Env
	mu     sync.RWMutex
	series map[string]*Series
	prev   map[string]uint64 // last cumulative counter values, for deltas
	logs   *logStore
	Notify Notifier
}

func New(env *svc.Env) (*Service, error) {
	s := &Service{env: env, series: map[string]*Series{}, prev: map[string]uint64{}}
	ls, err := newLogStore(env)
	if err != nil {
		return nil, err
	}
	s.logs = ls
	s.load()
	return s, nil
}

func (s *Service) metricsFile() string { return s.env.Cfg.Path("metrics.json") }

func (s *Service) load() {
	b, err := os.ReadFile(s.metricsFile())
	if err != nil {
		return
	}
	var ss []*Series
	if json.Unmarshal(b, &ss) == nil {
		for _, x := range ss {
			s.series[seriesKey(x.Namespace, x.Name, x.Dimensions)] = x
		}
	}
}

func (s *Service) save() {
	s.mu.RLock()
	ss := make([]*Series, 0, len(s.series))
	for _, x := range s.series {
		ss = append(ss, x)
	}
	b, err := json.Marshal(ss)
	s.mu.RUnlock()
	if err == nil {
		tmp := s.metricsFile() + ".tmp"
		if os.WriteFile(tmp, b, 0o600) == nil {
			_ = os.Rename(tmp, s.metricsFile())
		}
	}
}

// Put records one datapoint.
func (s *Service) Put(ns, name string, dims map[string]string, unit string, v float64, t time.Time) {
	if t.IsZero() {
		t = time.Now().UTC()
	}
	k := seriesKey(ns, name, dims)
	s.mu.Lock()
	defer s.mu.Unlock()
	x := s.series[k]
	if x == nil {
		x = &Series{Namespace: ns, Name: name, Dimensions: dims, Unit: unit}
		s.series[k] = x
	}
	x.Points = append(x.Points, Point{T: t, V: v})
	cut := time.Now().Add(-retention)
	i := 0
	for i < len(x.Points) && x.Points[i].T.Before(cut) {
		i++
	}
	x.Points = x.Points[i:]
}

// delta converts a cumulative counter into a per-interval value.
func (s *Service) delta(key string, cur uint64) float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.prev[key]
	s.prev[key] = cur
	if !ok || cur < p {
		return 0
	}
	return float64(cur - p)
}

var namespaces = map[string]struct{ ns, dim string }{
	"ec2":         {"HC/EC2", "InstanceId"},
	"rds":         {"HC/RDS", "DBInstanceIdentifier"},
	"elasticache": {"HC/ElastiCache", "CacheClusterId"},
	"s3":          {"HC/S3", "Service"},
	"ecs":         {"HC/ECS", "TaskId"},
	"elb":         {"HC/ELB", "LoadBalancer"},
}

// Run samples managed containers and evaluates alarms until ctx is done.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(sampleInterval)
	defer t.Stop()
	saveEvery := 0
	for {
		s.collect(ctx)
		s.evaluateAlarms()
		saveEvery++
		if saveEvery%4 == 0 {
			s.save()
			s.logs.enforceRetention()
		}
		select {
		case <-ctx.Done():
			s.save()
			return
		case <-t.C:
		}
	}
}

func (s *Service) collect(ctx context.Context) {
	cs, err := s.env.Docker.ManagedContainers()
	if err != nil {
		log.Printf("cloudwatch: list containers: %v", err)
		return
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	now := time.Now().UTC()
	for _, c := range cs {
		n, ok := namespaces[c.Labels[core.LabelService]]
		if !ok || c.State != "running" {
			continue
		}
		res := c.Labels[core.LabelResource]
		wg.Add(1)
		sem <- struct{}{}
		go func(id string) {
			defer wg.Done()
			defer func() { <-sem }()
			sctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			u, err := s.env.Docker.Stats(sctx, id)
			if err != nil {
				return
			}
			dims := map[string]string{n.dim: res}
			s.Put(n.ns, "CPUUtilization", dims, "Percent", round(u.CPUPercent), now)
			s.Put(n.ns, "MemoryUtilization", dims, "Percent", round(u.MemoryPercent), now)
			s.Put(n.ns, "MemoryUsed", dims, "Bytes", float64(u.MemoryBytes), now)
			s.Put(n.ns, "NetworkIn", dims, "Bytes", s.delta(id+"rx", u.NetRxBytes), now)
			s.Put(n.ns, "NetworkOut", dims, "Bytes", s.delta(id+"tx", u.NetTxBytes), now)
			s.Put(n.ns, "DiskReadBytes", dims, "Bytes", s.delta(id+"br", u.BlockRead), now)
			s.Put(n.ns, "DiskWriteBytes", dims, "Bytes", s.delta(id+"bw", u.BlockWrite), now)
			s.Put(n.ns, "ProcessCount", dims, "Count", float64(u.Pids), now)
		}(c.ID)
	}
	wg.Wait()
}

func round(v float64) float64 { return math.Round(v*100) / 100 }

// ---- statistics ----

type Datapoint struct {
	Timestamp   time.Time `json:"timestamp"`
	Average     float64   `json:"average"`
	Sum         float64   `json:"sum"`
	Minimum     float64   `json:"minimum"`
	Maximum     float64   `json:"maximum"`
	SampleCount int       `json:"sample_count"`
}

func (d Datapoint) Stat(name string) float64 {
	switch strings.ToLower(name) {
	case "sum":
		return d.Sum
	case "minimum", "min":
		return d.Minimum
	case "maximum", "max":
		return d.Maximum
	case "samplecount":
		return float64(d.SampleCount)
	default:
		return d.Average
	}
}

// Statistics aggregates a series into buckets of period between start and end.
func (s *Service) Statistics(ns, name string, dims map[string]string, start, end time.Time, period time.Duration) ([]Datapoint, string) {
	s.mu.RLock()
	x := s.series[seriesKey(ns, name, dims)]
	var pts []Point
	unit := ""
	if x != nil {
		pts = append(pts, x.Points...)
		unit = x.Unit
	}
	s.mu.RUnlock()
	if period <= 0 {
		period = time.Minute
	}
	buckets := map[int64]*Datapoint{}
	for _, p := range pts {
		if p.T.Before(start) || p.T.After(end) {
			continue
		}
		k := p.T.Truncate(period).Unix()
		d := buckets[k]
		if d == nil {
			d = &Datapoint{Timestamp: time.Unix(k, 0).UTC(), Minimum: p.V, Maximum: p.V}
			buckets[k] = d
		}
		d.Sum += p.V
		d.SampleCount++
		d.Minimum = math.Min(d.Minimum, p.V)
		d.Maximum = math.Max(d.Maximum, p.V)
	}
	out := make([]Datapoint, 0, len(buckets))
	for _, d := range buckets {
		d.Average = round(d.Sum / float64(d.SampleCount))
		out = append(out, *d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Timestamp.Before(out[j].Timestamp) })
	return out, unit
}

// ---- routes ----

func (s *Service) Routes(r *httpx.Router) {
	r.Handle("GET /api/v1/cloudwatch/metrics", "cloudwatch:ListMetrics", s.listMetrics)
	r.Handle("POST /api/v1/cloudwatch/metrics", "cloudwatch:PutMetricData", s.putMetric)
	r.Handle("POST /api/v1/cloudwatch/metrics/query", "cloudwatch:GetMetricStatistics", s.query)
	s.alarmRoutes(r)
	s.logRoutes(r)
}

func (s *Service) listMetrics(c *httpx.Ctx) (any, error) {
	ns := c.Query("namespace")
	s.mu.RLock()
	out := []Series{}
	for _, x := range s.series {
		if ns != "" && x.Namespace != ns {
			continue
		}
		if len(x.Points) == 0 {
			continue
		}
		out = append(out, Series{Namespace: x.Namespace, Name: x.Name, Dimensions: x.Dimensions, Unit: x.Unit})
	}
	s.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		return seriesKey(out[i].Namespace, out[i].Name, out[i].Dimensions) < seriesKey(out[j].Namespace, out[j].Name, out[j].Dimensions)
	})
	return out, nil
}

func (s *Service) putMetric(c *httpx.Ctx) (any, error) {
	var in struct {
		Namespace string `json:"namespace"`
		Data      []struct {
			Name       string            `json:"name"`
			Dimensions map[string]string `json:"dimensions"`
			Value      float64           `json:"value"`
			Unit       string            `json:"unit"`
			Timestamp  time.Time         `json:"timestamp"`
		} `json:"data"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	if in.Namespace == "" || strings.HasPrefix(in.Namespace, "HC/") {
		return nil, core.BadRequest("namespace is required and may not start with the reserved prefix HC/")
	}
	for _, d := range in.Data {
		if d.Name == "" {
			return nil, core.BadRequest("metric name is required")
		}
		s.Put(in.Namespace, d.Name, d.Dimensions, d.Unit, d.Value, d.Timestamp)
	}
	return nil, nil
}

type metricQuery struct {
	Namespace  string            `json:"namespace"`
	Name       string            `json:"name"`
	Dimensions map[string]string `json:"dimensions"`
	Start      time.Time         `json:"start"`
	End        time.Time         `json:"end"`
	Period     int               `json:"period"` // seconds
	Stat       string            `json:"stat"`
}

func (s *Service) query(c *httpx.Ctx) (any, error) {
	var in struct {
		Queries []metricQuery `json:"queries"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, q := range in.Queries {
		if q.End.IsZero() {
			q.End = time.Now()
		}
		if q.Start.IsZero() {
			q.Start = q.End.Add(-time.Hour)
		}
		if q.Period == 0 {
			q.Period = 60
		}
		dps, unit := s.Statistics(q.Namespace, q.Name, q.Dimensions, q.Start, q.End, time.Duration(q.Period)*time.Second)
		out = append(out, map[string]any{"namespace": q.Namespace, "name": q.Name, "dimensions": q.Dimensions, "unit": unit, "datapoints": dps})
	}
	return out, nil
}
