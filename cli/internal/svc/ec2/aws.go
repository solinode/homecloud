package ec2

import (
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/netip"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi"
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/store"
	"github.com/homecloudhq/homecloud/cli/internal/svc/vpc"
)

// The AWS EC2 API (ec2Query, 2016-11-15): instances, images, key pairs, EBS,
// tags, and the VPC resources (see aws_vpc.go). Operations share their logic
// with the native API.

const xmlns = "http://ec2.amazonaws.com/doc/2016-11-15/"

type ec2Op func(q *awsapi.Req) (any, error)

// RegisterAWS serves EC2 over the AWS protocol.
func (s *Service) RegisterAWS() {
	ops := map[string]ec2Op{
		"DescribeRegions":                      s.awsDescribeRegions,
		"DescribeAvailabilityZones":            s.awsDescribeAZs,
		"DescribeAccountAttributes":            s.awsDescribeAccountAttributes,
		"DescribeImages":                       s.awsDescribeImages,
		"CreateImage":                          s.awsCreateImage,
		"DeregisterImage":                      s.awsDeregisterImage,
		"DescribeInstanceTypes":                s.awsDescribeInstanceTypes,
		"RunInstances":                         s.awsRunInstances,
		"DescribeInstances":                    s.awsDescribeInstances,
		"TerminateInstances":                   s.awsTerminateInstances,
		"StopInstances":                        s.awsStopInstances,
		"StartInstances":                       s.awsStartInstances,
		"RebootInstances":                      s.awsRebootInstances,
		"DescribeInstanceStatus":               s.awsDescribeInstanceStatus,
		"DescribeInstanceAttribute":            s.awsDescribeInstanceAttribute,
		"ModifyInstanceAttribute":              s.awsModifyInstanceAttribute,
		"DescribeInstanceCreditSpecifications": s.awsDescribeCreditSpecs,
		"ModifyInstanceCreditSpecification":    s.awsModifyCreditSpec,
		"ModifyInstanceMetadataOptions":        s.awsModifyMetadataOptions,
		"GetConsoleOutput":                     s.awsGetConsoleOutput,
		"CreateKeyPair":                        s.awsCreateKeyPair,
		"ImportKeyPair":                        s.awsImportKeyPair,
		"DescribeKeyPairs":                     s.awsDescribeKeyPairs,
		"DeleteKeyPair":                        s.awsDeleteKeyPair,
		"CreateTags":                           s.awsCreateTags,
		"DeleteTags":                           s.awsDeleteTags,
		"DescribeTags":                         s.awsDescribeTags,
		"CreateVolume":                         s.awsCreateVolume,
		"DescribeVolumes":                      s.awsDescribeVolumes,
		"DeleteVolume":                         s.awsDeleteVolume,
		"AttachVolume":                         s.awsAttachVolume,
		"DetachVolume":                         s.awsDetachVolume,
		"CreateSnapshot":                       s.awsCreateSnapshot,
		"DescribeSnapshots":                    s.awsDescribeSnapshots,
		"DeleteSnapshot":                       s.awsDeleteSnapshot,
		"DescribeNetworkInterfaces":            s.awsDescribeNetworkInterfaces,
		"AllocateAddress":                      s.awsAllocateAddress,
		"DescribeAddresses":                    s.awsDescribeAddresses,
		"DescribeAddressesAttribute":           s.awsDescribeAddressesAttribute,
		"ReleaseAddress":                       s.awsReleaseAddress,
		"AssociateAddress":                     s.awsAssociateAddress,
		"DisassociateAddress":                  s.awsDisassociateAddress,
		"ModifyNetworkInterfaceAttribute":      s.awsModifyNetworkInterfaceAttribute,
		"CreateNatGateway":                     s.awsCreateNatGateway,
		"DescribeNatGateways":                  s.awsDescribeNatGateways,
		"DeleteNatGateway":                     s.awsDeleteNatGateway,
	}
	s.vpcOps(ops)
	s.ltOps(ops)
	svc := &awsapi.Service{Name: "ec2", XMLNS: xmlns, EC2: true, Ops: map[string]awsapi.Op{}}
	for name, fn := range ops {
		svc.Ops[name] = wrap(fn)
	}
	awsapi.Register(svc)
}

func wrap(fn ec2Op) awsapi.Op {
	return func(q *awsapi.Req) (any, error) {
		out, err := fn(q)
		if err != nil {
			return nil, ec2Error(err)
		}
		if out == nil {
			return map[string]any{"return": true}, nil
		}
		return out, nil
	}
}

var notFoundCodes = []struct{ kind, code string }{
	{"instance", "InvalidInstanceID.NotFound"}, {"vpc", "InvalidVpcID.NotFound"}, {"subnet", "InvalidSubnetID.NotFound"},
	{"security group", "InvalidGroup.NotFound"}, {"volume", "InvalidVolume.NotFound"}, {"image", "InvalidAMIID.NotFound"},
	{"rule", "InvalidPermission.NotFound"},
}

// ec2Error maps HomeCloud errors to EC2 error codes (all client errors are 400).
func ec2Error(err error) error {
	var ae *awsapi.Error
	if errors.As(err, &ae) {
		if ae.Code == "AccessDenied" || ae.Code == "AccessDeniedException" {
			return &awsapi.Error{Status: http.StatusForbidden, Code: "UnauthorizedOperation", Message: "You are not authorized to perform this operation. " + ae.Message}
		}
		return ae
	}
	var ce *core.Error
	if !errors.As(err, &ce) {
		return err
	}
	code := ce.Code
	switch code {
	case "ResourceNotFound":
		code = "InvalidParameterValue"
		for _, k := range notFoundCodes {
			if strings.HasPrefix(ce.Message, k.kind+" \"") {
				code = k.code
			}
		}
	case "ValidationError", "BadRequest":
		code = "InvalidParameterValue"
	case "ResourceConflict", "Conflict":
		code = "InvalidParameterValue"
	case "AccessDenied":
		return &awsapi.Error{Status: http.StatusForbidden, Code: "UnauthorizedOperation", Message: ce.Message}
	}
	status := ce.Status
	if status < 500 {
		status = http.StatusBadRequest
	}
	return &awsapi.Error{Status: status, Code: code, Message: ce.Message}
}

func vpcNotFound(id string) error {
	return core.Errf(http.StatusBadRequest, "InvalidVpcID.NotFound", "The vpc ID '%s' does not exist", id)
}

func subnetNotFound(id string) error {
	return core.Errf(http.StatusBadRequest, "InvalidSubnetID.NotFound", "The subnet ID '%s' does not exist", id)
}

func instanceNotFound(id string) error {
	return core.Errf(http.StatusBadRequest, "InvalidInstanceID.NotFound", "The instance ID '%s' does not exist", id)
}

func invalid(format string, a ...any) error {
	return core.Errf(http.StatusBadRequest, "InvalidParameterValue", format, a...)
}

// ---- request helpers ----

type filter struct {
	name   string
	values []string
}

func filters(q *awsapi.Req) []filter {
	var out []filter
	for _, m := range q.Structs("Filter") {
		f := filter{name: m["Name"]}
		for i := 1; ; i++ {
			v, ok := m["Value."+strconv.Itoa(i)]
			if !ok {
				break
			}
			f.values = append(f.values, v)
		}
		out = append(out, f)
	}
	return out
}

// attrs are a resource's filterable values.
type attrs map[string][]string

func (a attrs) set(name string, vals ...string) attrs {
	a[name] = append(a[name], vals...)
	return a
}

func (a attrs) tags(t core.Tags) attrs {
	a.set("tag-key")
	a.set("tag-value")
	for k, v := range t {
		a.set("tag:"+k, v)
		a.set("tag-key", k)
		a.set("tag-value", v)
	}
	return a
}

func globRE(p string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("^")
	for _, r := range p {
		switch r {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteString(".")
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	b.WriteString("$")
	return regexp.MustCompile(b.String())
}

// match applies EC2 filters: every filter must match one of its values
// (with * and ? wildcards). Filters a resource does not know are ignored,
// except tag filters.
func match(fs []filter, a attrs) bool {
	for _, f := range fs {
		vals, ok := a[f.name]
		if !ok {
			if strings.HasPrefix(f.name, "tag:") {
				return false
			}
			continue
		}
		hit := false
		for _, p := range f.values {
			re := globRE(p)
			for _, v := range vals {
				if re.MatchString(v) {
					hit = true
				}
			}
		}
		if !hit {
			return false
		}
	}
	return true
}

// nested returns the indexed structures "<name>.N.*" of a structure's fields.
func nested(m map[string]string, name string) []map[string]string {
	var out []map[string]string
	for i := 1; ; i++ {
		p := name + "." + strconv.Itoa(i) + "."
		sub := map[string]string{}
		for k, v := range m {
			if strings.HasPrefix(k, p) {
				sub[strings.TrimPrefix(k, p)] = v
			}
		}
		if len(sub) == 0 {
			return out
		}
		out = append(out, sub)
	}
}

func listOf(m map[string]string, name string) []string {
	var out []string
	for i := 1; ; i++ {
		v, ok := m[name+"."+strconv.Itoa(i)]
		if !ok {
			return out
		}
		out = append(out, v)
	}
}

func reqTags(q *awsapi.Req, name string) core.Tags {
	t := core.Tags{}
	for _, m := range q.Structs(name) {
		if k := m["Key"]; k != "" {
			t[k] = m["Value"]
		}
	}
	return t
}

// tagSpecs returns the TagSpecification tags for a resource type.
func tagSpecs(q *awsapi.Req, resourceType string) core.Tags {
	t := core.Tags{}
	for _, m := range q.Structs("TagSpecification") {
		if m["ResourceType"] != resourceType {
			continue
		}
		for _, tag := range nested(m, "Tag") {
			if k := tag["Key"]; k != "" {
				t[k] = tag["Value"]
			}
		}
	}
	return t
}

func tagSet(t core.Tags) awsapi.Items {
	keys := make([]string, 0, len(t))
	for k := range t {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := awsapi.Items{}
	for _, k := range keys {
		out = append(out, map[string]any{"key": k, "value": t[k]})
	}
	return out
}

func boolParam(q *awsapi.Req, name string) (bool, bool) {
	for _, k := range []string{name, name + ".Value"} {
		switch strings.ToLower(q.Param(k)) {
		case "true":
			return true, true
		case "false":
			return false, true
		}
	}
	return false, false
}

func idSet(ids []string) map[string]bool {
	m := map[string]bool{}
	for _, id := range ids {
		m[id] = true
	}
	return m
}

// ---- regions, zones, account ----

func (s *Service) region() string { return s.env.Cfg.Region }

func (s *Service) awsDescribeRegions(q *awsapi.Req) (any, error) {
	if err := q.Authorize("ec2:DescribeRegions", "*"); err != nil {
		return nil, err
	}
	a := attrs{}.set("region-name", s.region()).set("endpoint", "ec2."+s.region()+".amazonaws.com").set("opt-in-status", "opt-in-not-required")
	items := awsapi.Items{}
	names := q.List("RegionName")
	if match(filters(q), a) && (len(names) == 0 || slices.Contains(names, s.region())) {
		items = append(items, map[string]any{"regionName": s.region(), "regionEndpoint": "ec2." + s.region() + ".amazonaws.com", "optInStatus": "opt-in-not-required"})
	}
	return map[string]any{"regionInfo": items}, nil
}

var zoneLetters = []string{"a", "b", "c"}

func (s *Service) awsDescribeAZs(q *awsapi.Req) (any, error) {
	if err := q.Authorize("ec2:DescribeAvailabilityZones", "*"); err != nil {
		return nil, err
	}
	names, ids, fs := idSet(q.List("ZoneName")), idSet(q.List("ZoneId")), filters(q)
	items := awsapi.Items{}
	for _, l := range zoneLetters {
		z := s.region() + l
		a := attrs{}.set("zone-name", z).set("zone-id", azID(z)).set("state", "available").set("region-name", s.region()).
			set("zone-type", "availability-zone").set("group-name", s.region()).set("opt-in-status", "opt-in-not-required")
		if (len(names) > 0 && !names[z]) || (len(ids) > 0 && !ids[azID(z)]) || !match(fs, a) {
			continue
		}
		items = append(items, map[string]any{"zoneName": z, "zoneState": "available", "regionName": s.region(), "zoneId": azID(z),
			"zoneType": "availability-zone", "groupName": s.region(), "networkBorderGroup": s.region(), "optInStatus": "opt-in-not-required",
			"messageSet": awsapi.Items{}})
	}
	return map[string]any{"availabilityZoneInfo": items}, nil
}

func (s *Service) awsDescribeAccountAttributes(q *awsapi.Req) (any, error) {
	if err := q.Authorize("ec2:DescribeAccountAttributes", "*"); err != nil {
		return nil, err
	}
	def := s.vpc.DefaultVPCID()
	if def == "" {
		def = "none"
	}
	all := []struct{ name, value string }{
		{"supported-platforms", "VPC"}, {"default-vpc", def}, {"max-instances", "20"},
		{"vpc-max-security-groups-per-interface", "5"}, {"max-elastic-ips", "5"}, {"vpc-max-elastic-ips", "5"},
	}
	want := idSet(q.List("AttributeName"))
	items := awsapi.Items{}
	for _, a := range all {
		if len(want) > 0 && !want[a.name] {
			continue
		}
		items = append(items, map[string]any{"attributeName": a.name, "attributeValueSet": awsapi.Items{map[string]any{"attributeValue": a.value}}})
	}
	return map[string]any{"accountAttributeSet": items}, nil
}

// ---- images and instance types ----

// hypervisor is what the EC2 API reports: instances that run as containers keep
// the "xen" of the real service's older families; virtual machines are QEMU/KVM.
func hypervisor(vm bool) string {
	if vm {
		return "kvm"
	}
	return "xen"
}

func (s *Service) awsName(im Image) string {
	if im.AWSName == "" {
		return im.Name
	}
	deb, rpm := "amd64", "x86_64"
	if hostArch(s) == "arm64" {
		deb, rpm = "arm64", "aarch64"
	}
	return strings.NewReplacer("{deb}", deb, "{rpm}", rpm).Replace(im.AWSName)
}

func (s *Service) imageOwner(im Image) (id, alias string) {
	if im.Owner != "homecloud" {
		return s.env.AccountID, ""
	}
	if im.OwnerID != "" {
		return im.OwnerID, ""
	}
	return ownerAmazon, "amazon"
}

func (s *Service) allImages() []Image {
	out := []Image{}
	for _, im := range catalog {
		im.Owner, im.State = "homecloud", "available"
		out = append(out, im.normalized())
	}
	for _, im := range store.List[Image](s.env.Store, cImages) {
		out = append(out, im.normalized())
	}
	return out
}

func (s *Service) imageXML(im Image) map[string]any {
	owner, alias := s.imageOwner(im)
	created := im.CreatedAt
	if created == "" {
		created = "2024-01-01T00:00:00.000Z"
	}
	m := map[string]any{
		"imageId": im.ID, "imageLocation": owner + "/" + s.awsName(im), "imageState": im.State, "imageOwnerId": owner,
		"creationDate": created, "isPublic": im.Owner == "homecloud", "architecture": hostArch(s), "imageType": "machine",
		"platformDetails": "Linux/UNIX", "usageOperation": "RunInstances", "name": s.awsName(im), "description": im.Description,
		"rootDeviceType": "ebs", "rootDeviceName": "/dev/xvda", "virtualizationType": "hvm", "hypervisor": hypervisor(im.IsVM()), "enaSupport": true,
		"bootMode": "uefi-preferred", "tagSet": tagSet(im.Tags), "productCodes": awsapi.Items{},
		"blockDeviceMapping": awsapi.Items{map[string]any{"deviceName": "/dev/xvda",
			"ebs": map[string]any{"volumeSize": 8, "deleteOnTermination": true, "volumeType": "gp3", "encrypted": false}}},
	}
	if alias != "" {
		m["imageOwnerAlias"] = alias
	}
	return m
}

func (s *Service) awsDescribeImages(q *awsapi.Req) (any, error) {
	if err := q.Authorize("ec2:DescribeImages", "*"); err != nil {
		return nil, err
	}
	ids, owners, fs := q.List("ImageId"), q.List("Owner"), filters(q)
	want := idSet(ids)
	items := awsapi.Items{}
	found := map[string]bool{}
	for _, im := range s.allImages() {
		if len(ids) > 0 && !want[im.ID] {
			continue
		}
		owner, alias := s.imageOwner(im)
		if len(owners) > 0 && !slices.ContainsFunc(owners, func(o string) bool {
			return o == owner || (o == "self" && im.Owner != "homecloud") || (o == "amazon" && (alias == "amazon" || im.Owner == "homecloud"))
		}) {
			continue
		}
		a := attrs{}.set("image-id", im.ID).set("name", s.awsName(im)).set("owner-id", owner).set("owner-alias", alias).
			set("state", im.State).set("architecture", hostArch(s)).set("virtualization-type", "hvm").set("root-device-type", "ebs").
			set("root-device-name", "/dev/xvda").set("image-type", "machine").set("is-public", strconv.FormatBool(im.Owner == "homecloud")).
			set("platform-details", "Linux/UNIX").set("description", im.Description).set("hypervisor", hypervisor(im.IsVM())).
			set("block-device-mapping.volume-type", "gp3").set("ena-support", "true").tags(im.Tags)
		if !match(fs, a) {
			continue
		}
		found[im.ID] = true
		items = append(items, s.imageXML(im))
	}
	for _, id := range ids {
		if !found[id] && len(owners) == 0 && len(fs) == 0 {
			return nil, core.Errf(http.StatusBadRequest, "InvalidAMIID.NotFound", "The image id '[%s]' does not exist", id)
		}
	}
	return map[string]any{"imagesSet": items}, nil
}

func (s *Service) awsCreateImage(q *awsapi.Req) (any, error) {
	id := q.Param("InstanceId")
	if err := q.Authorize("ec2:CreateImage", q.ARN("ec2", "instance/"+id)); err != nil {
		return nil, err
	}
	im, err := s.CreateImage(id, q.Param("Name"), q.Param("Description"), tagSpecs(q, "image"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"imageId": im.ID}, nil
}

func (s *Service) awsDeregisterImage(q *awsapi.Req) (any, error) {
	id := q.Param("ImageId")
	if err := q.Authorize("ec2:DeregisterImage", q.ARN("ec2", "image/"+id)); err != nil {
		return nil, err
	}
	if err := s.DeregisterImage(id); err != nil {
		return nil, core.Errf(http.StatusBadRequest, "InvalidAMIID.NotFound", "The image id '[%s]' does not exist", id)
	}
	return nil, nil
}

func (s *Service) typeXML(t InstanceType) map[string]any {
	vcpus := int(math.Max(1, math.Ceil(t.VCPUs)))
	if strings.HasPrefix(t.Name, "t3.") && vcpus < 2 {
		vcpus = 2 // as in EC2: t3 sizes have two vCPUs and a CPU credit balance
	}
	cores := max(1, vcpus/2)
	return map[string]any{
		"instanceType": t.Name, "currentGeneration": true, "freeTierEligible": t.Name == "t3.micro",
		"supportedUsageClasses": awsapi.Items{"on-demand", "spot"}, "supportedRootDeviceTypes": awsapi.Items{"ebs"},
		"supportedVirtualizationTypes": awsapi.Items{"hvm"}, "bareMetal": false, "hypervisor": "nitro",
		"processorInfo":            map[string]any{"supportedArchitectures": awsapi.Items{hostArch(s)}, "sustainedClockSpeedInGhz": 2.5},
		"vCpuInfo":                 map[string]any{"defaultVCpus": vcpus, "defaultCores": cores, "defaultThreadsPerCore": vcpus / cores},
		"memoryInfo":               map[string]any{"sizeInMiB": t.MemoryMB},
		"instanceStorageSupported": false,
		"ebsInfo":                  map[string]any{"ebsOptimizedSupport": "default", "encryptionSupport": "supported", "nvmeSupport": "required"},
		"networkInfo": map[string]any{"networkPerformance": "Up to 5 Gigabit", "maximumNetworkInterfaces": 3, "maximumNetworkCards": 1,
			"defaultNetworkCardIndex": 0, "ipv4AddressesPerInterface": 6, "ipv6AddressesPerInterface": 6, "ipv6Supported": true,
			"enaSupport": "required", "efaSupported": false, "encryptionInTransitSupported": false},
		"placementGroupInfo":            map[string]any{"supportedStrategies": awsapi.Items{"partition", "spread"}},
		"hibernationSupported":          false,
		"burstablePerformanceSupported": strings.HasPrefix(t.Name, "t"),
		"dedicatedHostsSupported":       false, "autoRecoverySupported": true,
		"supportedBootModes": awsapi.Items{"legacy-bios", "uefi"},
	}
}

func (s *Service) awsDescribeInstanceTypes(q *awsapi.Req) (any, error) {
	if err := q.Authorize("ec2:DescribeInstanceTypes", "*"); err != nil {
		return nil, err
	}
	names, fs := q.List("InstanceType"), filters(q)
	for _, n := range names {
		if _, ok := findType(n); !ok {
			return nil, invalid("The following supplied instance types do not exist: [%s]", n)
		}
	}
	items := awsapi.Items{}
	for _, t := range instanceTypes {
		a := attrs{}.set("instance-type", t.Name).set("current-generation", "true").set("processor-info.supported-architecture", hostArch(s)).
			set("memory-info.size-in-mib", strconv.FormatInt(t.MemoryMB, 10)).set("burstable-performance-supported", strconv.FormatBool(strings.HasPrefix(t.Name, "t")))
		if (len(names) > 0 && !slices.Contains(names, t.Name)) || !match(fs, a) {
			continue
		}
		items = append(items, s.typeXML(t))
	}
	return map[string]any{"instanceTypeSet": items}, nil
}

// ---- instances ----

var stateCodes = map[string]int{"pending": 0, "running": 16, "shutting-down": 32, "terminated": 48, "stopping": 64, "stopped": 80}

func stateXML(state string) map[string]any {
	return map[string]any{"code": stateCodes[state], "name": state}
}

// macFor is an instance's MAC address as Docker derives it from the address.
func macFor(ip string) string {
	if a, err := netip.ParseAddr(ip); err == nil && a.Is4() {
		b := a.As4()
		return fmt.Sprintf("02:42:%02x:%02x:%02x:%02x", b[0], b[1], b[2], b[3])
	}
	return "02:42:00:00:00:00"
}

func (s *Service) groupSet(ids []string) awsapi.Items {
	out := awsapi.Items{}
	for _, id := range ids {
		name := ""
		if g, err := s.vpc.GetSecurityGroup(id); err == nil {
			name = g.Name
		}
		out = append(out, map[string]any{"groupId": id, "groupName": name})
	}
	return out
}

func (s *Service) eniXML(i Instance, withInstance bool) map[string]any {
	eni := eniID(i.ID)
	pub := s.publicIP(i)
	m := map[string]any{
		"networkInterfaceId": eni, "subnetId": i.SubnetID, "vpcId": i.VpcID, "availabilityZone": i.AvailabilityZone, "description": "",
		"ownerId": s.env.AccountID, "requesterManaged": false, "status": "in-use", "macAddress": macFor(i.PrivateIP),
		"privateIpAddress": i.PrivateIP, "privateDnsName": i.PrivateDNS, "sourceDestCheck": !i.SourceDestCheckOff,
		"groupSet": s.groupSet(i.SecurityGroups), "interfaceType": "interface", "tagSet": awsapi.Items{},
		"ipv6AddressesSet":      awsapi.Items{},
		"privateIpAddressesSet": awsapi.Items{map[string]any{"privateIpAddress": i.PrivateIP, "privateDnsName": i.PrivateDNS, "primary": true}},
		"attachment": map[string]any{"attachmentId": "eni-attach-" + strings.TrimPrefix(i.ID, "i-"), "deviceIndex": 0, "networkCardIndex": 0,
			"status": "attached", "attachTime": i.LaunchTime, "deleteOnTermination": true, "instanceOwnerId": s.env.AccountID},
	}
	if withInstance {
		m["attachment"].(map[string]any)["instanceId"] = i.ID
	}
	if pub != "" {
		m["association"] = map[string]any{"publicIp": pub, "ipOwnerId": "amazon", "publicDnsName": i.PublicHost}
	}
	return m
}

func (s *Service) instanceXML(i Instance) map[string]any {
	md := i.Metadata.withDefaults()
	bdm := awsapi.Items{}
	for _, v := range i.Volumes {
		if v.Device == "" {
			continue
		}
		st := "attached"
		if vol, err := store.Get[Volume](s.env.Store, cVolumes, v.VolumeID); err == nil && vol.AttachState != "" {
			st = vol.AttachState
		}
		bdm = append(bdm, map[string]any{"deviceName": v.Device, "ebs": map[string]any{"volumeId": v.VolumeID, "status": st,
			"attachTime": awsapi.T(v.AttachTime), "deleteOnTermination": v.DeleteOnTermination}})
	}
	monitoring := "disabled"
	if i.Monitoring {
		monitoring = "enabled"
	}
	vcpus := max(1, int(math.Ceil(i.VCPUs)))
	m := map[string]any{
		"instanceId": i.ID, "imageId": i.ImageID, "instanceState": stateXML(i.State), "privateDnsName": i.PrivateDNS,
		"dnsName": "", "reason": "", "amiLaunchIndex": i.LaunchIndex, "productCodes": awsapi.Items{}, "instanceType": i.InstanceType,
		"launchTime": i.LaunchTime, "placement": map[string]any{"availabilityZone": i.AvailabilityZone, "groupName": "", "tenancy": "default"},
		"monitoring": map[string]any{"state": monitoring}, "architecture": hostArch(s), "rootDeviceType": "ebs", "rootDeviceName": "/dev/xvda",
		"blockDeviceMapping": bdm, "virtualizationType": "hvm", "clientToken": i.ClientToken, "tagSet": tagSet(s.tagsOf(i.ID, i.Name, i.Tags)),
		"hypervisor": hypervisor(i.IsVM()), "ebsOptimized": i.EBSOptimized, "enaSupport": true, "sourceDestCheck": !i.SourceDestCheckOff,
		"groupSet": s.groupSet(i.SecurityGroups), "platformDetails": "Linux/UNIX", "usageOperation": "RunInstances",
		"usageOperationUpdateTime": i.LaunchTime, "bootMode": "uefi", "currentInstanceBootMode": "uefi",
		"cpuOptions":         map[string]any{"coreCount": max(1, vcpus/2), "threadsPerCore": min(2, vcpus)},
		"hibernationOptions": map[string]any{"configured": false}, "enclaveOptions": map[string]any{"enabled": false},
		"capacityReservationSpecification": map[string]any{"capacityReservationPreference": "open"},
		"metadataOptions": map[string]any{"state": "applied", "httpTokens": md.HttpTokens, "httpPutResponseHopLimit": md.HopLimit,
			"httpEndpoint": md.HttpEndpoint, "httpProtocolIpv6": "disabled", "instanceMetadataTags": md.InstanceMetadataTags},
		"privateDnsNameOptions": map[string]any{"hostnameType": "ip-name", "enableResourceNameDnsARecord": false, "enableResourceNameDnsAAAARecord": false},
		"maintenanceOptions":    map[string]any{"autoRecovery": "default"},
	}
	if i.KeyName != "" {
		m["keyName"] = i.KeyName
	}
	if i.State != "terminated" {
		m["subnetId"], m["vpcId"], m["privateIpAddress"] = i.SubnetID, i.VpcID, i.PrivateIP
		m["networkInterfaceSet"] = awsapi.Items{s.eniXML(i, false)}
		if ip := s.publicIP(i); ip != "" {
			m["ipAddress"] = ip
			m["dnsName"] = i.PublicHost
		}
	} else {
		m["privateDnsName"] = ""
		m["networkInterfaceSet"] = awsapi.Items{}
	}
	if i.IAMProfileARN != "" {
		m["iamInstanceProfile"] = map[string]any{"arn": i.IAMProfileARN, "id": i.IAMProfileID}
	}
	if i.StateReason != "" {
		code := i.StateReason
		if n := strings.IndexByte(code, ':'); n > 0 {
			code = code[:n]
		}
		m["stateReason"] = map[string]any{"code": code, "message": i.StateReason}
		m["reason"] = i.StateReason
	}
	return m
}

func (s *Service) instanceAttrs(i Instance) attrs {
	a := attrs{}.set("instance-id", i.ID).set("instance-state-name", i.State).set("instance-state-code", strconv.Itoa(stateCodes[i.State])).
		set("instance-type", i.InstanceType).set("image-id", i.ImageID).set("vpc-id", i.VpcID).set("subnet-id", i.SubnetID).
		set("private-ip-address", i.PrivateIP).set("private-dns-name", i.PrivateDNS).set("availability-zone", i.AvailabilityZone).
		set("reservation-id", i.ReservationID).set("key-name", i.KeyName).set("architecture", hostArch(s)).
		set("instance.group-id", i.SecurityGroups...).set("group-id", i.SecurityGroups...).set("iam-instance-profile.arn", i.IAMProfileARN).
		set("network-interface.network-interface-id", eniID(i.ID)).set("client-token", i.ClientToken).set("launch-index", strconv.Itoa(i.LaunchIndex)).
		set("root-device-type", "ebs").set("virtualization-type", "hvm").set("owner-id", s.env.AccountID)
	if ip := s.publicIP(i); ip != "" {
		a.set("ip-address", ip)
	}
	for _, v := range i.Volumes {
		a.set("block-device-mapping.volume-id", v.VolumeID)
	}
	return a.tags(s.tagsOf(i.ID, i.Name, i.Tags))
}

// awsInstances resolves InstanceId.N (all must exist) and applies filters.
func (s *Service) awsInstances(q *awsapi.Req) ([]Instance, error) {
	ids, fs := q.List("InstanceId"), filters(q)
	all := s.list()
	slices.SortStableFunc(all, func(a, b Instance) int { return a.LaunchTime.Compare(b.LaunchTime) })
	byID := map[string]Instance{}
	for _, i := range all {
		byID[i.ID] = i
	}
	for _, id := range ids {
		if _, ok := byID[id]; !ok {
			return nil, instanceNotFound(id)
		}
	}
	want := idSet(ids)
	out := []Instance{}
	for _, i := range all {
		if (len(ids) == 0 || want[i.ID]) && match(fs, s.instanceAttrs(i)) {
			out = append(out, i)
		}
	}
	return out, nil
}

func (s *Service) reservationXML(id string, insts []Instance) map[string]any {
	items := awsapi.Items{}
	for _, i := range insts {
		items = append(items, s.instanceXML(i))
	}
	return map[string]any{"reservationId": id, "ownerId": s.env.AccountID, "groupSet": awsapi.Items{}, "instancesSet": items}
}

func (s *Service) awsDescribeInstances(q *awsapi.Req) (any, error) {
	if err := q.Authorize("ec2:DescribeInstances", "*"); err != nil {
		return nil, err
	}
	insts, err := s.awsInstances(q)
	if err != nil {
		return nil, err
	}
	var order []string
	groups := map[string][]Instance{}
	for _, i := range insts {
		r := i.ReservationID
		if r == "" {
			r = "r-" + strings.TrimPrefix(i.ID, "i-")
		}
		if _, ok := groups[r]; !ok {
			order = append(order, r)
		}
		groups[r] = append(groups[r], i)
	}
	items := awsapi.Items{}
	for _, r := range order {
		items = append(items, s.reservationXML(r, groups[r]))
	}
	return map[string]any{"reservationSet": items}, nil
}

func (s *Service) awsRunInstances(q *awsapi.Req) (any, error) {
	if err := q.Authorize("ec2:RunInstances", q.ARN("ec2", "instance/*")); err != nil {
		return nil, err
	}
	in := RunInput{ImageID: q.Param("ImageId"), InstanceType: q.Param("InstanceType"), SubnetID: q.Param("SubnetId"),
		KeyName: q.Param("KeyName"), SecurityGroupIDs: q.List("SecurityGroupId"), ExactName: true}
	minCount, maxCount := q.ParamInt("MinCount", 1), q.ParamInt("MaxCount", 1)
	if minCount < 1 || maxCount < minCount {
		return nil, invalid("MinCount must be at least 1 and no more than MaxCount")
	}
	if minCount > 20 {
		return nil, core.Errf(http.StatusBadRequest, "InstanceLimitExceeded", "You have requested more instances (%d) than your current instance limit of 20 allows", minCount)
	}
	in.Count = min(maxCount, 20)
	if tok := q.Param("ClientToken"); tok != "" {
		var prev []Instance
		for _, i := range s.list() {
			if i.ClientToken == tok {
				prev = append(prev, i)
			}
		}
		if len(prev) > 0 {
			return s.reservationXML(prev[0].ReservationID, prev), nil
		}
		in.Attrs.ClientToken = tok
	}
	// A primary network interface may carry the subnet and groups instead.
	for _, ni := range q.Structs("NetworkInterface") {
		if ni["DeviceIndex"] != "" && ni["DeviceIndex"] != "0" {
			continue
		}
		if ni["SubnetId"] != "" {
			in.SubnetID = ni["SubnetId"]
		}
		in.SecurityGroupIDs = append(in.SecurityGroupIDs, listOf(ni, "SecurityGroupId")...)
	}
	if az := q.Param("Placement.AvailabilityZone"); az != "" && in.SubnetID == "" {
		for _, sn := range s.vpc.Subnets() {
			if sn.Default && sn.AvailabilityZone == az {
				in.SubnetID = sn.ID
			}
		}
		if in.SubnetID == "" {
			return nil, invalid("no default subnet in availability zone %s", az)
		}
	}
	if in.SubnetID != "" {
		if _, err := s.vpc.GetSubnet(in.SubnetID); err != nil {
			return nil, subnetNotFound(in.SubnetID)
		}
	}
	if names := q.List("SecurityGroup"); len(names) > 0 {
		vpcID, err := s.vpc.SubnetVPC(in.SubnetID)
		if err != nil {
			return nil, err
		}
		for _, n := range names {
			id := ""
			for _, g := range s.vpc.SecurityGroups() {
				if g.VpcID == vpcID && g.Name == n {
					id = g.ID
				}
			}
			if id == "" {
				return nil, core.Errf(http.StatusBadRequest, "InvalidGroup.NotFound", "The security group '%s' does not exist in VPC '%s'", n, vpcID)
			}
			in.SecurityGroupIDs = append(in.SecurityGroupIDs, id)
		}
	}
	for _, g := range in.SecurityGroupIDs {
		if _, err := s.vpc.GetSecurityGroup(g); err != nil {
			return nil, core.Errf(http.StatusBadRequest, "InvalidGroup.NotFound", "The security group '%s' does not exist", g)
		}
	}
	if in.ImageID != "" {
		if _, err := s.image(in.ImageID); err != nil {
			return nil, core.Errf(http.StatusBadRequest, "InvalidAMIID.NotFound", "The image id '[%s]' does not exist", in.ImageID)
		}
	}
	if ud := q.Param("UserData"); ud != "" {
		if b, err := base64.StdEncoding.DecodeString(ud); err == nil {
			in.UserData = string(b)
		} else {
			in.UserData = ud
		}
	}
	if ref := q.Param("IamInstanceProfile.Arn"); ref != "" {
		in.IAMInstanceProfile = ref
	} else if ref := q.Param("IamInstanceProfile.Name"); ref != "" {
		in.IAMInstanceProfile = ref
	}
	if in.IAMInstanceProfile != "" {
		if s.Roles == nil {
			return nil, invalid("instance profiles are not available")
		}
		_, _, role, err := s.Roles.InstanceProfile(in.IAMInstanceProfile)
		if err != nil {
			return nil, invalid("Value (%s) for parameter iamInstanceProfile is invalid. Invalid IAM Instance Profile name", in.IAMInstanceProfile)
		}
		if role != "" {
			if err := q.Check("iam:PassRole", role); err != nil {
				return nil, err
			}
		}
	}
	in.Metadata = MetadataOptions{HttpTokens: q.Param("MetadataOptions.HttpTokens"), HttpEndpoint: q.Param("MetadataOptions.HttpEndpoint"),
		HopLimit: q.ParamInt("MetadataOptions.HttpPutResponseHopLimit", 0), InstanceMetadataTags: q.Param("MetadataOptions.InstanceMetadataTags")}
	for _, m := range q.Structs("BlockDeviceMapping") {
		dev := m["DeviceName"]
		if dev == "/dev/xvda" || dev == "/dev/sda1" || dev == "/dev/sda" {
			// The root device is the instance's own disk. Container instances have
			// no separate root volume; VM instances size and keep theirs by it.
			if m["NoDevice"] == "" && m["VirtualName"] == "" {
				root := VolumeSpec{Device: dev, VolumeType: m["Ebs.VolumeType"], Iops: atoi(m["Ebs.Iops"]), Throughput: atoi(m["Ebs.Throughput"]),
					SizeGB: atoi(m["Ebs.VolumeSize"]), Encrypted: m["Ebs.Encrypted"] == "true", KMSKeyID: m["Ebs.KmsKeyId"]}
				del := m["Ebs.DeleteOnTermination"] != "false"
				root.DeleteOnTermination = &del
				in.RootVolume = &root
			}
			continue
		}
		if dev == "" || m["NoDevice"] != "" || m["VirtualName"] != "" {
			continue
		}
		if !deviceRE.MatchString(dev) {
			return nil, invalid("Invalid device name %s", dev)
		}
		v := VolumeSpec{Device: dev, MountPath: devicePath(dev), SnapshotID: m["Ebs.SnapshotId"], VolumeType: m["Ebs.VolumeType"],
			KMSKeyID: m["Ebs.KmsKeyId"], Encrypted: m["Ebs.Encrypted"] == "true"}
		v.SizeGB, _ = strconv.Atoi(m["Ebs.VolumeSize"])
		v.Iops, _ = strconv.Atoi(m["Ebs.Iops"])
		v.Throughput, _ = strconv.Atoi(m["Ebs.Throughput"])
		del := m["Ebs.DeleteOnTermination"] != "false"
		v.DeleteOnTermination = &del
		in.Volumes = append(in.Volumes, v)
	}
	in.Tags = tagSpecs(q, "instance")
	if err := checkTags(in.Tags); err != nil {
		return nil, err
	}
	in.Name = in.Tags["Name"]
	delete(in.Tags, "Name")
	in.VolumeTags = tagSpecs(q, "volume")
	a := &in.Attrs
	a.DisableAPITermination, _ = boolParam(q, "DisableApiTermination")
	a.DisableAPIStop, _ = boolParam(q, "DisableApiStop")
	a.Monitoring, _ = boolParam(q, "Monitoring.Enabled")
	a.EBSOptimized, _ = boolParam(q, "EbsOptimized")
	a.ShutdownBehavior = q.Param("InstanceInitiatedShutdownBehavior")
	a.CPUCredits = q.Param("CreditSpecification.CpuCredits")
	a.LaunchTemplateID = q.Param("LaunchTemplate.LaunchTemplateId")
	a.LaunchTemplateVersion = q.Param("LaunchTemplate.Version")
	if a.ShutdownBehavior != "" && a.ShutdownBehavior != "stop" && a.ShutdownBehavior != "terminate" {
		return nil, invalid("InstanceInitiatedShutdownBehavior must be stop or terminate")
	}
	a.ReservationID = core.NewID("r")
	launched, err := s.Launch(in)
	if err != nil {
		for _, i := range launched {
			_, _ = s.Terminate(i.ID)
		}
		return nil, err
	}
	return s.reservationXML(a.ReservationID, launched), nil
}

// stateChange runs fn on each instance and reports its previous and current state.
func (s *Service) stateChange(q *awsapi.Req, action string, fn func(i Instance) error) (any, error) {
	ids := q.List("InstanceId")
	if len(ids) == 0 {
		return nil, core.Errf(http.StatusBadRequest, "MissingParameter", "The request must contain the parameter InstanceId")
	}
	var insts []Instance
	for _, id := range ids {
		if err := q.Authorize(action, q.ARN("ec2", "instance/"+id)); err != nil {
			return nil, err
		}
		i, err := s.get(id)
		if err != nil {
			return nil, instanceNotFound(id)
		}
		insts = append(insts, i)
	}
	items := awsapi.Items{}
	for _, i := range insts {
		if err := fn(i); err != nil {
			return nil, err
		}
		cur, _ := s.get(i.ID)
		items = append(items, map[string]any{"instanceId": i.ID, "previousState": stateXML(i.State), "currentState": stateXML(cur.State)})
	}
	return map[string]any{"instancesSet": items}, nil
}

func (s *Service) awsTerminateInstances(q *awsapi.Req) (any, error) {
	return s.stateChange(q, "ec2:TerminateInstances", func(i Instance) error {
		if i.DisableAPITermination {
			return core.Errf(http.StatusBadRequest, "OperationNotPermitted", "The instance '%s' may not be terminated. Modify its 'disableApiTermination' instance attribute and try again.", i.ID)
		}
		_, err := s.Terminate(i.ID)
		return err
	})
}

func (s *Service) awsStopInstances(q *awsapi.Req) (any, error) {
	force, _ := boolParam(q, "Force")
	return s.stateChange(q, "ec2:StopInstances", func(i Instance) error {
		switch i.State {
		case "stopped", "stopping":
			return nil
		case "running":
			_, err := s.StopInstance(i.ID, force)
			return err
		}
		return core.Errf(http.StatusBadRequest, "IncorrectInstanceState", "This instance '%s' is not in a state from which it can be stopped.", i.ID)
	})
}

func (s *Service) awsStartInstances(q *awsapi.Req) (any, error) {
	return s.stateChange(q, "ec2:StartInstances", func(i Instance) error {
		switch i.State {
		case "running", "pending":
			return nil
		case "stopped":
			_, err := s.StartInstance(i.ID)
			return err
		}
		return core.Errf(http.StatusBadRequest, "IncorrectInstanceState", "The instance '%s' is not in a state from which it can be started.", i.ID)
	})
}

func (s *Service) awsRebootInstances(q *awsapi.Req) (any, error) {
	if _, err := s.stateChange(q, "ec2:RebootInstances", func(i Instance) error {
		if i.State != "running" {
			return core.Errf(http.StatusBadRequest, "IncorrectInstanceState", "The instance '%s' is not in the 'running' state.", i.ID)
		}
		_, err := s.RebootInstance(i.ID)
		return err
	}); err != nil {
		return nil, err
	}
	return nil, nil
}

func (s *Service) awsDescribeInstanceStatus(q *awsapi.Req) (any, error) {
	if err := q.Authorize("ec2:DescribeInstanceStatus", "*"); err != nil {
		return nil, err
	}
	insts, err := s.awsInstances(q)
	if err != nil {
		return nil, err
	}
	all, _ := boolParam(q, "IncludeAllInstances")
	items := awsapi.Items{}
	for _, i := range insts {
		if i.State != "running" && !all {
			continue
		}
		status := "ok"
		detail := "passed"
		switch i.State {
		case "pending":
			status, detail = "initializing", "initializing"
		case "running":
		default:
			status, detail = "not-applicable", ""
		}
		check := map[string]any{"status": status, "details": awsapi.Items{}}
		if detail != "" {
			check["details"] = awsapi.Items{map[string]any{"name": "reachability", "status": detail}}
		}
		items = append(items, map[string]any{"instanceId": i.ID, "availabilityZone": i.AvailabilityZone, "instanceState": stateXML(i.State),
			"systemStatus": check, "instanceStatus": check, "eventsSet": awsapi.Items{}, "attachedEbsStatus": check})
	}
	return map[string]any{"instanceStatusSet": items}, nil
}

func (s *Service) instanceArg(q *awsapi.Req, action string) (Instance, error) {
	id := q.Param("InstanceId")
	if err := q.Authorize(action, q.ARN("ec2", "instance/"+id)); err != nil {
		return Instance{}, err
	}
	i, err := s.get(id)
	if err != nil {
		return i, instanceNotFound(id)
	}
	return i, nil
}

func value(v any) map[string]any { return map[string]any{"value": v} }

func (s *Service) awsDescribeInstanceAttribute(q *awsapi.Req) (any, error) {
	i, err := s.instanceArg(q, "ec2:DescribeInstanceAttribute")
	if err != nil {
		return nil, err
	}
	out := map[string]any{"instanceId": i.ID}
	attr := q.Param("Attribute")
	switch attr {
	case "instanceType":
		out[attr] = value(i.InstanceType)
	case "kernel", "ramdisk":
		out[attr] = map[string]any{}
	case "userData":
		if i.UserData == "" {
			out[attr] = map[string]any{}
		} else {
			out[attr] = value(base64.StdEncoding.EncodeToString([]byte(i.UserData)))
		}
	case "disableApiTermination":
		out[attr] = value(i.DisableAPITermination)
	case "disableApiStop":
		out[attr] = value(i.DisableAPIStop)
	case "instanceInitiatedShutdownBehavior":
		out[attr] = value(i.ShutdownBehavior)
	case "rootDeviceName":
		out[attr] = value("/dev/xvda")
	case "blockDeviceMapping":
		out[attr] = s.instanceXML(i)["blockDeviceMapping"]
	case "productCodes":
		out[attr] = awsapi.Items{}
	case "sourceDestCheck":
		out[attr] = value(!i.SourceDestCheckOff)
	case "groupSet":
		out[attr] = s.groupSet(i.SecurityGroups)
	case "ebsOptimized":
		out[attr] = value(i.EBSOptimized)
	case "sriovNetSupport":
		out[attr] = map[string]any{}
	case "enaSupport":
		out[attr] = value(true)
	case "enclaveOptions":
		out[attr] = map[string]any{"enabled": false}
	default:
		return nil, invalid("Value (%s) for parameter attribute is invalid. Unknown attribute.", attr)
	}
	return out, nil
}

func (s *Service) awsModifyInstanceAttribute(q *awsapi.Req) (any, error) {
	i, err := s.instanceArg(q, "ec2:ModifyInstanceAttribute")
	if err != nil {
		return nil, err
	}
	val := func(name string) (string, bool) {
		if v, ok := q.Form[name+".Value"]; ok {
			return v[0], true
		}
		if q.Param("Attribute") == strings.ToLower(name[:1])+name[1:] {
			return q.Param("Value"), true
		}
		return "", false
	}
	if t, ok := val("InstanceType"); ok {
		if _, err := s.ChangeType(i.ID, t); err != nil {
			return nil, err
		}
	}
	if ud, ok := val("UserData"); ok {
		if i.State != "stopped" {
			return nil, core.Errf(http.StatusBadRequest, "IncorrectInstanceState", "The instance '%s' is not in the 'stopped' state.", i.ID)
		}
		if b, err := base64.StdEncoding.DecodeString(ud); err == nil {
			ud = string(b)
		}
		if _, err := store.Update(s.env.Store, cInstances, i.ID, func(x *Instance) error { x.UserData = ud; return nil }); err != nil {
			return nil, err
		}
	}
	if groups := q.List("GroupId"); len(groups) > 0 {
		if err := s.vpc.CheckGroups(i.VpcID, groups); err != nil {
			return nil, err
		}
		if _, err := store.Update(s.env.Store, cInstances, i.ID, func(x *Instance) error { x.SecurityGroups = groups; return nil }); err != nil {
			return nil, err
		}
		s.syncPortsAsync(i.ID)
	}
	for _, m := range q.Structs("BlockDeviceMapping") {
		if d, ok := m["Ebs.DeleteOnTermination"]; ok {
			if err := s.SetDeleteOnTermination(i.ID, m["DeviceName"], d == "true"); err != nil {
				return nil, err
			}
		}
	}
	type flag struct {
		name string
		set  func(x *Instance, v bool)
	}
	for _, f := range []flag{
		{"DisableApiTermination", func(x *Instance, v bool) { x.DisableAPITermination = v }},
		{"DisableApiStop", func(x *Instance, v bool) { x.DisableAPIStop = v }},
		{"SourceDestCheck", func(x *Instance, v bool) { x.SourceDestCheckOff = !v }},
		{"EbsOptimized", func(x *Instance, v bool) { x.EBSOptimized = v }},
	} {
		if v, ok := val(f.name); ok {
			b := strings.EqualFold(v, "true")
			if _, err := store.Update(s.env.Store, cInstances, i.ID, func(x *Instance) error { f.set(x, b); return nil }); err != nil {
				return nil, err
			}
		}
	}
	if v, ok := val("InstanceInitiatedShutdownBehavior"); ok {
		if v != "stop" && v != "terminate" {
			return nil, invalid("InstanceInitiatedShutdownBehavior must be stop or terminate")
		}
		if _, err := store.Update(s.env.Store, cInstances, i.ID, func(x *Instance) error { x.ShutdownBehavior = v; return nil }); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

func (s *Service) awsDescribeCreditSpecs(q *awsapi.Req) (any, error) {
	if err := q.Authorize("ec2:DescribeInstanceCreditSpecifications", "*"); err != nil {
		return nil, err
	}
	insts, err := s.awsInstances(q)
	if err != nil {
		return nil, err
	}
	items := awsapi.Items{}
	for _, i := range insts {
		if !strings.HasPrefix(i.InstanceType, "t") || i.State == "terminated" {
			continue
		}
		c := i.CPUCredits
		if c == "" {
			c = "unlimited"
		}
		items = append(items, map[string]any{"instanceId": i.ID, "cpuCredits": c})
	}
	return map[string]any{"instanceCreditSpecificationSet": items}, nil
}

func (s *Service) awsModifyCreditSpec(q *awsapi.Req) (any, error) {
	ok, bad := awsapi.Items{}, awsapi.Items{}
	for _, m := range q.Structs("InstanceCreditSpecification") {
		id, c := m["InstanceId"], m["CpuCredits"]
		if err := q.Authorize("ec2:ModifyInstanceCreditSpecification", q.ARN("ec2", "instance/"+id)); err != nil {
			return nil, err
		}
		if c != "standard" && c != "unlimited" {
			return nil, invalid("CpuCredits must be standard or unlimited")
		}
		if _, err := store.Update(s.env.Store, cInstances, id, func(x *Instance) error { x.CPUCredits = c; return nil }); err != nil {
			bad = append(bad, map[string]any{"instanceId": id, "error": map[string]any{"code": "InvalidInstanceID.NotFound", "message": "The instance ID '" + id + "' does not exist"}})
			continue
		}
		ok = append(ok, map[string]any{"instanceId": id})
	}
	return map[string]any{"successfulInstanceCreditSpecificationSet": ok, "unsuccessfulInstanceCreditSpecificationSet": bad}, nil
}

func (s *Service) awsModifyMetadataOptions(q *awsapi.Req) (any, error) {
	i, err := s.instanceArg(q, "ec2:ModifyInstanceMetadataOptions")
	if err != nil {
		return nil, err
	}
	md := i.Metadata.withDefaults()
	if v := q.Param("HttpTokens"); v != "" {
		md.HttpTokens = v
	}
	if v := q.Param("HttpEndpoint"); v != "" {
		md.HttpEndpoint = v
	}
	if v := q.ParamInt("HttpPutResponseHopLimit", 0); v != 0 {
		md.HopLimit = v
	}
	if v := q.Param("InstanceMetadataTags"); v != "" {
		md.InstanceMetadataTags = v
	}
	if err := checkMetadata(md); err != nil {
		return nil, err
	}
	i, err = store.Update(s.env.Store, cInstances, i.ID, func(x *Instance) error { x.Metadata = md; return nil })
	if err != nil {
		return nil, err
	}
	opts := s.instanceXML(i)["metadataOptions"].(map[string]any)
	opts["state"] = "pending"
	return map[string]any{"instanceId": i.ID, "instanceMetadataOptions": opts}, nil
}

func (s *Service) awsGetConsoleOutput(q *awsapi.Req) (any, error) {
	i, err := s.instanceArg(q, "ec2:GetConsoleOutput")
	if err != nil {
		return nil, err
	}
	out := ""
	if i.ContainerID != "" && i.State != "terminated" {
		out, _ = s.env.Docker.Logs(i.ContainerID, 1000, time.Time{})
	}
	return map[string]any{"instanceId": i.ID, "timestamp": core.Now(), "output": base64.StdEncoding.EncodeToString([]byte(out))}, nil
}

// ---- key pairs ----

func (s *Service) keyXML(k KeyPair, withPublic bool) map[string]any {
	m := map[string]any{"keyPairId": k.ID, "keyName": k.Name, "keyFingerprint": k.Fingerprint, "keyType": k.Type,
		"createTime": k.CreatedAt, "tagSet": tagSet(k.Tags)}
	if withPublic {
		m["publicKey"] = k.PublicKey
	}
	return m
}

func (s *Service) awsCreateKeyPair(q *awsapi.Req) (any, error) {
	name := q.Param("KeyName")
	if err := q.Authorize("ec2:CreateKeyPair", q.ARN("ec2", "key-pair/"+name)); err != nil {
		return nil, err
	}
	if f := q.Param("KeyFormat"); f != "" && f != "pem" {
		return nil, invalid("only the pem key format is supported")
	}
	k, private, err := s.CreateKeyPair(name, q.Param("KeyType"), tagSpecs(q, "key-pair"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"keyPairId": k.ID, "keyName": k.Name, "keyFingerprint": k.Fingerprint, "keyMaterial": private, "tagSet": tagSet(k.Tags)}, nil
}

func (s *Service) awsImportKeyPair(q *awsapi.Req) (any, error) {
	name := q.Param("KeyName")
	if err := q.Authorize("ec2:ImportKeyPair", q.ARN("ec2", "key-pair/"+name)); err != nil {
		return nil, err
	}
	material, err := base64.StdEncoding.DecodeString(q.Param("PublicKeyMaterial"))
	if err != nil {
		material = []byte(q.Param("PublicKeyMaterial"))
	}
	k, err := s.ImportKeyPair(name, material, tagSpecs(q, "key-pair"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"keyPairId": k.ID, "keyName": k.Name, "keyFingerprint": k.Fingerprint, "tagSet": tagSet(k.Tags)}, nil
}

func (s *Service) awsDescribeKeyPairs(q *awsapi.Req) (any, error) {
	if err := q.Authorize("ec2:DescribeKeyPairs", "*"); err != nil {
		return nil, err
	}
	names, ids, fs := q.List("KeyName"), q.List("KeyPairId"), filters(q)
	for _, n := range append(slices.Clone(names), ids...) {
		if _, err := s.keyPair(n); err != nil {
			return nil, err
		}
	}
	pub, _ := boolParam(q, "IncludePublicKey")
	keys := store.List[KeyPair](s.env.Store, cKeyPairs)
	slices.SortFunc(keys, func(a, b KeyPair) int { return strings.Compare(a.Name, b.Name) })
	items := awsapi.Items{}
	for _, k := range keys {
		if (len(names) > 0 && !slices.Contains(names, k.Name)) || (len(ids) > 0 && !slices.Contains(ids, k.ID)) {
			continue
		}
		a := attrs{}.set("key-name", k.Name).set("key-pair-id", k.ID).set("fingerprint", k.Fingerprint).set("key-type", k.Type).tags(k.Tags)
		if match(fs, a) {
			items = append(items, s.keyXML(k, pub))
		}
	}
	return map[string]any{"keySet": items}, nil
}

func (s *Service) awsDeleteKeyPair(q *awsapi.Req) (any, error) {
	ref := q.Param("KeyPairId")
	if ref == "" {
		ref = q.Param("KeyName")
	}
	if err := q.Authorize("ec2:DeleteKeyPair", q.ARN("ec2", "key-pair/"+ref)); err != nil {
		return nil, err
	}
	id := ""
	if k, err := s.keyPair(ref); err == nil {
		id = k.ID
	}
	if err := s.DeleteKeyPair(ref); err != nil {
		return nil, err
	}
	return map[string]any{"return": true, "keyPairId": id}, nil
}

// ---- tags ----

var resourceTypes = map[string]string{
	"i": "instance", "vol": "volume", "vpc": "vpc", "subnet": "subnet", "sg": "security-group", "ami": "image",
	"key": "key-pair", "snap": "snapshot", "igw": "internet-gateway", "rtb": "route-table", "lt": "launch-template", "eipalloc": "elastic-ip", "nat": "natgateway", "sgr": "security-group-rule", "acl": "network-acl",
}

func resourceType(id string) string {
	p, _, _ := strings.Cut(id, "-")
	return resourceTypes[p]
}

// setTags changes a resource's tags; the Name tag is kept in step with the
// name of resources that have one.
func (s *Service) setTags(id string, fn func(t core.Tags)) error {
	named := func(name *string, tags *core.Tags) {
		t := s.tagsOf("", *name, *tags)
		fn(t)
		*name, *tags = t["Name"], t
	}
	plain := func(tags *core.Tags) {
		t := s.tagsOf("", "", *tags)
		fn(t)
		*tags = t
	}
	var err error
	switch resourceType(id) {
	case "instance":
		_, err = store.Update(s.env.Store, cInstances, id, func(x *Instance) error { named(&x.Name, &x.Tags); return nil })
	case "volume":
		_, err = store.Update(s.env.Store, cVolumes, id, func(x *Volume) error { named(&x.Name, &x.Tags); return nil })
	case "vpc":
		_, err = s.vpc.UpdateVPC(id, func(x *vpc.VPC) error { named(&x.Name, &x.Tags); return nil })
	case "subnet":
		_, err = s.vpc.UpdateSubnet(id, func(x *vpc.Subnet) error { named(&x.Name, &x.Tags); return nil })
	case "security-group":
		_, err = s.vpc.UpdateSecurityGroup(id, func(x *vpc.SecurityGroup) error { plain(&x.Tags); return nil })
	case "image":
		_, err = store.Update(s.env.Store, cImages, id, func(x *Image) error { plain(&x.Tags); return nil })
	case "key-pair":
		_, err = store.Update(s.env.Store, cKeyPairs, id, func(x *KeyPair) error { plain(&x.Tags); return nil })
	case "snapshot":
		_, err = store.Update(s.env.Store, cSnapshots, id, func(x *Snapshot) error { plain(&x.Tags); return nil })
	case "internet-gateway":
		_, err = store.Update(s.env.Store, cIGWs, id, func(x *InternetGateway) error { plain(&x.Tags); return nil })
	case "route-table":
		_, err = store.Update(s.env.Store, cRouteTables, id, func(x *RouteTable) error { plain(&x.Tags); return nil })
	case "elastic-ip":
		_, err = store.Update(s.env.Store, cAddresses, id, func(x *Address) error { plain(&x.Tags); return nil })
	case "launch-template":
		_, err = store.Update(s.env.Store, cLaunchTemplates, id, func(x *LaunchTemplate) error { plain(&x.Tags); return nil })
	case "natgateway":
		_, err = store.Update(s.env.Store, cNatGateways, id, func(x *NatGateway) error { plain(&x.Tags); return nil })
	case "network-acl":
		err = s.setACLTags(id, plain)
	case "security-group-rule":
		err = store.ErrNotFound
		for _, g := range s.vpc.SecurityGroups() {
			if !slices.ContainsFunc(append(slices.Clone(g.Ingress), g.Egress...), func(r vpc.Rule) bool { return r.ID == id }) {
				continue
			}
			_, err = s.vpc.UpdateSecurityGroup(g.ID, func(x *vpc.SecurityGroup) error {
				for _, rs := range []*[]vpc.Rule{&x.Ingress, &x.Egress} {
					for i := range *rs {
						if (*rs)[i].ID == id {
							plain(&(*rs)[i].Tags)
						}
					}
				}
				return nil
			})
			break
		}
	default:
		return core.Errf(http.StatusBadRequest, "InvalidID", "The ID '%s' is not valid", id)
	}
	var ce *core.Error
	if errors.Is(err, store.ErrNotFound) || (errors.As(err, &ce) && ce.Code == "ResourceNotFound") {
		return core.Errf(http.StatusBadRequest, "InvalidID", "The ID '%s' does not exist", id)
	}
	return err
}

func (s *Service) awsCreateTags(q *awsapi.Req) (any, error) {
	ids, tags := q.List("ResourceId"), reqTags(q, "Tag")
	if err := checkTags(tags); err != nil {
		return nil, err
	}
	for _, id := range ids {
		if err := q.Authorize("ec2:CreateTags", q.ARN("ec2", resourceType(id)+"/"+id)); err != nil {
			return nil, err
		}
	}
	for _, id := range ids {
		if err := s.setTags(id, func(t core.Tags) {
			for k, v := range tags {
				t[k] = v
			}
		}); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

func (s *Service) awsDeleteTags(q *awsapi.Req) (any, error) {
	ids := q.List("ResourceId")
	type kv struct {
		k, v  string
		byVal bool
	}
	var del []kv
	for _, m := range q.Structs("Tag") {
		v, byVal := m["Value"]
		del = append(del, kv{m["Key"], v, byVal})
	}
	for _, id := range ids {
		if err := q.Authorize("ec2:DeleteTags", q.ARN("ec2", resourceType(id)+"/"+id)); err != nil {
			return nil, err
		}
	}
	for _, id := range ids {
		if err := s.setTags(id, func(t core.Tags) {
			if len(del) == 0 {
				clear(t)
			}
			for _, d := range del {
				if !d.byVal || t[d.k] == d.v {
					delete(t, d.k)
				}
			}
		}); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

type tagRow struct {
	id   string
	tags core.Tags
}

func (s *Service) allTagged() []tagRow {
	var out []tagRow
	for _, i := range s.list() {
		out = append(out, tagRow{i.ID, s.tagsOf(i.ID, i.Name, i.Tags)})
	}
	for _, v := range store.List[Volume](s.env.Store, cVolumes) {
		out = append(out, tagRow{v.ID, s.tagsOf(v.ID, v.Name, v.Tags)})
	}
	for _, v := range s.vpc.List() {
		out = append(out, tagRow{v.ID, s.tagsOf(v.ID, v.Name, v.Tags)})
	}
	for _, sn := range s.vpc.Subnets() {
		out = append(out, tagRow{sn.ID, s.tagsOf(sn.ID, sn.Name, sn.Tags)})
	}
	for _, g := range s.vpc.SecurityGroups() {
		out = append(out, tagRow{g.ID, g.Tags})
	}
	for _, im := range store.List[Image](s.env.Store, cImages) {
		out = append(out, tagRow{im.ID, im.Tags})
	}
	for _, k := range store.List[KeyPair](s.env.Store, cKeyPairs) {
		out = append(out, tagRow{k.ID, k.Tags})
	}
	for _, sn := range store.List[Snapshot](s.env.Store, cSnapshots) {
		out = append(out, tagRow{sn.ID, sn.Tags})
	}
	for _, g := range store.List[InternetGateway](s.env.Store, cIGWs) {
		out = append(out, tagRow{g.ID, g.Tags})
	}
	for _, t := range store.List[RouteTable](s.env.Store, cRouteTables) {
		out = append(out, tagRow{t.ID, t.Tags})
	}
	for _, t := range store.List[LaunchTemplate](s.env.Store, cLaunchTemplates) {
		out = append(out, tagRow{t.ID, t.Tags})
	}
	for _, g := range s.natGateways() {
		out = append(out, tagRow{g.ID, g.Tags})
	}
	for _, a := range store.List[NetworkACL](s.env.Store, cNetworkACLs) {
		out = append(out, tagRow{a.ID, a.Tags})
	}
	return out
}

func (s *Service) awsDescribeTags(q *awsapi.Req) (any, error) {
	if err := q.Authorize("ec2:DescribeTags", "*"); err != nil {
		return nil, err
	}
	fs := filters(q)
	items := awsapi.Items{}
	for _, r := range s.allTagged() {
		keys := make([]string, 0, len(r.tags))
		for k := range r.tags {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			a := attrs{}.set("resource-id", r.id).set("resource-type", resourceType(r.id)).set("key", k).set("value", r.tags[k]).tags(r.tags)
			if match(fs, a) {
				items = append(items, map[string]any{"resourceId": r.id, "resourceType": resourceType(r.id), "key": k, "value": r.tags[k]})
			}
		}
	}
	return map[string]any{"tagSet": items}, nil
}

// ---- volumes and snapshots ----

func (s *Service) volumeXML(v Volume) map[string]any {
	m := map[string]any{
		"volumeId": v.ID, "size": v.SizeGB, "snapshotId": v.SnapshotID, "availabilityZone": v.AvailabilityZone, "status": v.State,
		"createTime": v.CreatedAt, "volumeType": v.VolumeType, "encrypted": v.Encrypted, "multiAttachEnabled": false,
		"tagSet": tagSet(s.tagsOf(v.ID, v.Name, v.Tags)), "attachmentSet": awsapi.Items{},
	}
	if v.VolumeType == "" {
		m["volumeType"] = "gp3"
	}
	if v.Iops > 0 {
		m["iops"] = v.Iops
	}
	if v.Throughput > 0 {
		m["throughput"] = v.Throughput
	}
	if v.KMSKeyID != "" {
		m["kmsKeyId"] = v.KMSKeyID
	}
	if v.AttachedTo != "" {
		st := v.AttachState
		if st == "" {
			st = "attached"
		}
		m["attachmentSet"] = awsapi.Items{map[string]any{"volumeId": v.ID, "instanceId": v.AttachedTo, "device": v.Device, "status": st,
			"attachTime": awsapi.T(v.AttachTime), "deleteOnTermination": v.DeleteOnTermination}}
	}
	return m
}

func (s *Service) awsCreateVolume(q *awsapi.Req) (any, error) {
	if err := q.Authorize("ec2:CreateVolume", q.ARN("ec2", "volume/*")); err != nil {
		return nil, err
	}
	az := q.Param("AvailabilityZone")
	if az == "" {
		return nil, core.Errf(http.StatusBadRequest, "MissingParameter", "The request must contain the parameter AvailabilityZone")
	}
	tags := tagSpecs(q, "volume")
	if err := checkTags(tags); err != nil {
		return nil, err
	}
	enc, _ := boolParam(q, "Encrypted")
	v, err := s.createVolume(VolumeInput{Name: tags["Name"], Size: q.ParamInt("Size", 0), AZ: az, Tags: tags, Type: q.Param("VolumeType"),
		Iops: q.ParamInt("Iops", 0), Throughput: q.ParamInt("Throughput", 0), Encrypted: enc, KMSKeyID: q.Param("KmsKeyId"), SnapshotID: q.Param("SnapshotId")})
	if err != nil {
		return nil, err
	}
	delete(v.Tags, "Name")
	_, _ = store.Update(s.env.Store, cVolumes, v.ID, func(x *Volume) error { delete(x.Tags, "Name"); return nil })
	return s.volumeXML(v), nil
}

func (s *Service) awsDescribeVolumes(q *awsapi.Req) (any, error) {
	if err := q.Authorize("ec2:DescribeVolumes", "*"); err != nil {
		return nil, err
	}
	ids, fs := q.List("VolumeId"), filters(q)
	vols := store.List[Volume](s.env.Store, cVolumes)
	have := map[string]bool{}
	for _, v := range vols {
		have[v.ID] = true
	}
	for _, id := range ids {
		if !have[id] {
			return nil, core.Errf(http.StatusBadRequest, "InvalidVolume.NotFound", "The volume '%s' does not exist.", id)
		}
	}
	slices.SortFunc(vols, func(a, b Volume) int { return a.CreatedAt.Compare(b.CreatedAt) })
	want := idSet(ids)
	items := awsapi.Items{}
	for _, v := range vols {
		st := v.AttachState
		if st == "" && v.AttachedTo != "" {
			st = "attached"
		}
		a := attrs{}.set("volume-id", v.ID).set("status", v.State).set("availability-zone", v.AvailabilityZone).set("size", strconv.Itoa(v.SizeGB)).
			set("volume-type", v.VolumeType).set("snapshot-id", v.SnapshotID).set("encrypted", strconv.FormatBool(v.Encrypted)).
			set("attachment.instance-id", v.AttachedTo).set("attachment.device", v.Device).set("attachment.status", st).
			set("attachment.delete-on-termination", strconv.FormatBool(v.DeleteOnTermination)).tags(s.tagsOf(v.ID, v.Name, v.Tags))
		if (len(ids) == 0 || want[v.ID]) && match(fs, a) {
			items = append(items, s.volumeXML(v))
		}
	}
	return map[string]any{"volumeSet": items}, nil
}

func (s *Service) awsDeleteVolume(q *awsapi.Req) (any, error) {
	id := q.Param("VolumeId")
	if err := q.Authorize("ec2:DeleteVolume", q.ARN("ec2", "volume/"+id)); err != nil {
		return nil, err
	}
	return nil, s.DeleteVolume(id)
}

func attachXML(v Volume, status string) map[string]any {
	return map[string]any{"volumeId": v.ID, "instanceId": v.AttachedTo, "device": v.Device, "status": status, "attachTime": awsapi.T(v.AttachTime),
		"deleteOnTermination": v.DeleteOnTermination}
}

func (s *Service) awsAttachVolume(q *awsapi.Req) (any, error) {
	vid, iid := q.Param("VolumeId"), q.Param("InstanceId")
	if err := q.Authorize("ec2:AttachVolume", q.ARN("ec2", "volume/"+vid)); err != nil {
		return nil, err
	}
	if err := q.Check("ec2:AttachVolume", q.ARN("ec2", "instance/"+iid)); err != nil {
		return nil, err
	}
	if _, err := s.get(iid); err != nil {
		return nil, instanceNotFound(iid)
	}
	v, err := s.AttachVolume(vid, iid, q.Param("Device"))
	if err != nil {
		return nil, err
	}
	return attachXML(v, "attaching"), nil
}

func (s *Service) awsDetachVolume(q *awsapi.Req) (any, error) {
	vid := q.Param("VolumeId")
	if err := q.Authorize("ec2:DetachVolume", q.ARN("ec2", "volume/"+vid)); err != nil {
		return nil, err
	}
	force, _ := boolParam(q, "Force")
	v, err := s.DetachVolume(vid, q.Param("InstanceId"), q.Param("Device"), force)
	if err != nil {
		return nil, err
	}
	return attachXML(v, "detaching"), nil
}

func (s *Service) snapshotXML(sn Snapshot) map[string]any {
	progress := "100%"
	if sn.State == "pending" {
		progress = "0%"
	}
	return map[string]any{"snapshotId": sn.ID, "volumeId": sn.VolumeID, "status": sn.State, "startTime": sn.StartTime, "progress": progress,
		"ownerId": s.env.AccountID, "volumeSize": sn.VolumeSize, "description": sn.Description, "encrypted": sn.Encrypted,
		"storageTier": "standard", "tagSet": tagSet(sn.Tags)}
}

func (s *Service) awsCreateSnapshot(q *awsapi.Req) (any, error) {
	vid := q.Param("VolumeId")
	if err := q.Authorize("ec2:CreateSnapshot", q.ARN("ec2", "volume/"+vid)); err != nil {
		return nil, err
	}
	sn, err := s.CreateSnapshot(vid, q.Param("Description"), tagSpecs(q, "snapshot"))
	if err != nil {
		return nil, err
	}
	return s.snapshotXML(sn), nil
}

func (s *Service) awsDescribeSnapshots(q *awsapi.Req) (any, error) {
	if err := q.Authorize("ec2:DescribeSnapshots", "*"); err != nil {
		return nil, err
	}
	ids, fs := q.List("SnapshotId"), filters(q)
	for _, id := range ids {
		if _, err := s.snapshot(id); err != nil {
			return nil, err
		}
	}
	want := idSet(ids)
	items := awsapi.Items{}
	for _, sn := range store.List[Snapshot](s.env.Store, cSnapshots) {
		a := attrs{}.set("snapshot-id", sn.ID).set("volume-id", sn.VolumeID).set("status", sn.State).set("owner-id", s.env.AccountID).
			set("description", sn.Description).set("volume-size", strconv.Itoa(sn.VolumeSize)).tags(sn.Tags)
		if (len(ids) == 0 || want[sn.ID]) && match(fs, a) {
			items = append(items, s.snapshotXML(sn))
		}
	}
	return map[string]any{"snapshotSet": items}, nil
}

func (s *Service) awsDeleteSnapshot(q *awsapi.Req) (any, error) {
	id := q.Param("SnapshotId")
	if err := q.Authorize("ec2:DeleteSnapshot", q.ARN("ec2", "snapshot/"+id)); err != nil {
		return nil, err
	}
	return nil, s.DeleteSnapshot(id)
}

// ---- network interfaces and addresses ----

// awsDescribeNetworkInterfaces lists each live instance's primary interface.
func (s *Service) awsDescribeNetworkInterfaces(q *awsapi.Req) (any, error) {
	if err := q.Authorize("ec2:DescribeNetworkInterfaces", "*"); err != nil {
		return nil, err
	}
	ids, fs := q.List("NetworkInterfaceId"), filters(q)
	want := idSet(ids)
	found := map[string]bool{}
	items := awsapi.Items{}
	for _, i := range s.list() {
		if i.State == "terminated" || (len(ids) > 0 && !want[eniID(i.ID)]) {
			continue
		}
		a := attrs{}.set("network-interface-id", eniID(i.ID)).set("subnet-id", i.SubnetID).set("vpc-id", i.VpcID).
			set("availability-zone", i.AvailabilityZone).set("private-ip-address", i.PrivateIP).set("addresses.private-ip-address", i.PrivateIP).
			set("status", "in-use").set("attachment.instance-id", i.ID).set("attachment.status", "attached").set("group-id", i.SecurityGroups...).
			set("description", "").set("interface-type", "interface").set("owner-id", s.env.AccountID).set("requester-managed", "false").
			set("mac-address", macFor(i.PrivateIP)).set("attachment.device-index", "0").tags(nil)
		if match(fs, a) {
			found[eniID(i.ID)] = true
			items = append(items, s.eniXML(i, true))
		}
	}
	for _, id := range ids {
		if !found[id] && len(fs) == 0 {
			return nil, core.Errf(http.StatusBadRequest, "InvalidNetworkInterfaceID.NotFound", "The networkInterface ID '%s' does not exist", id)
		}
	}
	return map[string]any{"networkInterfaceSet": items}, nil
}

// awsModifyNetworkInterfaceAttribute changes the security groups of an
// instance's primary interface; the other attributes are accepted and ignored.
func (s *Service) awsModifyNetworkInterfaceAttribute(q *awsapi.Req) (any, error) {
	id := q.Param("NetworkInterfaceId")
	if err := q.Authorize("ec2:ModifyNetworkInterfaceAttribute", q.ARN("ec2", "network-interface/"+id)); err != nil {
		return nil, err
	}
	var inst *Instance
	for _, i := range s.list() {
		if i.State != "terminated" && eniID(i.ID) == id {
			inst = &i
			break
		}
	}
	if inst == nil {
		return nil, core.Errf(http.StatusBadRequest, "InvalidNetworkInterfaceID.NotFound", "The networkInterface ID '%s' does not exist", id)
	}
	if groups := q.List("SecurityGroupId"); len(groups) > 0 {
		if err := s.vpc.CheckGroups(inst.VpcID, groups); err != nil {
			return nil, err
		}
		if _, err := store.Update(s.env.Store, cInstances, inst.ID, func(x *Instance) error { x.SecurityGroups = groups; return nil }); err != nil {
			return nil, err
		}
		s.syncPortsAsync(inst.ID)
	}
	return map[string]any{"return": true}, nil
}
