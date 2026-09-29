package secrets

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/store"
)

// Invoker runs a Lambda function synchronously. name is the function name;
// functionError is set (e.g. "Unhandled") when the function raised.
type Invoker interface {
	Invoke(ctx context.Context, name string, payload []byte) (result []byte, functionError string, err error)
}

// InvokerFunc adapts a function to Invoker.
type InvokerFunc func(ctx context.Context, name string, payload []byte) ([]byte, string, error)

func (f InvokerFunc) Invoke(ctx context.Context, name string, payload []byte) ([]byte, string, error) {
	return f(ctx, name, payload)
}

const rotationTimeout = 15 * time.Minute

var rateRe = regexp.MustCompile(`^rate\((\d+) (hour|hours|day|days)\)$`)

// interval returns how often rules rotate a secret.
func (r *RotationRules) interval() (time.Duration, error) {
	if r == nil {
		return 0, nil
	}
	if r.ScheduleExpression != "" && r.AutomaticallyAfterDays != 0 {
		return 0, core.BadRequest("specify AutomaticallyAfterDays or ScheduleExpression, not both")
	}
	if r.AutomaticallyAfterDays != 0 {
		if r.AutomaticallyAfterDays < 1 || r.AutomaticallyAfterDays > 1000 {
			return 0, core.BadRequest("AutomaticallyAfterDays must be 1-1000")
		}
		return time.Duration(r.AutomaticallyAfterDays) * 24 * time.Hour, nil
	}
	if r.ScheduleExpression == "" {
		return 0, nil
	}
	m := rateRe.FindStringSubmatch(strings.TrimSpace(r.ScheduleExpression))
	if m == nil {
		return 0, core.BadRequest("ScheduleExpression %q is not supported: use rate(N days) or rate(N hours)", r.ScheduleExpression)
	}
	n, _ := strconv.Atoi(m[1])
	unit := 24 * time.Hour
	if strings.HasPrefix(m[2], "hour") {
		unit = time.Hour
		if n < 4 {
			return 0, core.BadRequest("the rotation interval must be at least 4 hours")
		}
	}
	if n < 1 || n > 1000 {
		return 0, core.BadRequest("the rotation interval must be 1-1000 days")
	}
	return time.Duration(n) * unit, nil
}

// functionName extracts the function name from a Lambda ARN (or returns a plain name).
func functionName(arn string) string {
	if _, rest, ok := strings.Cut(arn, ":function:"); ok {
		name, _, _ := strings.Cut(rest, ":") // drop a version or alias qualifier
		return name
	}
	return arn
}

// RotateInput configures and optionally starts rotation.
type RotateInput struct {
	Token             string
	RotationLambdaARN string
	Rules             *RotationRules
	RotateImmediately bool
}

func (s *Service) rotate(az Authz, ref string, in RotateInput) (Secret, string, error) {
	if err := validToken(in.Token); err != nil {
		return Secret{}, "", err
	}
	sec, err := s.lookup(az, "secretsmanager:RotateSecret", ref)
	if err != nil {
		return sec, "", err
	}
	if err := writable(sec); err != nil {
		return sec, "", err
	}
	fn := in.RotationLambdaARN
	if fn == "" {
		fn = sec.RotationLambdaARN
	}
	if fn == "" {
		return sec, "", core.Errf(http.StatusBadRequest, "InvalidRequestException", "No Lambda rotation function ARN is associated with this secret.")
	}
	if s.lambda() == nil {
		return sec, "", core.Errf(http.StatusBadRequest, "InvalidRequestException", "Lambda rotation functions are not available on this server.")
	}
	if !strings.HasPrefix(fn, "arn:") {
		fn = s.env.ARN("lambda", "function:"+fn)
	}
	// Secrets Manager invokes the function on the caller's behalf.
	if err := az("lambda:InvokeFunction", fn); err != nil {
		return sec, "", err
	}
	rules := sec.RotationRules
	if in.Rules != nil {
		rules = in.Rules
	}
	every, err := rules.interval()
	if err != nil {
		return sec, "", err
	}
	if s.pendingRotation(sec) {
		return sec, "", core.Errf(http.StatusBadRequest, "InvalidRequestException", "A previous rotation isn't complete. That rotation will be reattempted.")
	}
	sec, err = store.Update(s.env.Store, cSecrets, sec.Name, func(x *Secret) error {
		x.RotationEnabled, x.RotationLambdaARN, x.RotationRules = true, fn, rules
		x.NextRotation = nil
		if every > 0 {
			base := x.CreatedAt
			if x.LastRotated != nil {
				base = *x.LastRotated
			}
			n := base.Add(every)
			if in.RotateImmediately {
				n = s.now().Add(every)
			}
			x.NextRotation = &n
		}
		x.ARN = core.CanonicalARN(x.ARN)
		return nil
	})
	if err != nil || !in.RotateImmediately {
		return sec, "", err
	}
	token, err := s.startRotation(sec.Name, in.Token)
	if err != nil {
		return sec, "", err
	}
	sec, _ = s.get(sec.Name)
	return sec, token, nil
}

// pendingRotation reports an unfinished rotation: AWSPENDING on a version
// that is not AWSCURRENT, or a rotation that is running.
func (s *Service) pendingRotation(sec Secret) bool {
	s.rotMu.Lock()
	running := s.rotating[sec.Name]
	s.rotMu.Unlock()
	if running {
		return true
	}
	i := sec.staged(stagePending)
	return i >= 0 && !slices.Contains(sec.Versions[i].Stages, stageCurrent)
}

// startRotation adds the AWSPENDING placeholder version and runs the rotation
// function's four steps in the background.
// Rotating reports whether any rotation is still in progress.
func (s *Service) Rotating() bool {
	s.rotMu.Lock()
	defer s.rotMu.Unlock()
	return len(s.rotating) > 0
}

func (s *Service) startRotation(name, token string) (string, error) {
	if token == "" {
		token = uuid()
	}
	s.rotMu.Lock()
	if s.rotating[name] {
		s.rotMu.Unlock()
		return "", core.Errf(http.StatusBadRequest, "InvalidRequestException", "A previous rotation isn't complete. That rotation will be reattempted.")
	}
	s.rotating[name] = true
	s.rotMu.Unlock()
	done := func() {
		s.rotMu.Lock()
		delete(s.rotating, name)
		s.rotMu.Unlock()
	}
	sec, err := store.Update(s.env.Store, cSecrets, name, func(x *Secret) error {
		if x.version(token) >= 0 {
			return core.Errf(http.StatusConflict, "ResourceExistsException", "a version with ClientRequestToken %s already exists", token)
		}
		if i := x.staged(stagePending); i >= 0 && !slices.Contains(x.Versions[i].Stages, stageCurrent) {
			return core.Errf(http.StatusBadRequest, "InvalidRequestException", "A previous rotation isn't complete. That rotation will be reattempted.")
		}
		vs := cloneVersions(x.Versions)
		for i := range vs { // AWSPENDING left on the current version by a finished rotation
			vs[i].Stages = slices.DeleteFunc(vs[i].Stages, func(v string) bool { return v == stagePending })
		}
		x.Versions = append([]Version{{ID: token, Stages: []string{stagePending}, CreatedAt: s.now()}}, vs...)
		x.RotationError = ""
		x.ARN = core.CanonicalARN(x.ARN)
		return nil
	})
	if err != nil {
		done()
		return "", err
	}
	s.rotWG.Add(1)
	go func() {
		defer s.rotWG.Done()
		defer done()
		defer core.Recover("secret rotation")
		s.runRotation(sec, token)
	}()
	return token, nil
}

func (s *Service) runRotation(sec Secret, token string) {
	ctx, cancel := context.WithTimeout(context.Background(), rotationTimeout)
	defer cancel()
	fail := func(msg string) {
		log.Printf("secretsmanager: rotation of %s failed: %s", sec.Name, msg)
		_, _ = store.Update(s.env.Store, cSecrets, sec.Name, func(x *Secret) error { x.RotationError = msg; return nil })
	}
	for _, step := range []string{"createSecret", "setSecret", "testSecret", "finishSecret"} {
		payload, _ := json.Marshal(map[string]string{"Step": step, "SecretId": sec.ARN, "ClientRequestToken": token, "RotationToken": uuid()})
		out, fnErr, err := s.lambda().Invoke(ctx, functionName(sec.RotationLambdaARN), payload)
		if err != nil {
			fail(fmt.Sprintf("%s: %s", step, errMessage(err)))
			return
		}
		if fnErr != "" {
			fail(fmt.Sprintf("%s: the rotation function returned an error: %s", step, strings.TrimSpace(string(out))))
			return
		}
	}
	cur, err := s.get(sec.Name)
	if err != nil {
		return
	}
	i := cur.version(token)
	if i < 0 || !slices.Contains(cur.Versions[i].Stages, stageCurrent) {
		fail("finishSecret did not move AWSCURRENT to the new version")
		return
	}
	_, _ = store.Update(s.env.Store, cSecrets, sec.Name, func(x *Secret) error {
		now := s.now()
		x.LastRotated = &now
		x.RotationError = ""
		x.Versions = cloneVersions(x.Versions)
		if j := x.version(token); j >= 0 {
			x.Versions[j].Stages = slices.DeleteFunc(x.Versions[j].Stages, func(v string) bool { return v == stagePending })
		}
		if every, err := x.RotationRules.interval(); err == nil && every > 0 {
			n := now.Add(every)
			x.NextRotation = &n
		}
		return nil
	})
}

// WaitRotations blocks until running rotations finish (for tests and shutdown).
func (s *Service) WaitRotations() { s.rotWG.Wait() }

func (s *Service) cancelRotation(az Authz, ref string) (Secret, error) {
	sec, err := s.lookup(az, "secretsmanager:CancelRotateSecret", ref)
	if err != nil {
		return sec, err
	}
	if err := writable(sec); err != nil {
		return sec, err
	}
	return store.Update(s.env.Store, cSecrets, sec.Name, func(x *Secret) error {
		x.RotationEnabled, x.NextRotation = false, nil
		x.ARN = core.CanonicalARN(x.ARN)
		return nil
	})
}
