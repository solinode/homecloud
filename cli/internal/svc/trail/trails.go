package trail

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/store"
)

const cTrails = "cloudtrail_trails"

// Trail configures delivery of the audit log to an S3 bucket.
type Trail struct {
	Name              string    `json:"name"`
	S3Bucket          string    `json:"s3_bucket"`
	S3Prefix          string    `json:"s3_prefix,omitempty"`
	SNSTopic          string    `json:"sns_topic,omitempty"`
	IncludeGlobal     bool      `json:"include_global"`
	MultiRegion       bool      `json:"multi_region"`
	LogValidation     bool      `json:"log_validation"`
	KMSKey            string    `json:"kms_key,omitempty"`
	Organization      bool      `json:"organization"`
	CWLogsGroup       string    `json:"cw_logs_group,omitempty"`
	CWLogsRole        string    `json:"cw_logs_role,omitempty"`
	EventSelectors    any       `json:"event_selectors,omitempty"`
	Logging           bool      `json:"logging"`
	StartedAt         time.Time `json:"started_at,omitempty"`
	StoppedAt         time.Time `json:"stopped_at,omitempty"`
	LastDelivery      time.Time `json:"last_delivery,omitempty"`
	LastAttempt       time.Time `json:"last_attempt,omitempty"`
	LastDeliveryError string    `json:"last_delivery_error,omitempty"`
	// Cursor is the time up to which events were delivered.
	Cursor  time.Time `json:"cursor,omitempty"`
	Tags    core.Tags `json:"tags,omitempty"`
	Created time.Time `json:"created"`
}

var trailName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{2,127}$`)

// ValidName reports whether name is a valid trail name.
func ValidName(name string) bool {
	return trailName.MatchString(name) && !strings.Contains(name, "..") && !strings.Contains(name, "--")
}

// ARN is the trail's ARN.
func (s *Service) ARN(name string) string { return s.env.ARN("cloudtrail", "trail/"+name) }

// Trails lists trails by name.
func (s *Service) Trails() []Trail {
	l := store.List[Trail](s.env.Store, cTrails)
	sort.Slice(l, func(i, j int) bool { return l[i].Name < l[j].Name })
	return l
}

// Trail returns the named trail (a name or ARN).
func (s *Service) Trail(ref string) (Trail, error) {
	name := ref
	if strings.HasPrefix(ref, "arn:") {
		i := strings.Index(ref, ":trail/")
		if i < 0 {
			return Trail{}, core.NotFound("trail", ref)
		}
		name = ref[i+len(":trail/"):]
	}
	t, err := store.Get[Trail](s.env.Store, cTrails, name)
	if err != nil {
		return t, core.NotFound("trail", ref)
	}
	return t, nil
}

// Create stores a new trail; logging is off until Start.
func (s *Service) Create(t Trail) (Trail, error) {
	if !ValidName(t.Name) {
		return t, core.BadRequest("trail name must be 3-128 characters: letters, numbers, periods, underscores and hyphens")
	}
	if t.S3Bucket == "" {
		return t, core.BadRequest("S3BucketName is required")
	}
	if err := s.checkBucket(t.S3Bucket); err != nil {
		return t, err
	}
	if store.Has(s.env.Store, cTrails, t.Name) {
		return t, core.Errf(400, "TrailAlreadyExists", "trail %s already exists", t.Name)
	}
	t.Created = core.Now()
	return t, store.Put(s.env.Store, cTrails, t.Name, t)
}

func (s *Service) checkBucket(b string) error {
	if s.BucketExists == nil {
		return nil
	}
	ok, err := s.BucketExists(b)
	if err != nil {
		return err
	}
	if !ok {
		return core.Errf(400, "S3BucketDoesNotExist", "S3 bucket %s does not exist", b)
	}
	return nil
}

// CheckBucket validates a delivery bucket.
func (s *Service) CheckBucket(b string) error { return s.checkBucket(b) }

// Modify updates a trail under the store's lock.
func (s *Service) Modify(name string, fn func(*Trail) error) (Trail, error) {
	t, err := store.Update(s.env.Store, cTrails, name, fn)
	if err == store.ErrNotFound {
		return t, core.NotFound("trail", name)
	}
	return t, err
}

// Delete removes a trail.
func (s *Service) Delete(name string) error {
	if _, err := s.Trail(name); err != nil {
		return err
	}
	return store.Delete(s.env.Store, cTrails, name)
}

// Start begins logging: events from now on are delivered.
func (s *Service) Start(name string) error {
	_, err := s.Modify(name, func(t *Trail) error {
		if !t.Logging {
			t.Logging, t.StartedAt, t.Cursor = true, time.Now().UTC(), time.Now().UTC()
		}
		return nil
	})
	return err
}

// Stop ends logging after delivering what was recorded so far.
func (s *Service) Stop(name string) error {
	if t, err := s.Trail(name); err == nil && t.Logging {
		s.deliver(t, time.Now().UTC())
	}
	_, err := s.Modify(name, func(t *Trail) error {
		if t.Logging {
			t.Logging, t.StoppedAt = false, time.Now().UTC()
		}
		return nil
	})
	return err
}

// LogKey is the object key of a log file delivered at t.
func LogKey(prefix, account string, t time.Time, unique string) string {
	t = t.UTC()
	p := ""
	if prefix != "" {
		p = strings.Trim(prefix, "/") + "/"
	}
	return fmt.Sprintf("%sAWSLogs/%s/CloudTrail/%s/%s/%s_CloudTrail_%s_%s_%s.json.gz", p, account, core.Region, t.Format("2006/01/02"),
		account, core.Region, t.Format("20060102T1504Z"), unique)
}

const filesPerBatch = 2000

// deliver writes the events recorded since the trail's cursor (up to upTo) to
// its bucket, one gzipped {"Records": [...]} file per batch.
func (s *Service) deliver(t Trail, upTo time.Time) {
	if s.Deliver == nil {
		return
	}
	s.dmu.Lock()
	defer s.dmu.Unlock()
	if cur, err := s.Trail(t.Name); err == nil {
		t = cur
	}
	if !upTo.After(t.Cursor) {
		return
	}
	all, _, _ := s.Search(Filter{Start: t.Cursor.Add(time.Nanosecond), End: upTo}, "", 1<<30)
	attempt := time.Now().UTC()
	var derr error
	wrote := false
	// Search is newest-first; files hold events oldest-first.
	for i, j := 0, len(all)-1; i < j; i, j = i+1, j-1 {
		all[i], all[j] = all[j], all[i]
	}
	for len(all) > 0 && derr == nil {
		n := min(len(all), filesPerBatch)
		recs := make([]map[string]any, 0, n)
		for _, e := range all[:n] {
			recs = append(recs, e.Record(s.env.AccountID))
		}
		raw, _ := json.Marshal(map[string]any{"Records": recs})
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		zw.Write(raw)
		zw.Close()
		derr = s.Deliver(t.S3Bucket, LogKey(t.S3Prefix, s.env.AccountID, upTo, core.RandHex(8)), buf.Bytes())
		wrote = wrote || derr == nil
		all = all[n:]
	}
	_, _ = s.Modify(t.Name, func(x *Trail) error {
		x.LastAttempt = attempt
		if derr != nil {
			x.LastDeliveryError = derr.Error()
			return nil
		}
		x.LastDeliveryError = ""
		x.Cursor = upTo
		if wrote {
			x.LastDelivery = attempt
		}
		return nil
	})
}

// Flush delivers pending events of every logging trail.
func (s *Service) Flush() {
	// Events are stamped before their audit write completes; leave a margin.
	upTo := time.Now().UTC().Add(-2 * time.Second)
	for _, t := range s.Trails() {
		if t.Logging {
			s.deliver(t, upTo)
		}
	}
}

// Run delivers logs periodically until ctx ends.
func (s *Service) Run(ctx context.Context) {
	tk := time.NewTicker(time.Minute)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
			func() {
				defer core.Recover("cloudtrail delivery")
				s.Flush()
			}()
		}
	}
}
