package cloudwatch

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi"
	"github.com/homecloudhq/homecloud/cli/internal/store"
)

// Anomaly detection.
//
// The model is deliberately simple and deterministic (AWS trains a machine
// learning model). For each timestamp the band is mean +/- k standard
// deviations (population) of a set of comparable historical datapoints:
//
//   - history is the metric's stored datapoints of the last 14 days (before the
//     queried window when at least anomalyMinPoints exist there, so a spike
//     being evaluated does not widen its own band), minus the detector's
//     ExcludedTimeRanges;
//   - with fewer than anomalyMinPoints datapoints, or a span under one hour,
//     there is no band (GetMetricData returns empty Values; an alarm has
//     INSUFFICIENT_DATA);
//   - with at least 13 days of history the samples are bucketed by hour of week
//     (in the detector's MetricTimezone), with at least 1 day by hour of day,
//     otherwise all history is one bucket (rolling window). A bucket with fewer
//     than anomalyMinBucket samples falls back to the next coarser one.
const (
	anomalyHistory   = 14 * 24 * time.Hour
	anomalyMinPoints = 10
	anomalyMinBucket = 3
	cAnomaly         = "cloudwatch_anomaly_detectors"
)

type timeRange struct{ Start, End time.Time }

type hist struct {
	t []time.Time
	v []float64
}

// bandModel is the trained model: bucket statistics at three granularities.
type bandModel struct {
	loc     *time.Location
	level   int // 0 rolling, 1 hour of day, 2 hour of week
	buckets [3]map[int]*runStat
}

type runStat struct{ n, sum, sq float64 }

func (r *runStat) add(v float64) { r.n++; r.sum += v; r.sq += v * v }
func (r *runStat) meanStd() (float64, float64) {
	m := r.sum / r.n
	return m, math.Sqrt(math.Max(0, r.sq/r.n-m*m))
}

func bucketKey(level int, t time.Time, loc *time.Location) int {
	lt := t.In(loc)
	switch level {
	case 1:
		return lt.Hour()
	case 2:
		return int(lt.Weekday())*24 + lt.Hour()
	}
	return 0
}

func inRanges(t time.Time, rs []timeRange) bool {
	for _, r := range rs {
		if !t.Before(r.Start) && t.Before(r.End) {
			return true
		}
	}
	return false
}

// trainBand builds a model from history, or nil when there is too little of it.
func trainBand(h hist, excl []timeRange, loc *time.Location) *bandModel {
	var ts []time.Time
	var vs []float64
	for i, t := range h.t {
		if !inRanges(t, excl) {
			ts, vs = append(ts, t), append(vs, h.v[i])
		}
	}
	if len(ts) < anomalyMinPoints || ts[len(ts)-1].Sub(ts[0]) < time.Hour {
		return nil
	}
	span := ts[len(ts)-1].Sub(ts[0])
	m := &bandModel{loc: loc}
	switch {
	case span >= 13*24*time.Hour:
		m.level = 2
	case span >= 24*time.Hour:
		m.level = 1
	}
	for lv := 0; lv <= m.level; lv++ {
		m.buckets[lv] = map[int]*runStat{}
		for i, t := range ts {
			k := bucketKey(lv, t, loc)
			if m.buckets[lv][k] == nil {
				m.buckets[lv][k] = &runStat{}
			}
			m.buckets[lv][k].add(vs[i])
		}
	}
	return m
}

// band returns the lower and upper bounds at t.
func (m *bandModel) band(t time.Time, k float64) (lo, hi float64) {
	for lv := m.level; lv >= 0; lv-- {
		if b := m.buckets[lv][bucketKey(lv, t, m.loc)]; b != nil && (lv == 0 || b.n >= anomalyMinBucket) {
			mean, sd := b.meanStd()
			return mean - k*sd, mean + k*sd
		}
	}
	return 0, 0
}

// AnomalyDetector is a stored detector. Exactly one of Single and Math is set.
type AnomalyDetector struct {
	Single *SingleMetricDetector `json:"single,omitempty"`
	Math   []MetricDataQuery     `json:"math,omitempty"`
	Config AnomalyConfig         `json:"config"`
	Spikes bool                  `json:"periodic_spikes,omitempty"`
}

type SingleMetricDetector struct {
	AccountId  string      `json:",omitempty"`
	Namespace  string      `json:",omitempty"`
	MetricName string      `json:",omitempty"`
	Dimensions []Dimension `json:",omitempty"`
	Stat       string      `json:",omitempty"`
}

type AnomalyConfig struct {
	ExcludedTimeRanges []timeRange `json:"excluded,omitempty"`
	MetricTimezone     string      `json:"timezone,omitempty"`
}

func (c AnomalyConfig) location() *time.Location {
	if c.MetricTimezone == "" {
		return time.UTC
	}
	if l, err := time.LoadLocation(c.MetricTimezone); err == nil {
		return l
	}
	return time.UTC
}

func singleKey(d SingleMetricDetector) string {
	dims := dimMap(d.Dimensions)
	return "s|" + seriesKey(d.Namespace, d.MetricName, dims) + "|" + d.Stat
}

func mathKey(qs []MetricDataQuery) string {
	qs = append([]MetricDataQuery(nil), qs...)
	sort.Slice(qs, func(i, j int) bool { return qs[i].Id < qs[j].Id })
	var sb strings.Builder
	for _, q := range qs {
		fmt.Fprintf(&sb, "%s|%s|", q.Id, q.Expression)
		if ms := q.MetricStat; ms != nil {
			fmt.Fprintf(&sb, "%s|%d|%s|%s", seriesKey(ms.Metric.Namespace, ms.Metric.MetricName, dimMap(ms.Metric.Dimensions)), ms.Period, ms.Stat, ms.Unit)
		}
		sb.WriteString(";")
	}
	return "m|" + sb.String()
}

func (d AnomalyDetector) key() string {
	if d.Single != nil {
		return singleKey(*d.Single)
	}
	return mathKey(d.Math)
}

func detectorID(key string) string {
	h := sha1.Sum([]byte(key))
	return hex.EncodeToString(h[:])
}

// findDetector returns the stored detector for a single metric, if any.
func (s *Service) findSingleDetector(ms *MetricStat) (AnomalyDetector, bool) {
	d, err := store.Get[AnomalyDetector](s.env.Store, cAnomaly, detectorID(singleKey(SingleMetricDetector{Namespace: ms.Metric.Namespace,
		MetricName: ms.Metric.MetricName, Dimensions: ms.Metric.Dimensions, Stat: ms.Stat})))
	return d, err == nil
}

// ensureDetector creates a default detector when none exists (as AWS does for
// alarms that use ANOMALY_DETECTION_BAND).
func (s *Service) ensureDetector(d AnomalyDetector) {
	id := detectorID(d.key())
	if !store.Has(s.env.Store, cAnomaly, id) {
		_ = store.Put(s.env.Store, cAnomaly, id, d)
	}
}

// ---- ANOMALY_DETECTION_BAND in metric math ----

// bandNode evaluates ANOMALY_DETECTION_BAND(src, k): an array of the upper and
// lower band series. Nothing is returned until a detector exists for src
// (created by PutAnomalyDetector or by an alarm) and it has enough history.
func (p *mathParser) bandNode(src string, kn mnode) mnode {
	env := p.env
	return func() (mval, error) {
		k := 2.0
		if kn != nil {
			v, err := kn()
			if err != nil {
				return mval{}, err
			}
			if v.scalar == nil {
				return mval{}, fmt.Errorf("ANOMALY_DETECTION_BAND needs a number of standard deviations")
			}
			k = *v.scalar
		}
		if k < 0 || k > 100 {
			return mval{}, fmt.Errorf("ANOMALY_DETECTION_BAND standard deviations must be between 0 and 100")
		}
		if _, ok := env.queries[src]; !ok {
			return mval{}, fmt.Errorf("unknown metric or expression ID %q", src)
		}
		// Evaluate the source over 14 days of history in a separate environment.
		hs := env.start.Add(-anomalyHistory)
		sub := &mathEnv{s: env.s, queries: env.queries, order: env.order, start: hs, end: env.end, done: map[string]mval{}, busy: map[string]bool{}}
		v, err := sub.eval(src)
		if err != nil {
			return mval{}, err
		}
		if v.series == nil {
			return mval{}, fmt.Errorf("ANOMALY_DETECTION_BAND needs a time series")
		}
		var cfg AnomalyConfig
		found := false
		q := env.queries[src]
		if q.MetricStat != nil {
			var d AnomalyDetector
			if d, found = env.s.findSingleDetector(q.MetricStat); found {
				cfg = d.Config
			}
		} else if d, err := store.Get[AnomalyDetector](env.s.env.Store, cAnomaly, detectorID(mathKey(env.s.depQueries(env.queries, src)))); err == nil {
			cfg, found = d.Config, true
		}
		label := v.series.label
		up := &tseries{label: label + " (Upper)", period: v.series.period, band: "upper"}
		lo := &tseries{label: label + " (Lower)", period: v.series.period, band: "lower"}
		if !found {
			return mval{array: []*tseries{up, lo}}, nil
		}
		var trainH, all hist
		for i, t := range v.series.t {
			all.t, all.v = append(all.t, t), append(all.v, v.series.v[i])
			if t.Before(env.start) {
				trainH.t, trainH.v = append(trainH.t, t), append(trainH.v, v.series.v[i])
			}
		}
		m := trainBand(trainH, cfg.ExcludedTimeRanges, cfg.location())
		if m == nil {
			m = trainBand(all, cfg.ExcludedTimeRanges, cfg.location())
		}
		if m == nil {
			return mval{array: []*tseries{up, lo}}, nil
		}
		for _, t := range p.grid(v.series.period) {
			l, h := m.band(t, k)
			up.t, up.v = append(up.t, t), append(up.v, h)
			lo.t, lo.v = append(lo.t, t), append(lo.v, l)
		}
		return mval{array: []*tseries{up, lo}}, nil
	}
}

// depQueries returns the queries id needs, transitively, in request order.
func (s *Service) depQueries(qs map[string]MetricDataQuery, id string) []MetricDataQuery {
	need := map[string]bool{}
	var walk func(string)
	walk = func(id string) {
		if need[id] {
			return
		}
		q, ok := qs[id]
		if !ok {
			return
		}
		need[id] = true
		if q.Expression != "" {
			p := &mathParser{src: q.Expression}
			if p.lex() != nil {
				return
			}
			for _, t := range p.toks {
				walk(t)
			}
		}
	}
	walk(id)
	var out []MetricDataQuery
	for _, q := range qs {
		if need[q.Id] {
			out = append(out, q)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Id < out[j].Id })
	return out
}

// bandSource reports the metric ID an expression's ANOMALY_DETECTION_BAND refers to.
func bandSource(expr string) (string, bool) {
	p := &mathParser{src: expr}
	if p.lex() != nil {
		return "", false
	}
	for i, t := range p.toks {
		if strings.EqualFold(t, "ANOMALY_DETECTION_BAND") && i+2 < len(p.toks) && p.toks[i+1] == "(" {
			return p.toks[i+2], true
		}
	}
	return "", false
}

// ---- AWS API ----

type awsExcluded struct {
	StartTime awsapi.Time
	EndTime   awsapi.Time
}

type awsAnomalyConfig struct {
	ExcludedTimeRanges []awsExcluded `json:",omitempty"`
	MetricTimezone     string        `json:",omitempty"`
}

type awsMathDetector struct{ MetricDataQueries []MetricDataQuery }

type anomalyInput struct {
	Namespace                   string
	MetricName                  string
	Dimensions                  []Dimension
	Stat                        string
	Configuration               *awsAnomalyConfig
	MetricCharacteristics       *struct{ PeriodicSpikes *bool }
	SingleMetricAnomalyDetector *SingleMetricDetector
	MetricMathAnomalyDetector   *awsMathDetector
}

// detector validates the input and returns the detector it describes.
func (s *Service) parseDetector(in anomalyInput, needConfig bool) (AnomalyDetector, error) {
	var d AnomalyDetector
	switch {
	case in.SingleMetricAnomalyDetector != nil && in.MetricMathAnomalyDetector != nil:
		return d, invalid("specify only one of SingleMetricAnomalyDetector and MetricMathAnomalyDetector")
	case in.MetricMathAnomalyDetector != nil:
		qs := in.MetricMathAnomalyDetector.MetricDataQueries
		if len(qs) == 0 {
			return d, missingParam("MetricMathAnomalyDetector.MetricDataQueries")
		}
		returns := 0
		for _, q := range qs {
			if q.ReturnData == nil || *q.ReturnData {
				returns++
			}
		}
		if returns != 1 {
			return d, invalid("exactly one of the MetricDataQueries must have ReturnData true")
		}
		if _, _, err := s.evalMetricQueries(qs, time.Now().Add(-time.Minute), time.Now()); err != nil {
			return d, invalid("%v", err)
		}
		d.Math = qs
	default:
		sm := SingleMetricDetector{Namespace: in.Namespace, MetricName: in.MetricName, Dimensions: in.Dimensions, Stat: in.Stat}
		if in.SingleMetricAnomalyDetector != nil {
			sm = *in.SingleMetricAnomalyDetector
		}
		if sm.Namespace == "" {
			return d, missingParam("Namespace")
		}
		if sm.MetricName == "" {
			return d, missingParam("MetricName")
		}
		if sm.Stat == "" {
			return d, missingParam("Stat")
		}
		if !validStat(sm.Stat) {
			return d, invalid("unsupported statistic %q", sm.Stat)
		}
		if err := checkDims(sm.Dimensions); err != nil {
			return d, err
		}
		sm.AccountId = ""
		d.Single = &sm
	}
	if c := in.Configuration; c != nil && needConfig {
		for _, r := range c.ExcludedTimeRanges {
			if !r.StartTime.Before(r.EndTime.Time) {
				return d, invalid("ExcludedTimeRanges StartTime must be before EndTime")
			}
			d.Config.ExcludedTimeRanges = append(d.Config.ExcludedTimeRanges, timeRange{r.StartTime.Time, r.EndTime.Time})
		}
		if c.MetricTimezone != "" {
			if _, err := time.LoadLocation(c.MetricTimezone); err != nil {
				return d, invalid("MetricTimezone %q is not a valid time zone name", c.MetricTimezone)
			}
			d.Config.MetricTimezone = c.MetricTimezone
		}
	}
	if in.MetricCharacteristics != nil && in.MetricCharacteristics.PeriodicSpikes != nil {
		d.Spikes = *in.MetricCharacteristics.PeriodicSpikes
	}
	return d, nil
}

func (s *Service) awsPutAnomalyDetector(q *awsapi.Req) (any, error) {
	var in anomalyInput
	if err := q.Decode(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("cloudwatch:PutAnomalyDetector", "*"); err != nil {
		return nil, err
	}
	d, err := s.parseDetector(in, true)
	if err != nil {
		return nil, err
	}
	if err := store.Put(s.env.Store, cAnomaly, detectorID(d.key()), d); err != nil {
		return nil, err
	}
	return struct{}{}, nil
}

func (s *Service) awsDeleteAnomalyDetector(q *awsapi.Req) (any, error) {
	var in anomalyInput
	if err := q.Decode(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("cloudwatch:DeleteAnomalyDetector", "*"); err != nil {
		return nil, err
	}
	d, err := s.parseDetector(in, false)
	if err != nil {
		return nil, err
	}
	id := detectorID(d.key())
	if !store.Has(s.env.Store, cAnomaly, id) {
		return nil, cwErr(404, "ResourceNotFound", "The specified anomaly detector does not exist.")
	}
	_ = store.Delete(s.env.Store, cAnomaly, id)
	return struct{}{}, nil
}

type awsDetector struct {
	Namespace                   string            `json:",omitempty"`
	MetricName                  string            `json:",omitempty"`
	Dimensions                  []Dimension       `json:",omitempty"`
	Stat                        string            `json:",omitempty"`
	Configuration               *awsAnomalyConfig `json:",omitempty"`
	StateValue                  string
	MetricCharacteristics       *struct{ PeriodicSpikes bool } `json:",omitempty"`
	SingleMetricAnomalyDetector *SingleMetricDetector          `json:",omitempty"`
	MetricMathAnomalyDetector   *awsMathDetector               `json:",omitempty"`
}

func (s *Service) awsDescribeAnomalyDetectors(q *awsapi.Req) (any, error) {
	var in struct {
		Namespace            string
		MetricName           string
		Dimensions           []Dimension
		AnomalyDetectorTypes []string
		NextToken            string
		MaxResults           int
	}
	if err := q.Decode(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("cloudwatch:DescribeAnomalyDetectors", "*"); err != nil {
		return nil, err
	}
	for _, t := range in.AnomalyDetectorTypes {
		if t != "SINGLE_METRIC" && t != "METRIC_MATH" {
			return nil, invalid("AnomalyDetectorTypes must be SINGLE_METRIC or METRIC_MATH")
		}
	}
	if in.MaxResults < 0 || in.MaxResults > 100 {
		return nil, invalid("MaxResults must be between 1 and 100")
	}
	all := store.List[AnomalyDetector](s.env.Store, cAnomaly)
	sort.Slice(all, func(i, j int) bool { return all[i].key() < all[j].key() })
	var out []awsDetector
	for _, d := range all {
		typ := "METRIC_MATH"
		if d.Single != nil {
			typ = "SINGLE_METRIC"
		}
		if len(in.AnomalyDetectorTypes) > 0 && !contains(in.AnomalyDetectorTypes, typ) {
			continue
		}
		if d.Single != nil {
			if in.Namespace != "" && d.Single.Namespace != in.Namespace || in.MetricName != "" && d.Single.MetricName != in.MetricName {
				continue
			}
			if len(in.Dimensions) > 0 && seriesKey("", "", dimMap(in.Dimensions)) != seriesKey("", "", dimMap(d.Single.Dimensions)) {
				continue
			}
		} else if in.Namespace != "" || in.MetricName != "" || len(in.Dimensions) > 0 {
			continue
		}
		out = append(out, s.describeDetector(d))
	}
	start := 0
	if in.NextToken != "" {
		n, err := strconv.Atoi(in.NextToken)
		if err != nil || n < 0 || n > len(out) {
			return nil, cwErr(400, "InvalidNextToken", "invalid NextToken")
		}
		start = n
	}
	out = out[start:]
	res := struct {
		AnomalyDetectors []awsDetector
		NextToken        string `json:",omitempty"`
	}{AnomalyDetectors: []awsDetector{}}
	if in.MaxResults > 0 && len(out) > in.MaxResults {
		out = out[:in.MaxResults]
		res.NextToken = strconv.Itoa(start + in.MaxResults)
	}
	res.AnomalyDetectors = append(res.AnomalyDetectors, out...)
	return res, nil
}

func (s *Service) describeDetector(d AnomalyDetector) awsDetector {
	out := awsDetector{Configuration: &awsAnomalyConfig{MetricTimezone: d.Config.MetricTimezone}}
	if out.Configuration.MetricTimezone == "" {
		out.Configuration.MetricTimezone = "UTC"
	}
	for _, r := range d.Config.ExcludedTimeRanges {
		out.Configuration.ExcludedTimeRanges = append(out.Configuration.ExcludedTimeRanges, awsExcluded{awsapi.Time{Time: r.Start}, awsapi.Time{Time: r.End}})
	}
	if d.Spikes {
		out.MetricCharacteristics = &struct{ PeriodicSpikes bool }{true}
	}
	now := time.Now()
	var series *tseries
	if d.Single != nil {
		sm := *d.Single
		out.Namespace, out.MetricName, out.Dimensions, out.Stat = sm.Namespace, sm.MetricName, sm.Dimensions, sm.Stat
		out.SingleMetricAnomalyDetector = &sm
		res, _, err := s.evalMetricQueries([]MetricDataQuery{{Id: "m", MetricStat: &MetricStat{Metric: Metric{Namespace: sm.Namespace, MetricName: sm.MetricName,
			Dimensions: sm.Dimensions}, Period: 300, Stat: sm.Stat}}}, now.Add(-anomalyHistory), now)
		if err == nil {
			series = res["m"].series
		}
	} else {
		out.MetricMathAnomalyDetector = &awsMathDetector{d.Math}
		if res, order, err := s.evalMetricQueries(d.Math, now.Add(-anomalyHistory), now); err == nil {
			for _, id := range order {
				for _, q := range d.Math {
					if q.Id == id && (q.ReturnData == nil || *q.ReturnData) {
						series = res[id].series
					}
				}
			}
		}
	}
	out.StateValue = "PENDING_TRAINING"
	if series != nil && trainBand(hist{series.t, series.v}, d.Config.ExcludedTimeRanges, d.Config.location()) != nil {
		out.StateValue = "TRAINED"
	}
	return out
}
