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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
	"github.com/homecloudhq/homecloud/cli/internal/svc"
)

const (
	// retention is how long HomeCloud's own samples (HC/ namespaces) are kept;
	// custom metrics are kept for customRetention, the oldest timestamp PutMetricData accepts.
	retention       = 24 * time.Hour
	customRetention = 15 * 24 * time.Hour
	sampleInterval  = 30 * time.Second
)

func retentionFor(ns string) time.Duration {
	if strings.HasPrefix(ns, "HC/") {
		return retention
	}
	return customRetention
}

// Point is one datapoint: a value (N == 0), or a statistic set / repeated
// value (N samples with sum S, minimum Lo and maximum Hi; V is their mean).
type Point struct {
	T  time.Time `json:"t"`
	V  float64   `json:"v"`
	N  float64   `json:"n,omitempty"`
	S  float64   `json:"s,omitempty"`
	Lo float64   `json:"lo,omitempty"`
	Hi float64   `json:"hi,omitempty"`
}

func (p Point) count() float64 {
	if p.N > 0 {
		return p.N
	}
	return 1
}

func (p Point) sum() float64 {
	if p.N > 0 {
		return p.S
	}
	return p.V
}

func (p Point) min() float64 {
	if p.N > 0 {
		return p.Lo
	}
	return p.V
}

func (p Point) max() float64 {
	if p.N > 0 {
		return p.Hi
	}
	return p.V
}

type Series struct {
	Namespace  string            `json:"namespace"`
	Name       string            `json:"name"`
	Dimensions map[string]string `json:"dimensions"`
	Unit       string            `json:"unit"`
	HighRes    bool              `json:"high_res,omitempty"`
	Updated    time.Time         `json:"updated,omitempty"`
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
	// Deliver sends log subscription payloads to a Lambda function ARN.
	Deliver func(ctx context.Context, arn string, payload []byte) error
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

const maxSeries = 20000

// Put records one datapoint. Points outside the retention window or more
// than 2 hours in the future are dropped.
func (s *Service) Put(ns, name string, dims map[string]string, unit string, v float64, t time.Time) {
	s.PutPoint(ns, name, dims, unit, Point{T: t, V: v}, false)
}

// PutPoint records a datapoint (possibly a statistic set).
func (s *Service) PutPoint(ns, name string, dims map[string]string, unit string, p Point, highRes bool) {
	if p.T.IsZero() {
		p.T = time.Now().UTC()
	}
	if p.T.Before(time.Now().Add(-retentionFor(ns))) || p.T.After(time.Now().Add(2*time.Hour)) || !finite(p.V, p.N, p.S, p.Lo, p.Hi) {
		return
	}
	if len(dims) == 0 {
		dims = nil
	}
	k := seriesKey(ns, name, dims)
	s.mu.Lock()
	defer s.mu.Unlock()
	x := s.series[k]
	if x == nil {
		if len(s.series) >= maxSeries {
			return
		}
		x = &Series{Namespace: ns, Name: name, Dimensions: dims, Unit: unit}
		s.series[k] = x
	}
	if unit != "" && unit != "None" {
		x.Unit = unit
	}
	x.HighRes = x.HighRes || highRes
	x.Updated = time.Now().UTC()
	// Keep points ordered even when a timestamp arrives late.
	i := len(x.Points)
	for i > 0 && x.Points[i-1].T.After(p.T) {
		i--
	}
	x.Points = append(x.Points, Point{})
	copy(x.Points[i+1:], x.Points[i:])
	x.Points[i] = p
}

func finite(vs ...float64) bool {
	for _, v := range vs {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return false
		}
	}
	return true
}

// prune drops expired points and series that no longer have any.
func (s *Service) prune() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, x := range s.series {
		cut := time.Now().Add(-retentionFor(x.Namespace))
		i := 0
		for i < len(x.Points) && x.Points[i].T.Before(cut) {
			i++
		}
		x.Points = x.Points[i:]
		if len(x.Points) == 0 {
			delete(s.series, k)
		}
	}
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
		s.prune()
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

// RegisterAWS serves CloudWatch Logs ("logs") and CloudWatch metrics, alarms
// and dashboards ("monitoring") over the AWS protocols.
func (s *Service) RegisterAWS() {
	s.registerLogsAWS()
	s.registerMetricsAWS()
}

func (s *Service) collect(ctx context.Context) {
	if s.env.Docker == nil {
		return
	}
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
	bs, unit := s.buckets(ns, name, dims, start, end.Add(time.Nanosecond), period, "")
	out := make([]Datapoint, 0, len(bs))
	for _, b := range bs {
		out = append(out, Datapoint{Timestamp: b.T, Average: round(b.Sum / b.Count), Sum: b.Sum, Minimum: b.Min, Maximum: b.Max, SampleCount: int(b.Count)})
	}
	return out, unit
}

// bucket aggregates the points of one period.
type bucket struct {
	T                    time.Time
	Count, Sum, Min, Max float64
	vals                 []Point
}

// Stat returns a statistic of the bucket: Average, Sum, Minimum, Maximum,
// SampleCount or a percentile such as p99 / p99.9. ok is false for unknown statistics.
func (b bucket) Stat(name string) (float64, bool) {
	switch name {
	case "Average", "avg", "Avg":
		return b.Sum / b.Count, true
	case "Sum", "sum":
		return b.Sum, true
	case "Minimum", "Min", "min":
		return b.Min, true
	case "Maximum", "Max", "max":
		return b.Max, true
	case "SampleCount", "samplecount":
		return b.Count, true
	}
	if pct, ok := percentileOf(name); ok {
		pts := append([]Point(nil), b.vals...)
		sort.Slice(pts, func(i, j int) bool { return pts[i].V < pts[j].V })
		want := pct / 100 * b.Count
		acc := 0.0
		for _, p := range pts {
			acc += p.count()
			if acc >= want {
				return p.V, true
			}
		}
		if len(pts) > 0 {
			return pts[len(pts)-1].V, true
		}
		return 0, true
	}
	return 0, false
}

// percentileOf parses "p99", "p99.9" or "p50".
func percentileOf(stat string) (float64, bool) {
	rest, ok := strings.CutPrefix(strings.ToLower(stat), "p")
	if !ok {
		return 0, false
	}
	f, err := strconv.ParseFloat(rest, 64)
	if err != nil || f < 0 || f > 100 {
		return 0, false
	}
	return f, true
}

// validStat reports whether a statistic name is supported.
func validStat(stat string) bool {
	_, ok := bucket{Count: 1}.Stat(stat)
	return ok
}

// buckets aggregates a series into period-aligned buckets over [start, end).
// When unit is set, only a series with that unit has data.
func (s *Service) buckets(ns, name string, dims map[string]string, start, end time.Time, period time.Duration, unit string) ([]bucket, string) {
	if len(dims) == 0 {
		dims = nil
	}
	s.mu.RLock()
	x := s.series[seriesKey(ns, name, dims)]
	var pts []Point
	u := ""
	if x != nil {
		u = x.Unit
		if unit == "" || unit == u || (unit == "None" && u == "") {
			pts = append(pts, x.Points...)
		}
	}
	s.mu.RUnlock()
	if period <= 0 {
		period = time.Minute
	}
	byT := map[int64]*bucket{}
	for _, p := range pts {
		if p.T.Before(start) || !p.T.Before(end) {
			continue
		}
		k := p.T.Truncate(period).UnixNano()
		b := byT[k]
		if b == nil {
			b = &bucket{T: time.Unix(0, k).UTC(), Min: p.min(), Max: p.max()}
			byT[k] = b
		}
		b.Count += p.count()
		b.Sum += p.sum()
		b.Min = math.Min(b.Min, p.min())
		b.Max = math.Max(b.Max, p.max())
		b.vals = append(b.vals, p)
	}
	out := make([]bucket, 0, len(byT))
	for _, b := range byT {
		out = append(out, *b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].T.Before(out[j].T) })
	return out, u
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
