// Package cfn implements CloudFormation-style stacks: YAML or JSON templates
// declaring resources of any HomeCloud service, with parameters, outputs,
// intrinsic functions, dependency ordering, readiness waits, rollback on
// failure, updates by replacement and ordered deletion. Every resource
// operation is an in-process API call made with the caller's permissions.
package cfn

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
	"github.com/homecloudhq/homecloud/cli/internal/svc"
	"gopkg.in/yaml.v3"
)

const cStacks = "cfn_stacks"

type Template struct {
	AWSTemplateFormatVersion string                               `json:"AWSTemplateFormatVersion,omitempty"`
	Description              string                               `json:"Description,omitempty"`
	Transform                any                                  `json:"Transform,omitempty"`
	Parameters               map[string]ParamDef                  `json:"Parameters,omitempty"`
	Mappings                 map[string]map[string]map[string]any `json:"Mappings,omitempty"`
	Conditions               map[string]any                       `json:"Conditions,omitempty"`
	Resources                map[string]ResourceDef               `json:"Resources"`
	Outputs                  map[string]map[string]any            `json:"Outputs,omitempty"`
}

// boolish accepts true/false and "true"/"false" (NoEcho: "true" is common).
type boolish bool

func (b *boolish) UnmarshalJSON(d []byte) error {
	s := strings.Trim(string(d), `"`)
	*b = boolish(strings.EqualFold(s, "true"))
	return nil
}

type ParamDef struct {
	Type                  string  `json:"Type"`
	Default               any     `json:"Default,omitempty"`
	AllowedValues         []any   `json:"AllowedValues,omitempty"`
	AllowedPattern        string  `json:"AllowedPattern,omitempty"`
	Description           string  `json:"Description,omitempty"`
	ConstraintDescription string  `json:"ConstraintDescription,omitempty"`
	MinLength             any     `json:"MinLength,omitempty"`
	MaxLength             any     `json:"MaxLength,omitempty"`
	MinValue              any     `json:"MinValue,omitempty"`
	MaxValue              any     `json:"MaxValue,omitempty"`
	NoEcho                boolish `json:"NoEcho,omitempty"`
}

type ResourceDef struct {
	Type                string         `json:"Type"`
	Properties          map[string]any `json:"Properties"`
	DependsOn           any            `json:"DependsOn,omitempty"`
	DeletionPolicy      string         `json:"DeletionPolicy,omitempty"`
	UpdateReplacePolicy string         `json:"UpdateReplacePolicy,omitempty"`
	Condition           string         `json:"Condition,omitempty"`
}

type Resource struct {
	LogicalID  string         `json:"logical_id"`
	Type       string         `json:"type"`
	HCType     string         `json:"hc_type,omitempty"` // native type behind an AWS::* resource
	NativeID   string         `json:"native_id,omitempty"`
	PhysicalID string         `json:"physical_id"`
	Status     string         `json:"status"`
	Reason     string         `json:"reason,omitempty"`
	Properties map[string]any `json:"properties,omitempty"` // resolved, as sent
	Attributes map[string]any `json:"attributes,omitempty"`
	UpdatedAt  time.Time      `json:"updated_at"`
}

// native is the ID the native API knows the resource by.
func (r *Resource) native() string {
	if r.NativeID != "" {
		return r.NativeID
	}
	return r.PhysicalID
}

func (r *Resource) nativeType() string {
	if r.HCType != "" {
		return r.HCType
	}
	return r.Type
}

type Event struct {
	ID         string    `json:"id,omitempty"`
	Time       time.Time `json:"time"`
	LogicalID  string    `json:"logical_id"`
	PhysicalID string    `json:"physical_id,omitempty"`
	Type       string    `json:"type"`
	Status     string    `json:"status"`
	Reason     string    `json:"reason,omitempty"`
}

// OutputMeta is what DescribeStacks reports about an output besides its value.
type OutputMeta struct {
	Description string `json:"description,omitempty"`
	Export      string `json:"export,omitempty"`
}

type Stack struct {
	Name                  string                `json:"name"`
	ARN                   string                `json:"arn"`
	Status                string                `json:"status"`
	StatusReason          string                `json:"status_reason,omitempty"`
	Description           string                `json:"description,omitempty"`
	Template              string                `json:"template"`
	Parameters            map[string]any        `json:"parameters"`
	Resources             map[string]*Resource  `json:"resources"`
	Order                 []string              `json:"order"` // creation order
	Outputs               map[string]any        `json:"outputs"`
	OutputMeta            map[string]OutputMeta `json:"output_meta,omitempty"`
	Events                []Event               `json:"events"`
	Tags                  core.Tags             `json:"tags,omitempty"`
	Capabilities          []string              `json:"capabilities,omitempty"`
	RoleARN               string                `json:"role_arn,omitempty"`
	Endpoint              string                `json:"endpoint,omitempty"`
	NotificationARNs      []string              `json:"notification_arns,omitempty"`
	DisableRollback       bool                  `json:"disable_rollback,omitempty"`
	TerminationProtection bool                  `json:"termination_protection,omitempty"`
	Imports               []string              `json:"imports,omitempty"` // export names this stack uses
	Rollback              *UpdateState          `json:"update_state,omitempty"` // the way back while an update runs or a rollback is unfinished
	CreatedAt             time.Time             `json:"created_at"`
	UpdatedAt             time.Time             `json:"updated_at"`
	DeletedAt             *time.Time            `json:"deleted_at,omitempty"`
	done                  chan struct{}         // closed when the operation ends (see settle)
	LastUpdated           *time.Time            `json:"last_updated,omitempty"` // last completed update
}

type Service struct {
	env *svc.Env
	// Handler is the API (set by the server) that resource operations call into.
	Handler http.Handler
	// Refresh re-reads a principal's current permissions (set by the server from IAM).
	Refresh func(*httpx.Principal) (*httpx.Principal, error)
	// RolePrincipal assumes a stack's service role (RoleARN) for CloudFormation
	// (set by the server from IAM); it fails when the role does not trust
	// cloudformation.amazonaws.com.
	RolePrincipal func(roleARN string) (*httpx.Principal, error)
	locks         sync.Map
	mu            sync.Mutex // guards stack status transitions
}

func New(env *svc.Env) *Service { return &Service{env: env} }

// ---- template parsing ----

// Parse reads a JSON or YAML template; YAML short tags (!Ref, !GetAtt, !Sub, ...) become their long forms.
func Parse(src string) (*Template, error) {
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(src), &node); err != nil {
		return nil, fmt.Errorf("template is not valid YAML or JSON: %w", err)
	}
	if len(src) > 1<<20 {
		return nil, fmt.Errorf("template is larger than 1 MB")
	}
	d := &decoder{active: map[*yaml.Node]bool{}}
	v, err := d.decode(&node)
	if err != nil {
		return nil, err
	}
	b, _ := json.Marshal(v)
	var t Template
	if err := json.Unmarshal(b, &t); err != nil {
		return nil, fmt.Errorf("template structure: %w", err)
	}
	if t.Transform != nil {
		return nil, fmt.Errorf("Template format error: template transforms (macros, AWS::Serverless) are not supported")
	}
	if len(t.Resources) == 0 {
		return nil, fmt.Errorf("Template format error: At least one Resources member must be defined.")
	}
	var unknown []string
	for id, r := range t.Resources {
		if !logicalRe.MatchString(id) {
			return nil, fmt.Errorf("Template format error: Resource name %s is non alphanumeric.", id)
		}
		if !typeRe.MatchString(r.Type) {
			unknown = append(unknown, r.Type)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, fmt.Errorf("Template format error: Unrecognized resource types: [%s]", strings.Join(unknown, ", "))
	}
	for name, def := range t.Parameters {
		if def.Type == "" {
			return nil, fmt.Errorf("Template format error: Every Parameter object must have a Type. (%s)", name)
		}
	}
	if err := checkRefs(&t); err != nil {
		return nil, err
	}
	return &t, nil
}

// typeRe is the shape of a resource type name: AWS::S3::Bucket, Custom::Thing, HC::SQS::Queue.
var typeRe = regexp.MustCompile(`^(Custom::[A-Za-z0-9_@-]{1,60}|[A-Za-z0-9]+::[A-Za-z0-9]+::[A-Za-z0-9]+(::[A-Za-z0-9]+)?)$`)

// supported reports whether HomeCloud can create resources of the type.
func supported(typ string) bool {
	if _, ok := types[typ]; ok {
		return true
	}
	_, ok := awsTypes[typ]
	return ok
}

var logicalRe = regexp.MustCompile(`^[A-Za-z0-9]{1,255}$`)

// decoder converts YAML nodes to plain values, refusing alias cycles and
// alias "bombs" that would expand to huge documents.
type decoder struct {
	active map[*yaml.Node]bool
	nodes  int
}

const maxNodes = 100000

func (d *decoder) decode(n *yaml.Node) (any, error) {
	d.nodes++
	if d.nodes > maxNodes {
		return nil, fmt.Errorf("template expands to more than %d values", maxNodes)
	}
	switch n.Kind {
	case yaml.DocumentNode:
		if len(n.Content) == 0 {
			return nil, nil
		}
		return d.decode(n.Content[0])
	case yaml.AliasNode:
		if d.active[n.Alias] {
			return nil, fmt.Errorf("template contains a recursive alias")
		}
		d.active[n.Alias] = true
		defer delete(d.active, n.Alias)
		return d.decode(n.Alias)
	}
	var v any
	switch n.Kind {
	case yaml.MappingNode:
		m := map[string]any{}
		for i := 0; i+1 < len(n.Content); i += 2 {
			val, err := d.decode(n.Content[i+1])
			if err != nil {
				return nil, err
			}
			m[n.Content[i].Value] = val
		}
		v = m
	case yaml.SequenceNode:
		arr := []any{}
		for _, c := range n.Content {
			val, err := d.decode(c)
			if err != nil {
				return nil, err
			}
			arr = append(arr, val)
		}
		v = arr
	case yaml.ScalarNode:
		if err := n.Decode(&v); err != nil || n.ShortTag() == "!!timestamp" {
			v = n.Value // dates such as 2012-10-17 stay strings
		}
		if strings.HasPrefix(n.Tag, "!") && !strings.HasPrefix(n.Tag, "!!") {
			v = n.Value // arguments of short-form intrinsics (!Ref x) are strings
		}
	}
	if strings.HasPrefix(n.Tag, "!") && !strings.HasPrefix(n.Tag, "!!") {
		tag := n.Tag[1:]
		switch tag {
		case "Ref":
			return map[string]any{"Ref": v}, nil
		case "Condition":
			return map[string]any{"Condition": v}, nil
		case "GetAtt":
			if s, ok := v.(string); ok {
				id, attr, _ := strings.Cut(s, ".")
				return map[string]any{"Fn::GetAtt": []any{id, attr}}, nil
			}
			return map[string]any{"Fn::GetAtt": v}, nil
		default:
			return map[string]any{"Fn::" + tag: v}, nil
		}
	}
	return v, nil
}
