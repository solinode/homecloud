package ec2

import (
	"encoding/base64"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi"
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/store"
)

// EC2 launch templates: named, versioned instance settings that Auto Scaling
// groups (and RunInstances callers) launch from. A version keeps the request's
// LaunchTemplateData parameters verbatim and renders them back for Describe.

const cLaunchTemplates = "ec2_launch_templates"

type LaunchTemplate struct {
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	CreatedAt      time.Time         `json:"created_at"`
	CreatedBy      string            `json:"created_by"`
	DefaultVersion int               `json:"default_version"`
	Tags           core.Tags         `json:"tags,omitempty"`
	Versions       []LaunchTemplateV `json:"versions"`
}

type LaunchTemplateV struct {
	Number      int               `json:"number"`
	Description string            `json:"description,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
	Data        map[string]string `json:"data"` // LaunchTemplateData.* request parameters
}

func (t LaunchTemplate) latest() int { return t.Versions[len(t.Versions)-1].Number }

var ltNameRe = regexp.MustCompile(`^[a-zA-Z0-9()./_-]{3,128}$`)

func (s *Service) launchTemplate(id, name string) (LaunchTemplate, error) {
	if id != "" {
		if t, err := store.Get[LaunchTemplate](s.env.Store, cLaunchTemplates, id); err == nil {
			return t, nil
		}
		return LaunchTemplate{}, core.Errf(http.StatusBadRequest, "InvalidLaunchTemplateId.NotFound", "The specified launch template, with template ID %s, does not exist.", id)
	}
	for _, t := range store.List[LaunchTemplate](s.env.Store, cLaunchTemplates) {
		if t.Name == name {
			return t, nil
		}
	}
	return LaunchTemplate{}, core.Errf(http.StatusBadRequest, "InvalidLaunchTemplateName.NotFoundException", "At least one of the launch templates specified in the request does not exist.")
}

// version resolves "$Latest", "$Default", "" (the default) or a number.
func (t LaunchTemplate) version(v string) (LaunchTemplateV, error) {
	n := t.DefaultVersion
	switch v {
	case "", "$Default":
	case "$Latest":
		n = t.latest()
	default:
		var err error
		if n, err = strconv.Atoi(v); err != nil {
			return LaunchTemplateV{}, core.Errf(http.StatusBadRequest, "InvalidLaunchTemplateId.VersionNotFound", "The specified launch template version %s is not valid.", v)
		}
	}
	for _, x := range t.Versions {
		if x.Number == n {
			return x, nil
		}
	}
	return LaunchTemplateV{}, core.Errf(http.StatusBadRequest, "InvalidLaunchTemplateId.VersionNotFound", "Could not find launch template version %d of template %s.", n, t.ID)
}

// ResolvedTemplate is a launch template version turned into launch settings.
type ResolvedTemplate struct {
	ID, Name string
	Version  int
	Input    RunInput
}

// ResolveTemplate reads the launch settings of a template version (by ID or
// name; version is "$Latest", "$Default" or a number).
func (s *Service) ResolveTemplate(id, name, version string) (ResolvedTemplate, error) {
	t, err := s.launchTemplate(id, name)
	if err != nil {
		return ResolvedTemplate{}, err
	}
	v, err := t.version(version)
	if err != nil {
		return ResolvedTemplate{}, err
	}
	d := v.Data
	in := RunInput{ImageID: d["ImageId"], InstanceType: d["InstanceType"], KeyName: d["KeyName"], SecurityGroupIDs: listFlat(d, "SecurityGroupId"),
		Attrs: InstanceAttrs{LaunchTemplateID: t.ID, LaunchTemplateVersion: strconv.Itoa(v.Number)}}
	if ud := d["UserData"]; ud != "" {
		if b, err := base64.StdEncoding.DecodeString(ud); err == nil {
			in.UserData = string(b)
		} else {
			in.UserData = ud
		}
	}
	for _, ni := range nested(d, "NetworkInterface") {
		if ni["DeviceIndex"] == "0" || ni["DeviceIndex"] == "" {
			in.SecurityGroupIDs = append(in.SecurityGroupIDs, listOf(ni, "SecurityGroupId")...)
		}
	}
	in.IAMInstanceProfile = d["IamInstanceProfile.Arn"]
	if in.IAMInstanceProfile == "" {
		in.IAMInstanceProfile = d["IamInstanceProfile.Name"]
	}
	in.Tags = core.Tags{}
	for _, ts := range nested(d, "TagSpecification") {
		if ts["ResourceType"] != "instance" {
			continue
		}
		for _, tag := range nested(ts, "Tag") {
			if k := tag["Key"]; k != "" {
				in.Tags[k] = tag["Value"]
			}
		}
	}
	return ResolvedTemplate{ID: t.ID, Name: t.Name, Version: v.Number, Input: in}, nil
}

func listFlat(m map[string]string, name string) []string { return listOf(m, name) }

// ---- rendering ----

type ltNode struct {
	val      string
	leaf     bool
	children map[string]*ltNode
}

func (n *ltNode) child(k string) *ltNode {
	if n.children == nil {
		n.children = map[string]*ltNode{}
	}
	c := n.children[k]
	if c == nil {
		c = &ltNode{}
		n.children[k] = c
	}
	return c
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToLower(r[0])
	return string(r)
}

func numeric(s string) bool {
	_, err := strconv.Atoi(s)
	return err == nil
}

func (n *ltNode) render(parent string) any {
	if n.leaf && len(n.children) == 0 {
		return n.val
	}
	keys := make([]string, 0, len(n.children))
	allNum := len(n.children) > 0
	for k := range n.children {
		keys = append(keys, k)
		allNum = allNum && numeric(k)
	}
	if allNum {
		sort.Slice(keys, func(i, j int) bool { a, _ := strconv.Atoi(keys[i]); b, _ := strconv.Atoi(keys[j]); return a < b })
		out := awsapi.Items{}
		for _, k := range keys {
			out = append(out, n.children[k].render(parent))
		}
		return out
	}
	sort.Strings(keys)
	out := awsapi.Ordered{}
	for _, k := range keys {
		c := n.children[k]
		name := lowerFirst(k)
		if len(c.children) > 0 && !c.leaf {
			allNum := true
			for ck := range c.children {
				allNum = allNum && numeric(ck)
			}
			if allNum {
				name = lowerFirst(k) + "Set"
				if parent != "" && k == "SecurityGroupId" {
					name = "groupSet"
				}
			}
		}
		out = append(out, awsapi.KV{K: name, V: c.render(k)})
	}
	return out
}

// dataXML renders LaunchTemplateData parameters as the API's launchTemplateData.
func dataXML(d map[string]string) any {
	root := &ltNode{}
	for k, v := range d {
		n := root
		for _, part := range strings.Split(k, ".") {
			n = n.child(part)
		}
		n.leaf, n.val = true, v
	}
	return root.render("")
}

func (s *Service) templateXML(t LaunchTemplate) awsapi.Ordered {
	return awsapi.Ordered{{K: "launchTemplateId", V: t.ID}, {K: "launchTemplateName", V: t.Name}, {K: "createTime", V: t.CreatedAt},
		{K: "createdBy", V: t.CreatedBy}, {K: "defaultVersionNumber", V: t.DefaultVersion}, {K: "latestVersionNumber", V: t.latest()},
		{K: "tagSet", V: tagSet(t.Tags)}}
}

func (s *Service) templateVersionXML(t LaunchTemplate, v LaunchTemplateV, withData bool) awsapi.Ordered {
	o := awsapi.Ordered{{K: "launchTemplateId", V: t.ID}, {K: "launchTemplateName", V: t.Name}, {K: "versionNumber", V: v.Number},
		{K: "versionDescription", V: v.Description}, {K: "createTime", V: v.CreatedAt}, {K: "createdBy", V: t.CreatedBy},
		{K: "defaultVersion", V: v.Number == t.DefaultVersion}}
	if withData {
		o = append(o, awsapi.KV{K: "launchTemplateData", V: dataXML(v.Data)})
	}
	return o
}

// ---- operations ----

func (s *Service) ltOps(ops map[string]ec2Op) {
	ops["CreateLaunchTemplate"] = s.awsCreateLaunchTemplate
	ops["CreateLaunchTemplateVersion"] = s.awsCreateLaunchTemplateVersion
	ops["DescribeLaunchTemplates"] = s.awsDescribeLaunchTemplates
	ops["DescribeLaunchTemplateVersions"] = s.awsDescribeLaunchTemplateVersions
	ops["ModifyLaunchTemplate"] = s.awsModifyLaunchTemplate
	ops["DeleteLaunchTemplate"] = s.awsDeleteLaunchTemplate
	ops["DeleteLaunchTemplateVersions"] = s.awsDeleteLaunchTemplateVersions
}

func (s *Service) ltARN(q *awsapi.Req, id string) string { return q.ARN("ec2", "launch-template/"+id) }

// ltData collects the LaunchTemplateData parameters and checks what can be checked.
func (s *Service) ltData(q *awsapi.Req) (map[string]string, error) {
	d := map[string]string{}
	for k, v := range q.Form {
		if rest, ok := strings.CutPrefix(k, "LaunchTemplateData."); ok {
			d[rest] = v[0]
		}
	}
	if img := d["ImageId"]; img != "" && !strings.HasPrefix(img, "resolve:") {
		if _, err := s.image(img); err != nil {
			return nil, core.Errf(http.StatusBadRequest, "InvalidAMIID.NotFound", "The image id '[%s]' does not exist", img)
		}
	}
	if t := d["InstanceType"]; t != "" {
		if _, ok := findType(t); !ok {
			return nil, invalid("The instance type '%s' does not exist", t)
		}
	}
	for _, g := range listOf(d, "SecurityGroupId") {
		if _, err := s.vpc.GetSecurityGroup(g); err != nil {
			return nil, core.Errf(http.StatusBadRequest, "InvalidGroup.NotFound", "The security group '%s' does not exist", g)
		}
	}
	// As in AWS, putting an instance profile in a template needs iam:PassRole.
	ref := d["IamInstanceProfile.Arn"]
	if ref == "" {
		ref = d["IamInstanceProfile.Name"]
	}
	if err := s.PassProfile(q.Check, ref); err != nil {
		return nil, err
	}
	return d, nil
}

func (s *Service) awsCreateLaunchTemplate(q *awsapi.Req) (any, error) {
	name := q.Param("LaunchTemplateName")
	if err := q.Authorize("ec2:CreateLaunchTemplate", q.ARN("ec2", "launch-template/*")); err != nil {
		return nil, err
	}
	if !ltNameRe.MatchString(name) {
		return nil, core.Errf(http.StatusBadRequest, "InvalidLaunchTemplateName.MalformedException", "Launch template name must be 3-128 characters: letters, digits and ()./_-")
	}
	for _, t := range store.List[LaunchTemplate](s.env.Store, cLaunchTemplates) {
		if t.Name == name {
			return nil, core.Errf(http.StatusBadRequest, "InvalidLaunchTemplateName.AlreadyExistsException", "Launch template name already in use.")
		}
	}
	d, err := s.ltData(q)
	if err != nil {
		return nil, err
	}
	tags := tagSpecs(q, "launch-template")
	if err := checkTags(tags); err != nil {
		return nil, err
	}
	now := core.Now()
	t := LaunchTemplate{ID: core.NewID("lt"), Name: name, CreatedAt: now, DefaultVersion: 1, Tags: tags,
		Versions: []LaunchTemplateV{{Number: 1, Description: q.Param("VersionDescription"), CreatedAt: now, Data: d}}}
	if q.P != nil {
		t.CreatedBy = q.P.ARN
	}
	if err := store.Put(s.env.Store, cLaunchTemplates, t.ID, t); err != nil {
		return nil, err
	}
	return map[string]any{"launchTemplate": s.templateXML(t)}, nil
}

func (s *Service) awsCreateLaunchTemplateVersion(q *awsapi.Req) (any, error) {
	t, err := s.launchTemplate(q.Param("LaunchTemplateId"), q.Param("LaunchTemplateName"))
	if err != nil {
		return nil, err
	}
	if err := q.Authorize("ec2:CreateLaunchTemplateVersion", s.ltARN(q, t.ID)); err != nil {
		return nil, err
	}
	d, err := s.ltData(q)
	if err != nil {
		return nil, err
	}
	if src := q.Param("SourceVersion"); src != "" {
		base, err := t.version(src)
		if err != nil {
			return nil, err
		}
		merged := map[string]string{}
		for k, v := range base.Data {
			merged[k] = v
		}
		// A top-level parameter in the request replaces every value under that name.
		for k := range d {
			top, _, _ := strings.Cut(k, ".")
			for bk := range merged {
				if bt, _, _ := strings.Cut(bk, "."); bt == top {
					delete(merged, bk)
				}
			}
		}
		for k, v := range d {
			merged[k] = v
		}
		d = merged
	}
	var nv LaunchTemplateV
	t, err = store.Update(s.env.Store, cLaunchTemplates, t.ID, func(x *LaunchTemplate) error {
		nv = LaunchTemplateV{Number: x.latest() + 1, Description: q.Param("VersionDescription"), CreatedAt: core.Now(), Data: d}
		x.Versions = append(x.Versions, nv)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"launchTemplateVersion": s.templateVersionXML(t, nv, true)}, nil
}

func (s *Service) awsDescribeLaunchTemplates(q *awsapi.Req) (any, error) {
	if err := q.Authorize("ec2:DescribeLaunchTemplates", "*"); err != nil {
		return nil, err
	}
	ids, names, fs := q.List("LaunchTemplateId"), q.List("LaunchTemplateName"), filters(q)
	for _, id := range ids {
		if _, err := s.launchTemplate(id, ""); err != nil {
			return nil, err
		}
	}
	for _, n := range names {
		if _, err := s.launchTemplate("", n); err != nil {
			return nil, err
		}
	}
	all := store.List[LaunchTemplate](s.env.Store, cLaunchTemplates)
	slices.SortFunc(all, func(a, b LaunchTemplate) int { return strings.Compare(a.Name, b.Name) })
	items := awsapi.Items{}
	for _, t := range all {
		if (len(ids) > 0 && !slices.Contains(ids, t.ID)) || (len(names) > 0 && !slices.Contains(names, t.Name)) {
			continue
		}
		a := attrs{}.set("launch-template-id", t.ID).set("launch-template-name", t.Name).set("create-time", t.CreatedAt.Format(time.RFC3339)).tags(t.Tags)
		if match(fs, a) {
			items = append(items, s.templateXML(t))
		}
	}
	return map[string]any{"launchTemplates": items}, nil
}

func (s *Service) awsDescribeLaunchTemplateVersions(q *awsapi.Req) (any, error) {
	if err := q.Authorize("ec2:DescribeLaunchTemplateVersions", "*"); err != nil {
		return nil, err
	}
	id, name := q.Param("LaunchTemplateId"), q.Param("LaunchTemplateName")
	if id == "" && name == "" {
		return nil, invalid("You must specify a launch template ID or name")
	}
	t, err := s.launchTemplate(id, name)
	if err != nil {
		return nil, err
	}
	wanted := q.List("LaunchTemplateVersion")
	minV, maxV := q.ParamInt("MinVersion", 0), q.ParamInt("MaxVersion", 0)
	items := awsapi.Items{}
	for _, v := range t.Versions {
		if len(wanted) > 0 {
			hit := false
			for _, w := range wanted {
				if x, err := t.version(w); err == nil && x.Number == v.Number {
					hit = true
				}
			}
			if !hit {
				continue
			}
		}
		if (minV > 0 && v.Number < minV) || (maxV > 0 && v.Number > maxV) {
			continue
		}
		items = append(items, s.templateVersionXML(t, v, true))
	}
	if len(wanted) > 0 && len(items) == 0 {
		for _, w := range wanted {
			if _, err := t.version(w); err != nil {
				return nil, err
			}
		}
	}
	return map[string]any{"launchTemplateVersionSet": items}, nil
}

func (s *Service) awsModifyLaunchTemplate(q *awsapi.Req) (any, error) {
	t, err := s.launchTemplate(q.Param("LaunchTemplateId"), q.Param("LaunchTemplateName"))
	if err != nil {
		return nil, err
	}
	if err := q.Authorize("ec2:ModifyLaunchTemplate", s.ltARN(q, t.ID)); err != nil {
		return nil, err
	}
	if dv := q.Param("SetDefaultVersion"); dv != "" {
		v, err := t.version(dv)
		if err != nil {
			return nil, err
		}
		t, err = store.Update(s.env.Store, cLaunchTemplates, t.ID, func(x *LaunchTemplate) error { x.DefaultVersion = v.Number; return nil })
		if err != nil {
			return nil, err
		}
	}
	return map[string]any{"launchTemplate": s.templateXML(t)}, nil
}

func (s *Service) awsDeleteLaunchTemplate(q *awsapi.Req) (any, error) {
	t, err := s.launchTemplate(q.Param("LaunchTemplateId"), q.Param("LaunchTemplateName"))
	if err != nil {
		return nil, err
	}
	if err := q.Authorize("ec2:DeleteLaunchTemplate", s.ltARN(q, t.ID)); err != nil {
		return nil, err
	}
	if s.TemplateInUse != nil {
		if by := s.TemplateInUse(t.ID); by != "" {
			return nil, core.Errf(http.StatusBadRequest, "InvalidLaunchTemplate.InUse", "Launch template %s is used by %s", t.ID, by)
		}
	}
	return map[string]any{"launchTemplate": s.templateXML(t)}, store.Delete(s.env.Store, cLaunchTemplates, t.ID)
}

func (s *Service) awsDeleteLaunchTemplateVersions(q *awsapi.Req) (any, error) {
	t, err := s.launchTemplate(q.Param("LaunchTemplateId"), q.Param("LaunchTemplateName"))
	if err != nil {
		return nil, err
	}
	if err := q.Authorize("ec2:DeleteLaunchTemplateVersions", s.ltARN(q, t.ID)); err != nil {
		return nil, err
	}
	ok, failed := awsapi.Items{}, awsapi.Items{}
	var del []int
	for _, w := range q.List("LaunchTemplateVersion") {
		n, err := strconv.Atoi(w)
		if err != nil || !slices.ContainsFunc(t.Versions, func(v LaunchTemplateV) bool { return v.Number == n }) {
			failed = append(failed, awsapi.Ordered{{K: "launchTemplateId", V: t.ID}, {K: "launchTemplateName", V: t.Name}, {K: "versionNumber", V: n},
				{K: "responseError", V: awsapi.Ordered{{K: "code", V: "launchTemplateVersionDoesNotExist"}, {K: "message", V: "version does not exist"}}}})
			continue
		}
		if n == t.DefaultVersion {
			failed = append(failed, awsapi.Ordered{{K: "launchTemplateId", V: t.ID}, {K: "launchTemplateName", V: t.Name}, {K: "versionNumber", V: n},
				{K: "responseError", V: awsapi.Ordered{{K: "code", V: "unexpectedError"}, {K: "message", V: "the default version cannot be deleted"}}}})
			continue
		}
		del = append(del, n)
		ok = append(ok, awsapi.Ordered{{K: "launchTemplateId", V: t.ID}, {K: "launchTemplateName", V: t.Name}, {K: "versionNumber", V: n}})
	}
	if len(del) > 0 {
		if _, err := store.Update(s.env.Store, cLaunchTemplates, t.ID, func(x *LaunchTemplate) error {
			x.Versions = slices.DeleteFunc(x.Versions, func(v LaunchTemplateV) bool { return slices.Contains(del, v.Number) })
			return nil
		}); err != nil {
			return nil, err
		}
	}
	return map[string]any{"successfullyDeletedLaunchTemplateVersionSet": ok, "unsuccessfullyDeletedLaunchTemplateVersionSet": failed}, nil
}

// DefaultSubnetIDs returns the default subnets in the given availability zones.
func (s *Service) DefaultSubnetIDs(azs []string) ([]string, error) {
	var out []string
	for _, az := range azs {
		found := false
		for _, sn := range s.vpc.Subnets() {
			if sn.Default && sn.AvailabilityZone == az {
				out = append(out, sn.ID)
				found = true
				break
			}
		}
		if !found {
			return nil, core.BadRequest("no default subnet in availability zone %s", az)
		}
	}
	return out, nil
}

// SubnetAZ returns the availability zone of a subnet ("" if unknown).
func (s *Service) SubnetAZ(id string) string {
	sn, err := s.vpc.GetSubnet(id)
	if err != nil {
		return ""
	}
	return sn.AvailabilityZone
}
