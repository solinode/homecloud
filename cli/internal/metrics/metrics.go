// Package metrics is a small Prometheus text-format (0.0.4) exposition: labelled
// counters, gauges and histograms plus collectors that report values computed
// at scrape time. It avoids pulling client_golang and its protobuf dependency
// into the binary for the few dozen series HomeCloud exports.
package metrics

import (
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// Kind is a metric family type.
type Kind string

const (
	Counter   Kind = "counter"
	Gauge     Kind = "gauge"
	Histogram Kind = "histogram"
)

// Sample is one value of a family written by a collector.
type Sample struct {
	Labels []string // name, value pairs
	Value  float64
}

// Registry holds metric families in registration order.
type Registry struct {
	mu       sync.Mutex
	families []family
	names    map[string]bool
}

type family interface {
	write(w io.Writer)
	name() string
}

func NewRegistry() *Registry { return &Registry{names: map[string]bool{}} }

func (r *Registry) add(f family) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.names[f.name()] {
		panic("metrics: duplicate family " + f.name())
	}
	r.names[f.name()] = true
	r.families = append(r.families, f)
}

// WriteText writes every family in the Prometheus text format.
func (r *Registry) WriteText(w io.Writer) {
	r.mu.Lock()
	fs := append([]family(nil), r.families...)
	r.mu.Unlock()
	for _, f := range fs {
		f.write(w)
	}
}

// ContentType is the media type of WriteText's output.
const ContentType = "text/plain; version=0.0.4; charset=utf-8"

type header struct {
	n, help string
	kind    Kind
}

func (h header) name() string { return h.n }
func (h header) writeHeader(w io.Writer) {
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", h.n, escapeHelp(h.help), h.n, h.kind)
}

// ---- vectors (counters and gauges kept in memory) ----

// Vec is a counter or gauge with a fixed set of label names.
type Vec struct {
	header
	labels []string
	mu     sync.RWMutex
	series map[string]*series
}

type series struct {
	values []string
	bits   atomic.Uint64
}

func (s *series) add(d float64) {
	for {
		old := s.bits.Load()
		if s.bits.CompareAndSwap(old, math.Float64bits(math.Float64frombits(old)+d)) {
			return
		}
	}
}

// NewCounter registers a counter family.
func (r *Registry) NewCounter(name, help string, labels ...string) *Vec {
	v := &Vec{header: header{name, help, Counter}, labels: labels, series: map[string]*series{}}
	r.add(v)
	return v
}

// NewGauge registers a gauge family.
func (r *Registry) NewGauge(name, help string, labels ...string) *Vec {
	v := &Vec{header: header{name, help, Gauge}, labels: labels, series: map[string]*series{}}
	r.add(v)
	return v
}

func (v *Vec) get(values []string) *series {
	if len(values) != len(v.labels) {
		panic(fmt.Sprintf("metrics: %s wants %d label values, got %d", v.n, len(v.labels), len(values)))
	}
	key := strings.Join(values, "\xff")
	v.mu.RLock()
	s := v.series[key]
	v.mu.RUnlock()
	if s != nil {
		return s
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if s = v.series[key]; s == nil {
		s = &series{values: append([]string(nil), values...)}
		v.series[key] = s
	}
	return s
}

// Inc adds one to the series with the label values.
func (v *Vec) Inc(values ...string) { v.get(values).add(1) }

// Add adds d to the series with the label values.
func (v *Vec) Add(d float64, values ...string) { v.get(values).add(d) }

// Set sets the series (gauges).
func (v *Vec) Set(x float64, values ...string) { v.get(values).bits.Store(math.Float64bits(x)) }

// Value returns the current value of a series (tests).
func (v *Vec) Value(values ...string) float64 {
	return math.Float64frombits(v.get(values).bits.Load())
}

func (v *Vec) write(w io.Writer) {
	v.mu.RLock()
	ss := make([]*series, 0, len(v.series))
	for _, s := range v.series {
		ss = append(ss, s)
	}
	v.mu.RUnlock()
	sort.Slice(ss, func(i, j int) bool { return lessValues(ss[i].values, ss[j].values) })
	v.writeHeader(w)
	for _, s := range ss {
		writeSample(w, v.n, pairs(v.labels, s.values), math.Float64frombits(s.bits.Load()))
	}
}

// ---- histograms ----

// HistogramVec is a histogram with a fixed set of label names.
type HistogramVec struct {
	header
	labels  []string
	buckets []float64
	mu      sync.Mutex
	series  map[string]*hseries
}

type hseries struct {
	values []string
	counts []uint64 // per bucket, non-cumulative; last is +Inf
	sum    float64
	count  uint64
}

// NewHistogram registers a histogram family with upper bucket bounds.
func (r *Registry) NewHistogram(name, help string, buckets []float64, labels ...string) *HistogramVec {
	b := append([]float64(nil), buckets...)
	sort.Float64s(b)
	h := &HistogramVec{header: header{name, help, Histogram}, labels: labels, buckets: b, series: map[string]*hseries{}}
	r.add(h)
	return h
}

// Observe records x in the series with the label values.
func (h *HistogramVec) Observe(x float64, values ...string) {
	if len(values) != len(h.labels) {
		panic(fmt.Sprintf("metrics: %s wants %d label values, got %d", h.n, len(h.labels), len(values)))
	}
	key := strings.Join(values, "\xff")
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.series[key]
	if s == nil {
		s = &hseries{values: append([]string(nil), values...), counts: make([]uint64, len(h.buckets)+1)}
		h.series[key] = s
	}
	i := sort.SearchFloat64s(h.buckets, x) // first bound >= x
	s.counts[i]++
	s.sum += x
	s.count++
}

func (h *HistogramVec) write(w io.Writer) {
	h.mu.Lock()
	type snap struct {
		values []string
		counts []uint64
		sum    float64
		count  uint64
	}
	ss := make([]snap, 0, len(h.series))
	for _, s := range h.series {
		ss = append(ss, snap{s.values, append([]uint64(nil), s.counts...), s.sum, s.count})
	}
	h.mu.Unlock()
	sort.Slice(ss, func(i, j int) bool { return lessValues(ss[i].values, ss[j].values) })
	h.writeHeader(w)
	for _, s := range ss {
		base := pairs(h.labels, s.values)
		var cum uint64
		for i, b := range h.buckets {
			cum += s.counts[i]
			writeSample(w, h.n+"_bucket", append(base, "le", formatFloat(b)), float64(cum))
		}
		writeSample(w, h.n+"_bucket", append(base, "le", "+Inf"), float64(s.count))
		writeSample(w, h.n+"_sum", base, s.sum)
		writeSample(w, h.n+"_count", base, float64(s.count))
	}
}

// ---- collectors (values computed at scrape time) ----

type collector struct {
	header
	collect func() []Sample
}

// NewCollector registers a family whose samples collect returns at each scrape.
func (r *Registry) NewCollector(name, help string, kind Kind, collect func() []Sample) {
	r.add(&collector{header: header{name, help, kind}, collect: collect})
}

// NewGaugeFunc registers an unlabelled gauge read at scrape time.
func (r *Registry) NewGaugeFunc(name, help string, f func() float64) {
	r.NewCollector(name, help, Gauge, func() []Sample { return []Sample{{Value: f()}} })
}

func (c *collector) write(w io.Writer) {
	ss := c.collect()
	if len(ss) == 0 {
		return
	}
	c.writeHeader(w)
	for _, s := range ss {
		if math.IsNaN(s.Value) {
			continue
		}
		writeSample(w, c.n, s.Labels, s.Value)
	}
}

// ---- formatting ----

func pairs(names, values []string) []string {
	out := make([]string, 0, 2*len(names)+2)
	for i, n := range names {
		out = append(out, n, values[i])
	}
	return out
}

func lessValues(a, b []string) bool {
	for i := range a {
		if i >= len(b) {
			return false
		}
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return len(a) < len(b)
}

func writeSample(w io.Writer, name string, labels []string, v float64) {
	var b strings.Builder
	b.WriteString(name)
	if len(labels) > 0 {
		b.WriteByte('{')
		for i := 0; i+1 < len(labels); i += 2 {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(labels[i])
			b.WriteString(`="`)
			b.WriteString(escapeLabel(labels[i+1]))
			b.WriteByte('"')
		}
		b.WriteByte('}')
	}
	b.WriteByte(' ')
	b.WriteString(formatFloat(v))
	b.WriteByte('\n')
	_, _ = io.WriteString(w, b.String())
}

func formatFloat(v float64) string {
	switch {
	case math.IsInf(v, 1):
		return "+Inf"
	case math.IsInf(v, -1):
		return "-Inf"
	case math.IsNaN(v):
		return "NaN"
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}

var labelEscaper = strings.NewReplacer(`\`, `\\`, "\n", `\n`, `"`, `\"`)
var helpEscaper = strings.NewReplacer(`\`, `\\`, "\n", `\n`)

func escapeLabel(s string) string { return labelEscaper.Replace(s) }
func escapeHelp(s string) string  { return helpEscaper.Replace(s) }
