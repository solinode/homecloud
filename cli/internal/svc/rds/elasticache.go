package rds

import (
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi"
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/store"
)

// The AWS ElastiCache API (awsQuery, 2015-02-02) for Redis, Valkey and
// Memcached. Cache clusters are the cache-kind instances the native API
// creates; both layers call the same provision, modify, reboot, delete and
// snapshot functions.
//
// HomeCloud runs one node per cluster and one primary per replication group
// (no replicas, no cluster mode, no failover). TLS and cache security groups
// are not implemented.

const cacheXMLNS = "http://elasticache.amazonaws.com/doc/2015-02-02/"

const (
	cCacheSubnetGroups = "elasticache_subnet_groups"
	cReplGroups        = "elasticache_replication_groups"
)

// ReplicationGroup is a Redis or Valkey replication group: a primary node (a
// cache instance named <id>-001) behind the group's endpoints.
type ReplicationGroup struct {
	ID          string    `json:"id"`
	Description string    `json:"description"`
	Primary     string    `json:"primary"` // the member cluster's ID
	Tags        core.Tags `json:"tags,omitempty"`
}

func (s *Service) registerElastiCache() {
	ops := map[string]awsOp{
		"CreateCacheCluster":          s.ecCreateCluster,
		"DescribeCacheClusters":       s.ecDescribeClusters,
		"ModifyCacheCluster":          s.ecModifyCluster,
		"DeleteCacheCluster":          s.ecDeleteCluster,
		"RebootCacheCluster":          s.ecRebootCluster,
		"CreateReplicationGroup":      s.ecCreateGroup,
		"DescribeReplicationGroups":   s.ecDescribeGroups,
		"ModifyReplicationGroup":      s.ecModifyGroup,
		"DeleteReplicationGroup":      s.ecDeleteGroup,
		"CreateCacheSubnetGroup":      s.ecCreateSubnetGroup,
		"DescribeCacheSubnetGroups":   s.ecDescribeSubnetGroups,
		"ModifyCacheSubnetGroup":      s.ecModifySubnetGroup,
		"DeleteCacheSubnetGroup":      s.ecDeleteSubnetGroup,
		"CreateSnapshot":              s.ecCreateSnapshot,
		"DescribeSnapshots":           s.ecDescribeSnapshots,
		"DeleteSnapshot":              s.ecDeleteSnapshot,
		"DescribeCacheEngineVersions": s.ecDescribeEngineVersions,
		"AddTagsToResource":           s.ecAddTags,
		"RemoveTagsFromResource":      s.ecRemoveTags,
		"ListTagsForResource":         s.ecListTags,
	}
	svc := &awsapi.Service{Name: "elasticache", XMLNS: cacheXMLNS, Ops: map[string]awsapi.Op{}}
	for name, fn := range ops {
		svc.Ops[name] = func(q *awsapi.Req) (any, error) {
			out, err := fn(q)
			if err != nil {
				return nil, cacheError(err)
			}
			if out == nil {
				return map[string]any{}, nil
			}
			return out, nil
		}
	}
	awsapi.Register(svc)
}

// cacheError maps HomeCloud errors to ElastiCache error codes.
func cacheError(err error) error {
	var ae *awsapi.Error
	if errors.As(err, &ae) {
		return ae
	}
	var ce *core.Error
	if !errors.As(err, &ce) {
		return err
	}
	code, msg, status := ce.Code, ce.Message, http.StatusBadRequest
	switch code {
	case "ResourceNotFound":
		switch {
		case strings.HasPrefix(msg, "snapshot "):
			code, status = "SnapshotNotFoundFault", http.StatusNotFound
		case strings.HasPrefix(msg, "subnet "):
			code = "InvalidSubnet"
		case strings.HasPrefix(msg, "security group "):
			code = "InvalidParameterValue"
		default:
			code, status = "CacheClusterNotFound", http.StatusNotFound
		}
	case "ResourceConflict", "Conflict", "AlreadyExists":
		if strings.HasPrefix(msg, "snapshot ") {
			code = "SnapshotAlreadyExistsFault"
		} else {
			code = "CacheClusterAlreadyExists"
		}
	case "InvalidDBInstanceState":
		code = "InvalidCacheClusterState"
	case "InvalidDBSnapshotState":
		code = "InvalidSnapshotState"
	case "ValidationError", "BadRequest":
		code = "InvalidParameterValue"
	case "AccessDenied":
		return &awsapi.Error{Status: http.StatusForbidden, Code: "AccessDenied", Message: msg}
	}
	if ce.Status >= 500 {
		status = ce.Status
	}
	return &awsapi.Error{Status: status, Code: code, Message: msg}
}

// ---- ARNs and lookups ----

func (s *Service) cacheARN(id string) string { return s.env.ARN("elasticache", "cluster:"+id) }
func (s *Service) groupARN(id string) string { return s.env.ARN("elasticache", "replicationgroup:"+id) }
func (s *Service) cacheSubnetARN(id string) string {
	return s.env.ARN("elasticache", "subnetgroup:"+id)
}
func (s *Service) cacheSnapARN(id string) string { return s.env.ARN("elasticache", "snapshot:"+id) }

// primaryHost and readerHost are the endpoints of a replication group.
func primaryHost(rg string) string { return rg + ".elasticache.internal" }
func readerHost(rg string) string  { return rg + "-ro.elasticache.internal" }

func cacheNotFound(id string) error {
	return notFound("CacheClusterNotFound", "CacheCluster not found: %s", id)
}

func groupNotFound(id string) error {
	return notFound("ReplicationGroupNotFoundFault", "ReplicationGroup %s not found.", id)
}

func subnetGroupNotFound(name string) error {
	return apiErr("CacheSubnetGroupNotFoundFault", "Cache subnet group %s not found.", name)
}

func snapshotNotFound(id string) error {
	return notFound("SnapshotNotFoundFault", "Snapshot not found: %s", id)
}

func (s *Service) cacheInstance(id string) (Instance, error) {
	i, err := s.get(strings.ToLower(id))
	if err != nil || i.Kind != "cache" {
		return i, cacheNotFound(id)
	}
	return i, nil
}

func (s *Service) group(id string) (ReplicationGroup, error) {
	g, err := store.Get[ReplicationGroup](s.env.Store, cReplGroups, strings.ToLower(id))
	if err != nil {
		return g, groupNotFound(id)
	}
	return g, nil
}

// ---- rendering ----

func cacheStatus(st string) string {
	switch st {
	case "restoring", "starting":
		return "creating"
	case "rebooting":
		return "rebooting cluster nodes"
	case "backing-up":
		return "snapshotting"
	case "failed":
		return "create-failed"
	case "stopped", "stopping":
		return "incompatible-network"
	}
	return st
}

var cacheDefaults = map[string]string{
	"PreferredMaintenanceWindow": "sun:05:00-sun:06:00", "SnapshotWindow": "05:00-06:00", "AutoMinorVersionUpgrade": "true",
	"AtRestEncryptionEnabled": "false",
}

// cacheSettingKeys are the request attributes HomeCloud stores and reports without acting on them.
var cacheSettingKeys = []string{"PreferredMaintenanceWindow", "SnapshotWindow", "AutoMinorVersionUpgrade", "CacheParameterGroupName",
	"NotificationTopicArn", "AtRestEncryptionEnabled", "KmsKeyId"}

func cacheSettingsIn(q *awsapi.Req) map[string]string {
	m := map[string]string{}
	for _, k := range cacheSettingKeys {
		if has(q, k) {
			m[k] = q.Param(k)
		}
	}
	return m
}

// cacheFamily is the parameter group family of an engine version.
func cacheFamily(engine, version string) string {
	major := strings.SplitN(version, ".", 2)[0]
	switch engine {
	case "redis":
		if major == "6" {
			return "redis6.x"
		}
		return "redis" + major
	case "valkey":
		return "valkey" + major
	}
	p := strings.SplitN(version, ".", 3)
	if len(p) < 2 {
		return engine + p[0]
	}
	return engine + p[0] + "." + p[1]
}

func (i Instance) setting(k string) string {
	if v, ok := i.Settings[k]; ok {
		return v
	}
	return cacheDefaults[k]
}

func endpointXML(host string, port int) map[string]any {
	return map[string]any{"Address": host, "Port": port}
}

func (s *Service) cacheClusterXML(i Instance, showNodes bool) map[string]any {
	ready := !slices.Contains([]string{"creating", "restoring", "failed", "deleting"}, i.Status)
	pg := orDefault(i.setting("CacheParameterGroupName"), "default."+cacheFamily(i.Engine, i.EngineVersion))
	m := map[string]any{
		"CacheClusterId": i.ID, "ClientDownloadLandingPage": "https://console.aws.amazon.com/elasticache/home#client-download:",
		"CacheNodeType": i.Class, "Engine": i.Engine, "EngineVersion": i.EngineVersion, "CacheClusterStatus": cacheStatus(i.Status),
		"NumCacheNodes": 1, "PreferredAvailabilityZone": i.AvailabilityZone, "CacheClusterCreateTime": i.CreatedAt,
		"PreferredMaintenanceWindow": i.setting("PreferredMaintenanceWindow"), "PendingModifiedValues": map[string]any{},
		"CacheSecurityGroups":     named("CacheSecurityGroup"),
		"CacheParameterGroup":     map[string]any{"CacheParameterGroupName": pg, "ParameterApplyStatus": "in-sync", "CacheNodeIdsToReboot": named("CacheNodeId")},
		"AutoMinorVersionUpgrade": i.setting("AutoMinorVersionUpgrade") != "false", "SnapshotRetentionLimit": i.BackupRetentionDays,
		"SnapshotWindow": i.setting("SnapshotWindow"), "AuthTokenEnabled": i.SecretName != "", "TransitEncryptionEnabled": false,
		"AtRestEncryptionEnabled": i.setting("AtRestEncryptionEnabled") == "true", "ARN": s.cacheARN(i.ID),
		"ReplicationGroupLogDeliveryEnabled": false, "NetworkType": "ipv4", "IpDiscovery": "ipv4",
	}
	if i.SubnetGroup != "" {
		m["CacheSubnetGroupName"] = i.SubnetGroup
	}
	if i.ReplicationGroup != "" {
		m["ReplicationGroupId"] = i.ReplicationGroup
	}
	sgs := awsapi.Named{Name: "member"}
	for _, g := range i.SecurityGroups {
		sgs.Values = append(sgs.Values, map[string]any{"SecurityGroupId": g, "Status": "active"})
	}
	m["SecurityGroups"] = sgs
	if ready {
		ep := endpointXML(i.Endpoint.Address, i.Endpoint.Port)
		if i.Engine == "memcached" {
			m["ConfigurationEndpoint"] = ep
		}
		if showNodes {
			m["CacheNodes"] = named("CacheNode", map[string]any{"CacheNodeId": "0001", "CacheNodeStatus": cacheStatus(i.Status),
				"CacheNodeCreateTime": i.CreatedAt, "Endpoint": ep, "ParameterGroupStatus": "in-sync", "CustomerAvailabilityZone": i.AvailabilityZone})
		}
	}
	return m
}

func (s *Service) groupXML(g ReplicationGroup) map[string]any {
	p, err := store.Get[Instance](s.env.Store, cInstances, g.Primary)
	if err == nil {
		p = s.sync(p)
	}
	status := "available"
	if err == nil {
		status = cacheStatus(p.Status)
	}
	member := map[string]any{"CacheClusterId": g.Primary, "CacheNodeId": "0001", "PreferredAvailabilityZone": p.AvailabilityZone, "CurrentRole": "primary"}
	ng := map[string]any{"NodeGroupId": "0001", "Status": status, "NodeGroupMembers": named("NodeGroupMember", member)}
	if err == nil && status == "available" {
		member["ReadEndpoint"] = endpointXML(p.Endpoint.Address, p.Endpoint.Port)
		ng["PrimaryEndpoint"] = endpointXML(primaryHost(g.ID), p.Endpoint.Port)
		ng["ReaderEndpoint"] = endpointXML(readerHost(g.ID), p.Endpoint.Port)
	}
	created := any(nil)
	if err == nil {
		created = p.CreatedAt
	}
	m := map[string]any{
		"ReplicationGroupId": g.ID, "Description": g.Description, "Status": status, "PendingModifiedValues": map[string]any{},
		"MemberClusters": named("ClusterId", g.Primary), "NodeGroups": named("NodeGroup", ng), "AutomaticFailover": "disabled",
		"MultiAZ": "disabled", "ClusterEnabled": false, "CacheNodeType": p.Class, "AuthTokenEnabled": p.SecretName != "",
		"TransitEncryptionEnabled": false, "AtRestEncryptionEnabled": p.setting("AtRestEncryptionEnabled") == "true",
		"SnapshotRetentionLimit": p.BackupRetentionDays, "SnapshotWindow": p.setting("SnapshotWindow"), "ARN": s.groupARN(g.ID),
		"AutoMinorVersionUpgrade": p.setting("AutoMinorVersionUpgrade") != "false", "DataTiering": "disabled",
		"NetworkType": "ipv4", "IpDiscovery": "ipv4", "ClusterMode": "disabled", "Engine": p.Engine,
		"LogDeliveryConfigurations": named("LogDeliveryConfiguration"), "UserGroupIds": named("member"),
	}
	if created != nil {
		m["ReplicationGroupCreateTime"] = created
	}
	return m
}

// ---- request handling shared by cluster and group creation ----

// cacheSpec is what creating a cache cluster or a replication group asks for.
type cacheSpec struct {
	id, engine string
	in         createInput
	snapshot   string
}

var cacheIDRe = idRe

// cacheSpecFrom reads the attributes CreateCacheCluster and CreateReplicationGroup share.
func (s *Service) cacheSpecFrom(q *awsapi.Req, id, memberID string) (cacheSpec, error) {
	var sp cacheSpec
	engine := strings.ToLower(q.Param("Engine"))
	if engine == "" {
		engine = "redis"
	}
	e, ok := findEngine(engine)
	if !ok || e.Kind != "cache" {
		return sp, apiErr("InvalidParameterValue", "Invalid cache engine: %s (supported: redis, valkey, memcached)", q.Param("Engine"))
	}
	ver, err := normalizeVersion(e, q.Param("EngineVersion"))
	if err != nil {
		return sp, err
	}
	if p := q.ParamInt("Port", 0); p != 0 && p != e.DefaultPort {
		return sp, apiErr("InvalidParameterValue", "Port %d is not supported: HomeCloud serves %s on %d", p, e.Label, e.DefaultPort)
	}
	if q.ParamBool("TransitEncryptionEnabled", false) {
		return sp, apiErr("InvalidParameterCombination", "HomeCloud does not implement in-transit encryption (TransitEncryptionEnabled)")
	}
	if len(list(q, "CacheSecurityGroupNames", "CacheSecurityGroupName")) > 0 {
		return sp, apiErr("InvalidParameterCombination", "Cache security groups are only for EC2-Classic; use SecurityGroupIds")
	}
	class := q.Param("CacheNodeType")
	if class != "" {
		if c, ok := findClass(class); !ok || c.Kind != "cache" {
			return sp, apiErr("InvalidParameterValue", "The parameter CacheNodeType is not a supported node type: %s", class)
		}
	}
	pl, err := s.resolveCachePlacement(q)
	if err != nil {
		return sp, err
	}
	in := createInput{ID: memberID, Engine: engine, EngineVersion: ver, Class: class, SubnetID: pl.subnetID, SubnetGroup: pl.group, SecurityGroups: pl.sgs,
		Tags: tagsIn(q), Settings: cacheSettingsIn(q)}
	retention := q.ParamInt("SnapshotRetentionLimit", 0)
	if e.dump == nil && retention > 0 {
		return sp, apiErr("InvalidParameterCombination", "Snapshots are not supported for %s", e.Label)
	}
	if retention < 0 || retention > 35 {
		return sp, apiErr("InvalidParameterValue", "SnapshotRetentionLimit must be between 0 and 35")
	}
	in.BackupRetentionDays = &retention
	if tok := q.Param("AuthToken"); tok != "" {
		if e.Name == "memcached" {
			return sp, apiErr("InvalidParameterCombination", "AuthToken is not supported for memcached")
		}
		if len(tok) < 16 || len(tok) > 128 || strings.ContainsAny(tok, "\"'/@ \\") {
			return sp, apiErr("InvalidParameterValue", "The parameter AuthToken must be 16-128 characters and may not contain '/', '\"', '@', quotes, backslashes or spaces")
		}
		in.MasterPassword = tok
	} else {
		in.NoAuth = e.HasPassword
	}
	return cacheSpec{id: id, engine: engine, in: in, snapshot: strings.ToLower(q.Param("SnapshotName"))}, nil
}

// normalizeVersion accepts what ElastiCache clients send: "7.1", "7.1.0", "6.x" or nothing.
func normalizeVersion(e Engine, v string) (string, error) {
	if v == "" {
		return e.Versions[0], nil
	}
	if m, ok := strings.CutSuffix(v, ".x"); ok {
		for _, x := range e.Versions {
			if x == m || strings.HasPrefix(x, m+".") {
				return x, nil
			}
		}
	}
	if err := e.validVersion(v); err != nil {
		return "", apiErr("InvalidParameterValue", "Invalid cache engine version %s: %v", v, err)
	}
	return v, nil
}

// resolveCachePlacement checks the cache subnet group and security groups of a request.
func (s *Service) resolveCachePlacement(q *awsapi.Req) (placement, error) {
	var r placement
	if g := strings.ToLower(q.Param("CacheSubnetGroupName")); g != "" {
		sg, err := store.Get[SubnetGroup](s.env.Store, cCacheSubnetGroups, g)
		if err != nil {
			return r, subnetGroupNotFound(g)
		}
		r.group = g
		for _, id := range sg.SubnetIDs {
			if r.subnetID == "" {
				r.subnetID = id
			}
			// PreferredAvailabilityZone picks the group's subnet in that zone.
			if sn, err := s.vpc.GetSubnet(id); err == nil && sn.AvailabilityZone == q.Param("PreferredAvailabilityZone") {
				r.subnetID = id
				break
			}
		}
	}
	r.sgs = list(q, "SecurityGroupIds", "SecurityGroupId")
	for _, g := range r.sgs {
		if _, err := s.vpc.GetSecurityGroup(g); err != nil {
			return r, apiErr("InvalidParameterValue", "The security group '%s' does not exist", g)
		}
	}
	return r, nil
}

// launchCache provisions a cache instance, restoring from a snapshot when sp names one.
func (s *Service) launchCache(sp cacheSpec) (Instance, error) {
	if sp.snapshot == "" {
		return s.provision(sp.in, nil)
	}
	sn, err := store.Get[Snapshot](s.env.Store, cSnapshots, sp.snapshot)
	if err != nil || sn.Kind != "cache" {
		return Instance{}, snapshotNotFound(sp.snapshot)
	}
	if sn.Engine != sp.engine {
		return Instance{}, apiErr("InvalidParameterCombination", "Snapshot %s is a %s snapshot, not %s", sn.ID, sn.Engine, sp.engine)
	}
	out, err := s.restoreSnapshot(sn.ID, sp.in)
	if err != nil {
		return Instance{}, err
	}
	return out.(Instance), nil
}

// ---- clusters ----

func (s *Service) ecCreateCluster(q *awsapi.Req) (any, error) {
	id := strings.ToLower(q.Param("CacheClusterId"))
	if err := q.Authorize("elasticache:CreateCacheCluster", s.cacheARN(id)); err != nil {
		return nil, err
	}
	tags := tagsIn(q)
	if len(tags) > 0 {
		if err := q.Check("elasticache:AddTagsToResource", s.cacheARN(id)); err != nil {
			return nil, err
		}
	}
	if id == "" {
		return nil, apiErr("MissingParameter", "The request must contain the parameter CacheClusterId")
	}
	if !cacheIDRe.MatchString(id) || len(id) > 50 || strings.Contains(id, "--") || strings.HasSuffix(id, "-") {
		return nil, apiErr("InvalidParameterValue", "The parameter CacheClusterId is not a valid identifier.")
	}
	if q.Param("ReplicationGroupId") != "" {
		return nil, apiErr("InvalidParameterCombination", "HomeCloud replication groups have a single primary; create the group with CreateReplicationGroup")
	}
	if n := q.ParamInt("NumCacheNodes", 1); n != 1 {
		return nil, apiErr("InvalidParameterValue", "HomeCloud runs one node per cache cluster (NumCacheNodes must be 1)")
	}
	sp, err := s.cacheSpecFrom(q, id, id)
	if err != nil {
		return nil, err
	}
	if q.Param("AZMode") == "cross-az" {
		return nil, apiErr("InvalidParameterCombination", "AZMode cross-az needs more than one node")
	}
	i, err := s.launchCache(sp)
	if err != nil {
		return nil, err
	}
	return map[string]any{"CacheCluster": s.cacheClusterXML(i, false)}, nil
}

func (s *Service) ecDescribeClusters(q *awsapi.Req) (any, error) {
	id := strings.ToLower(q.Param("CacheClusterId"))
	res := "*"
	if id != "" {
		res = s.cacheARN(id)
	}
	if err := q.Authorize("elasticache:DescribeCacheClusters", res); err != nil {
		return nil, err
	}
	show := q.ParamBool("ShowCacheNodeInfo", false)
	if id != "" {
		i, err := s.cacheInstance(id)
		if err != nil {
			return nil, err
		}
		return map[string]any{"CacheClusters": named("CacheCluster", s.cacheClusterXML(i, show))}, nil
	}
	all := store.List[Instance](s.env.Store, cInstances)
	slices.SortFunc(all, func(a, b Instance) int { return strings.Compare(a.ID, b.ID) })
	out := awsapi.Named{Name: "CacheCluster"}
	for _, i := range all {
		if i.Kind != "cache" || (i.ReplicationGroup != "" && q.ParamBool("ShowCacheClustersNotInReplicationGroups", false)) {
			continue
		}
		out.Values = append(out.Values, s.cacheClusterXML(s.sync(i), show))
	}
	return map[string]any{"CacheClusters": out}, nil
}

// cacheModify builds the modification shared by ModifyCacheCluster and ModifyReplicationGroup.
func (s *Service) cacheModify(q *awsapi.Req, i Instance) (modifyInput, error) {
	var in modifyInput
	if q.Param("AuthToken") != "" {
		return in, apiErr("InvalidParameterCombination", "HomeCloud cannot change the AuthToken of a running cache")
	}
	if v := q.Param("EngineVersion"); v != "" && v != i.EngineVersion && !strings.HasPrefix(i.EngineVersion, v+".") && !strings.HasPrefix(v, i.EngineVersion+".") {
		return in, apiErr("InvalidParameterCombination", "HomeCloud does not support engine version upgrades")
	}
	if c := q.Param("CacheNodeType"); c != "" {
		if cl, ok := findClass(c); !ok || cl.Kind != "cache" {
			return in, apiErr("InvalidParameterValue", "The parameter CacheNodeType is not a supported node type: %s", c)
		}
		in.Class = c
	}
	if has(q, "SnapshotRetentionLimit") {
		n := q.ParamInt("SnapshotRetentionLimit", 0)
		if n < 0 || n > 35 || (n > 0 && i.Engine == "memcached") {
			return in, apiErr("InvalidParameterValue", "SnapshotRetentionLimit must be between 0 and 35 (0 for Memcached)")
		}
		in.BackupRetentionDays = &n
	}
	if has(q, "SecurityGroupIds.SecurityGroupId.1") || has(q, "SecurityGroupIds.member.1") {
		pl, err := s.resolveCachePlacement(q)
		if err != nil {
			return in, err
		}
		in.SecurityGroups = pl.sgs
	}
	in.Settings = cacheSettingsIn(q)
	delete(in.Settings, "AtRestEncryptionEnabled")
	delete(in.Settings, "KmsKeyId")
	return in, nil
}

func (s *Service) ecModifyCluster(q *awsapi.Req) (any, error) {
	id := strings.ToLower(q.Param("CacheClusterId"))
	if err := q.Authorize("elasticache:ModifyCacheCluster", s.cacheARN(id)); err != nil {
		return nil, err
	}
	i, err := s.cacheInstance(id)
	if err != nil {
		return nil, err
	}
	if n := q.ParamInt("NumCacheNodes", 1); n != 1 {
		return nil, apiErr("InvalidParameterValue", "HomeCloud runs one node per cache cluster (NumCacheNodes must be 1)")
	}
	in, err := s.cacheModify(q, i)
	if err != nil {
		return nil, err
	}
	out, err := s.modifyInstance(id, in)
	if err != nil {
		return nil, err
	}
	return map[string]any{"CacheCluster": s.cacheClusterXML(out, false)}, nil
}

func (s *Service) ecDeleteCluster(q *awsapi.Req) (any, error) {
	id := strings.ToLower(q.Param("CacheClusterId"))
	if err := q.Authorize("elasticache:DeleteCacheCluster", s.cacheARN(id)); err != nil {
		return nil, err
	}
	i, err := s.cacheInstance(id)
	if err != nil {
		return nil, err
	}
	if i.ReplicationGroup != "" {
		return nil, apiErr("InvalidCacheClusterState", "Cache cluster %s is the primary of replication group %s; delete the replication group instead.", id, i.ReplicationGroup)
	}
	final, err := s.finalSnapshot(q, i)
	if err != nil {
		return nil, err
	}
	if err := s.deleteInstance(q.R.Context(), id, final, true); err != nil {
		return nil, err
	}
	i.Status = "deleting"
	return map[string]any{"CacheCluster": s.cacheClusterXML(i, false)}, nil
}

// finalSnapshot checks the request's FinalSnapshotIdentifier and returns its name.
func (s *Service) finalSnapshot(q *awsapi.Req, i Instance) (string, error) {
	final := strings.ToLower(q.Param("FinalSnapshotIdentifier"))
	if final == "" {
		return "", nil
	}
	if err := q.Check("elasticache:CreateSnapshot", s.cacheSnapARN(final)); err != nil {
		return "", err
	}
	if e, _ := findEngine(i.Engine); e.dump == nil {
		return "", apiErr("SnapshotFeatureNotSupportedFault", "Snapshots are not supported for %s", e.Label)
	}
	if !idRe.MatchString(final) {
		return "", apiErr("InvalidParameterValue", "The parameter FinalSnapshotIdentifier is not a valid identifier.")
	}
	return final, nil
}

func (s *Service) ecRebootCluster(q *awsapi.Req) (any, error) {
	id := strings.ToLower(q.Param("CacheClusterId"))
	if err := q.Authorize("elasticache:RebootCacheCluster", s.cacheARN(id)); err != nil {
		return nil, err
	}
	if _, err := s.cacheInstance(id); err != nil {
		return nil, err
	}
	if len(list(q, "CacheNodeIdsToReboot", "CacheNodeId")) == 0 {
		return nil, apiErr("MissingParameter", "The request must contain the parameter CacheNodeIdsToReboot")
	}
	i, err := s.rebootInstance(id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"CacheCluster": s.cacheClusterXML(i, false)}, nil
}

// ---- replication groups ----

func (s *Service) ecCreateGroup(q *awsapi.Req) (any, error) {
	id := strings.ToLower(q.Param("ReplicationGroupId"))
	member := id + "-001"
	if err := q.Authorize("elasticache:CreateReplicationGroup", s.groupARN(id)); err != nil {
		return nil, err
	}
	if err := q.Check("elasticache:CreateCacheCluster", s.cacheARN(member)); err != nil {
		return nil, err
	}
	tags := tagsIn(q)
	if len(tags) > 0 {
		if err := q.Check("elasticache:AddTagsToResource", s.groupARN(id)); err != nil {
			return nil, err
		}
	}
	if id == "" {
		return nil, apiErr("MissingParameter", "The request must contain the parameter ReplicationGroupId")
	}
	if !cacheIDRe.MatchString(id) || len(id) > 40 || strings.Contains(id, "--") || strings.HasSuffix(id, "-") {
		return nil, apiErr("InvalidParameterValue", "The parameter ReplicationGroupId is not a valid identifier.")
	}
	desc := q.Param("ReplicationGroupDescription")
	if desc == "" {
		return nil, apiErr("MissingParameter", "The request must contain the parameter ReplicationGroupDescription")
	}
	if store.Has(s.env.Store, cReplGroups, id) {
		return nil, apiErr("ReplicationGroupAlreadyExists", "Replication group %s already exists.", id)
	}
	switch {
	case q.Param("PrimaryClusterId") != "":
		return nil, apiErr("InvalidParameterCombination", "HomeCloud cannot build a replication group from an existing cluster")
	case q.ParamInt("NumCacheClusters", 1) != 1:
		return nil, apiErr("InvalidParameterValue", "HomeCloud replication groups have a single primary and no replicas (NumCacheClusters must be 1)")
	case q.ParamInt("NumNodeGroups", 1) != 1 || q.ParamInt("ReplicasPerNodeGroup", 0) != 0:
		return nil, apiErr("InvalidParameterValue", "HomeCloud does not implement cluster mode or replicas")
	case q.ParamBool("AutomaticFailoverEnabled", false):
		return nil, apiErr("InvalidParameterCombination", "Automatic failover needs at least one replica, which HomeCloud does not run")
	case q.ParamBool("MultiAZEnabled", false):
		return nil, apiErr("InvalidParameterCombination", "Multi-AZ needs at least one replica, which HomeCloud does not run")
	case q.ParamBool("ClusterModeEnabled", false):
		return nil, apiErr("InvalidParameterValue", "HomeCloud does not implement cluster mode")
	}
	sp, err := s.cacheSpecFrom(q, id, member)
	if err != nil {
		return nil, err
	}
	if sp.engine == "memcached" {
		return nil, apiErr("InvalidParameterValue", "Replication groups are for Redis and Valkey; use CreateCacheCluster for Memcached")
	}
	sp.in.ReplicationGroup = id
	if store.Has(s.env.Store, cInstances, member) {
		return nil, apiErr("CacheClusterAlreadyExists", "Cache cluster %s already exists.", member)
	}
	i, err := s.launchCache(sp)
	if err != nil {
		return nil, err
	}
	g := ReplicationGroup{ID: id, Description: desc, Primary: i.ID, Tags: tags}
	if err := store.Put(s.env.Store, cReplGroups, id, g); err != nil {
		return nil, err
	}
	return map[string]any{"ReplicationGroup": s.groupXML(g)}, nil
}

func (s *Service) ecDescribeGroups(q *awsapi.Req) (any, error) {
	id := strings.ToLower(q.Param("ReplicationGroupId"))
	res := "*"
	if id != "" {
		res = s.groupARN(id)
	}
	if err := q.Authorize("elasticache:DescribeReplicationGroups", res); err != nil {
		return nil, err
	}
	out := awsapi.Named{Name: "ReplicationGroup"}
	if id != "" {
		g, err := s.group(id)
		if err != nil {
			return nil, err
		}
		out.Values = append(out.Values, s.groupXML(g))
	} else {
		all := store.List[ReplicationGroup](s.env.Store, cReplGroups)
		slices.SortFunc(all, func(a, b ReplicationGroup) int { return strings.Compare(a.ID, b.ID) })
		for _, g := range all {
			out.Values = append(out.Values, s.groupXML(g))
		}
	}
	return map[string]any{"ReplicationGroups": out}, nil
}

func (s *Service) ecModifyGroup(q *awsapi.Req) (any, error) {
	id := strings.ToLower(q.Param("ReplicationGroupId"))
	if err := q.Authorize("elasticache:ModifyReplicationGroup", s.groupARN(id)); err != nil {
		return nil, err
	}
	g, err := s.group(id)
	if err != nil {
		return nil, err
	}
	i, err := s.cacheInstance(g.Primary)
	if err != nil {
		return nil, err
	}
	if q.ParamBool("AutomaticFailoverEnabled", false) || q.ParamBool("MultiAZEnabled", false) {
		return nil, apiErr("InvalidParameterCombination", "Automatic failover and Multi-AZ need at least one replica, which HomeCloud does not run")
	}
	if p := q.Param("PrimaryClusterId"); p != "" && p != g.Primary {
		return nil, apiErr("InvalidParameterValue", "Cluster %s is not a member of replication group %s", p, id)
	}
	in, err := s.cacheModify(q, i)
	if err != nil {
		return nil, err
	}
	if _, err := s.modifyInstance(i.ID, in); err != nil {
		return nil, err
	}
	if has(q, "ReplicationGroupDescription") {
		g, err = store.Update(s.env.Store, cReplGroups, id, func(x *ReplicationGroup) error { x.Description = q.Param("ReplicationGroupDescription"); return nil })
		if err != nil {
			return nil, err
		}
	}
	return map[string]any{"ReplicationGroup": s.groupXML(g)}, nil
}

func (s *Service) ecDeleteGroup(q *awsapi.Req) (any, error) {
	id := strings.ToLower(q.Param("ReplicationGroupId"))
	if err := q.Authorize("elasticache:DeleteReplicationGroup", s.groupARN(id)); err != nil {
		return nil, err
	}
	g, err := s.group(id)
	if err != nil {
		return nil, err
	}
	i, err := s.cacheInstance(g.Primary)
	if err != nil {
		return nil, err
	}
	out := s.groupXML(g)
	if q.ParamBool("RetainPrimaryCluster", false) {
		if q.Param("FinalSnapshotIdentifier") != "" {
			return nil, apiErr("InvalidParameterCombination", "FinalSnapshotIdentifier cannot be used with RetainPrimaryCluster")
		}
		// The primary lives on as a standalone cluster.
		if _, err := store.Update(s.env.Store, cInstances, i.ID, func(x *Instance) error { x.ReplicationGroup = ""; return nil }); err != nil {
			return nil, err
		}
	} else {
		final, err := s.finalSnapshot(q, i)
		if err != nil {
			return nil, err
		}
		if err := s.deleteInstance(q.R.Context(), i.ID, final, true); err != nil {
			return nil, err
		}
		out["Status"] = "deleting"
	}
	return map[string]any{"ReplicationGroup": out}, store.Delete(s.env.Store, cReplGroups, id)
}

// ---- cache subnet groups ----

func (s *Service) cacheSubnetGroupXML(g SubnetGroup) map[string]any {
	subs := awsapi.Named{Name: "Subnet"}
	for _, id := range g.SubnetIDs {
		az := ""
		if sn, err := s.vpc.GetSubnet(id); err == nil {
			az = sn.AvailabilityZone
		}
		subs.Values = append(subs.Values, map[string]any{"SubnetIdentifier": id, "SubnetAvailabilityZone": map[string]any{"Name": az},
			"SupportedNetworkTypes": named("member", "ipv4")})
	}
	return map[string]any{"CacheSubnetGroupName": g.Name, "CacheSubnetGroupDescription": g.Description, "VpcId": g.VpcID, "Subnets": subs,
		"ARN": s.cacheSubnetARN(g.Name), "SupportedNetworkTypes": named("member", "ipv4")}
}

func (s *Service) ecCreateSubnetGroup(q *awsapi.Req) (any, error) {
	name := strings.ToLower(q.Param("CacheSubnetGroupName"))
	if err := q.Authorize("elasticache:CreateCacheSubnetGroup", s.cacheSubnetARN(name)); err != nil {
		return nil, err
	}
	tags := tagsIn(q)
	if len(tags) > 0 {
		if err := q.Check("elasticache:AddTagsToResource", s.cacheSubnetARN(name)); err != nil {
			return nil, err
		}
	}
	if !groupNameRe.MatchString(name) || name == "default" {
		return nil, apiErr("InvalidParameterValue", "The parameter CacheSubnetGroupName is not a valid identifier.")
	}
	if store.Has(s.env.Store, cCacheSubnetGroups, name) {
		return nil, apiErr("CacheSubnetGroupAlreadyExists", "Cache subnet group %s already exists.", name)
	}
	ids := list(q, "SubnetIds", "SubnetIdentifier")
	vpcID, err := s.checkSubnets(ids)
	if err != nil {
		return nil, err
	}
	g := SubnetGroup{Name: name, Description: q.Param("CacheSubnetGroupDescription"), VpcID: vpcID, SubnetIDs: ids, Tags: tags}
	if err := store.Put(s.env.Store, cCacheSubnetGroups, name, g); err != nil {
		return nil, err
	}
	return map[string]any{"CacheSubnetGroup": s.cacheSubnetGroupXML(g)}, nil
}

func (s *Service) ecDescribeSubnetGroups(q *awsapi.Req) (any, error) {
	name := strings.ToLower(q.Param("CacheSubnetGroupName"))
	res := "*"
	if name != "" {
		res = s.cacheSubnetARN(name)
	}
	if err := q.Authorize("elasticache:DescribeCacheSubnetGroups", res); err != nil {
		return nil, err
	}
	out := awsapi.Named{Name: "CacheSubnetGroup"}
	if name != "" {
		g, err := store.Get[SubnetGroup](s.env.Store, cCacheSubnetGroups, name)
		if err != nil {
			return nil, subnetGroupNotFound(name)
		}
		out.Values = append(out.Values, s.cacheSubnetGroupXML(g))
	} else {
		all := store.List[SubnetGroup](s.env.Store, cCacheSubnetGroups)
		slices.SortFunc(all, func(a, b SubnetGroup) int { return strings.Compare(a.Name, b.Name) })
		for _, g := range all {
			out.Values = append(out.Values, s.cacheSubnetGroupXML(g))
		}
	}
	return map[string]any{"CacheSubnetGroups": out}, nil
}

func (s *Service) ecModifySubnetGroup(q *awsapi.Req) (any, error) {
	name := strings.ToLower(q.Param("CacheSubnetGroupName"))
	if err := q.Authorize("elasticache:ModifyCacheSubnetGroup", s.cacheSubnetARN(name)); err != nil {
		return nil, err
	}
	if !store.Has(s.env.Store, cCacheSubnetGroups, name) {
		return nil, subnetGroupNotFound(name)
	}
	ids := list(q, "SubnetIds", "SubnetIdentifier")
	vpcID, err := s.checkSubnets(ids)
	if err != nil {
		return nil, err
	}
	g, err := store.Update(s.env.Store, cCacheSubnetGroups, name, func(x *SubnetGroup) error {
		x.SubnetIDs, x.VpcID = ids, vpcID
		if has(q, "CacheSubnetGroupDescription") {
			x.Description = q.Param("CacheSubnetGroupDescription")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"CacheSubnetGroup": s.cacheSubnetGroupXML(g)}, nil
}

func (s *Service) ecDeleteSubnetGroup(q *awsapi.Req) (any, error) {
	name := strings.ToLower(q.Param("CacheSubnetGroupName"))
	if err := q.Authorize("elasticache:DeleteCacheSubnetGroup", s.cacheSubnetARN(name)); err != nil {
		return nil, err
	}
	if !store.Has(s.env.Store, cCacheSubnetGroups, name) {
		return nil, subnetGroupNotFound(name)
	}
	for _, i := range store.List[Instance](s.env.Store, cInstances) {
		if i.Kind == "cache" && i.SubnetGroup == name {
			return nil, apiErr("CacheSubnetGroupInUse", "Cache subnet group %s is currently in use by cache cluster %s.", name, i.ID)
		}
	}
	return nil, store.Delete(s.env.Store, cCacheSubnetGroups, name)
}

// ---- snapshots ----

func (s *Service) cacheSnapshotXML(sn Snapshot) map[string]any {
	src, _ := store.Get[Instance](s.env.Store, cInstances, sn.SourceInstance)
	m := map[string]any{
		"SnapshotName": sn.ID, "CacheClusterId": sn.SourceInstance, "SnapshotStatus": sn.Status, "SnapshotSource": sn.Type,
		"Engine": sn.Engine, "EngineVersion": sn.EngineVersion, "NumCacheNodes": 1, "ARN": s.cacheSnapARN(sn.ID), "AutomaticFailover": "disabled",
		"DataTiering": "disabled", "AutoMinorVersionUpgrade": src.setting("AutoMinorVersionUpgrade") != "false",
		"SnapshotRetentionLimit": src.BackupRetentionDays, "SnapshotWindow": src.setting("SnapshotWindow"),
		"PreferredMaintenanceWindow": src.setting("PreferredMaintenanceWindow"), "CacheNodeType": src.Class,
		"PreferredAvailabilityZone": src.AvailabilityZone, "VpcId": src.VpcID, "CacheSubnetGroupName": src.SubnetGroup,
		"CacheParameterGroupName": orDefault(src.setting("CacheParameterGroupName"), "default."+cacheFamily(sn.Engine, sn.EngineVersion)),
	}
	if e, ok := findEngine(sn.Engine); ok {
		m["Port"] = e.DefaultPort
	}
	if src.ID != "" {
		m["CacheClusterCreateTime"] = src.CreatedAt
	}
	node := map[string]any{"CacheClusterId": sn.SourceInstance, "CacheNodeId": "0001", "CacheNodeCreateTime": src.CreatedAt, "SnapshotCreateTime": sn.CreatedAt}
	m["NodeSnapshots"] = named("NodeSnapshot", node)
	if sn.ReplicationGroup != "" {
		m["ReplicationGroupId"] = sn.ReplicationGroup
		m["NumNodeGroups"] = 1
		if g, err := store.Get[ReplicationGroup](s.env.Store, cReplGroups, sn.ReplicationGroup); err == nil {
			m["ReplicationGroupDescription"] = g.Description
		}
	}
	return m
}

func (s *Service) ecCreateSnapshot(q *awsapi.Req) (any, error) {
	id, cluster, rg := strings.ToLower(q.Param("SnapshotName")), strings.ToLower(q.Param("CacheClusterId")), strings.ToLower(q.Param("ReplicationGroupId"))
	if err := q.Authorize("elasticache:CreateSnapshot", s.cacheSnapARN(id)); err != nil {
		return nil, err
	}
	switch {
	case cluster != "":
		if err := q.Check("elasticache:CreateSnapshot", s.cacheARN(cluster)); err != nil {
			return nil, err
		}
	case rg != "":
		if err := q.Check("elasticache:CreateSnapshot", s.groupARN(rg)); err != nil {
			return nil, err
		}
	}
	tags := tagsIn(q)
	if len(tags) > 0 {
		if err := q.Check("elasticache:AddTagsToResource", s.cacheSnapARN(id)); err != nil {
			return nil, err
		}
	}
	if id == "" {
		return nil, apiErr("MissingParameter", "The request must contain the parameter SnapshotName")
	}
	if (cluster == "") == (rg == "") {
		return nil, apiErr("InvalidParameterCombination", "Specify exactly one of CacheClusterId and ReplicationGroupId")
	}
	if !idRe.MatchString(id) {
		return nil, apiErr("InvalidParameterValue", "The parameter SnapshotName is not a valid identifier.")
	}
	if rg != "" {
		g, err := s.group(rg)
		if err != nil {
			return nil, err
		}
		cluster = g.Primary
	}
	i, err := s.cacheInstance(cluster)
	if err != nil {
		return nil, err
	}
	if e, _ := findEngine(i.Engine); e.dump == nil {
		return nil, apiErr("SnapshotFeatureNotSupportedFault", "Snapshots are not supported for %s", e.Label)
	}
	if store.Has(s.env.Store, cSnapshots, id) {
		return nil, apiErr("SnapshotAlreadyExistsFault", "Snapshot %s already exists.", id)
	}
	sn, err := s.takeSnapshot(q.R.Context(), i.ID, id, tags)
	if err != nil {
		return nil, err
	}
	return map[string]any{"Snapshot": s.cacheSnapshotXML(sn)}, nil
}

func (s *Service) ecDescribeSnapshots(q *awsapi.Req) (any, error) {
	id, cluster, rg := strings.ToLower(q.Param("SnapshotName")), strings.ToLower(q.Param("CacheClusterId")), strings.ToLower(q.Param("ReplicationGroupId"))
	res := "*"
	if id != "" {
		res = s.cacheSnapARN(id)
	}
	if err := q.Authorize("elasticache:DescribeSnapshots", res); err != nil {
		return nil, err
	}
	src := q.Param("SnapshotSource")
	all := store.List[Snapshot](s.env.Store, cSnapshots)
	slices.SortFunc(all, func(a, b Snapshot) int { return a.CreatedAt.Compare(b.CreatedAt) })
	out := awsapi.Named{Name: "Snapshot"}
	for _, sn := range all {
		if sn.Kind != "cache" {
			continue
		}
		if (id != "" && sn.ID != id) || (cluster != "" && sn.SourceInstance != cluster) || (rg != "" && sn.ReplicationGroup != rg) || (src != "" && sn.Type != src) {
			continue
		}
		out.Values = append(out.Values, s.cacheSnapshotXML(sn.view()))
	}
	if id != "" && len(out.Values) == 0 {
		return nil, snapshotNotFound(id)
	}
	return map[string]any{"Snapshots": out}, nil
}

func (s *Service) ecDeleteSnapshot(q *awsapi.Req) (any, error) {
	id := strings.ToLower(q.Param("SnapshotName"))
	if err := q.Authorize("elasticache:DeleteSnapshot", s.cacheSnapARN(id)); err != nil {
		return nil, err
	}
	sn, err := store.Get[Snapshot](s.env.Store, cSnapshots, id)
	if err != nil || sn.Kind != "cache" {
		return nil, snapshotNotFound(id)
	}
	if sn.Status == "creating" {
		return nil, apiErr("InvalidSnapshotState", "Cannot delete the snapshot %s because it is being created", id)
	}
	s.removeSnapshot(id)
	sn.Status = "deleting"
	return map[string]any{"Snapshot": s.cacheSnapshotXML(sn.view())}, nil
}

// ---- engines ----

func (s *Service) ecDescribeEngineVersions(q *awsapi.Req) (any, error) {
	if err := q.Authorize("elasticache:DescribeCacheEngineVersions", "*"); err != nil {
		return nil, err
	}
	eng, ver, fam := q.Param("Engine"), q.Param("EngineVersion"), q.Param("CacheParameterGroupFamily")
	out := awsapi.Named{Name: "CacheEngineVersion"}
	for _, e := range engines {
		if e.Kind != "cache" || (eng != "" && eng != e.Name) {
			continue
		}
		for n, v := range e.Versions {
			f := cacheFamily(e.Name, v)
			if (ver != "" && ver != v) || (fam != "" && fam != f) || (q.ParamBool("DefaultOnly", false) && n > 0) {
				continue
			}
			out.Values = append(out.Values, map[string]any{"Engine": e.Name, "EngineVersion": v, "CacheParameterGroupFamily": f,
				"CacheEngineDescription": e.Label, "CacheEngineVersionDescription": e.Label + " " + v})
		}
	}
	return map[string]any{"CacheEngineVersions": out}, nil
}

// ---- tags ----

// editCacheTags applies fn to the tags of the resource named by arn and stores the result.
func (s *Service) editCacheTags(arn string, fn func(core.Tags) core.Tags) (core.Tags, error) {
	parts := strings.SplitN(arn, ":", 7) // arn:aws:elasticache:region:account:kind:id
	if len(parts) != 7 || parts[2] != "elasticache" {
		return nil, apiErr("InvalidARN", "Invalid ARN: %s", arn)
	}
	kind, id := parts[5], strings.ToLower(parts[6])
	switch kind {
	case "cluster":
		i, err := s.cacheInstance(id)
		if err != nil {
			return nil, err
		}
		t := fn(i.Tags)
		_, err = store.Update(s.env.Store, cInstances, id, func(x *Instance) error { x.Tags = t; return nil })
		return t, err
	case "replicationgroup":
		g, err := s.group(id)
		if err != nil {
			return nil, err
		}
		t := fn(g.Tags)
		if _, err := store.Update(s.env.Store, cReplGroups, id, func(x *ReplicationGroup) error { x.Tags = t; return nil }); err != nil {
			return nil, err
		}
		// A group's tags apply to its member.
		_, err = store.Update(s.env.Store, cInstances, g.Primary, func(x *Instance) error { x.Tags = t; return nil })
		return t, err
	case "snapshot":
		sn, err := store.Get[Snapshot](s.env.Store, cSnapshots, id)
		if err != nil || sn.Kind != "cache" {
			return nil, snapshotNotFound(id)
		}
		t := fn(sn.Tags)
		_, err = store.Update(s.env.Store, cSnapshots, id, func(x *Snapshot) error { x.Tags = t; return nil })
		return t, err
	case "subnetgroup":
		g, err := store.Get[SubnetGroup](s.env.Store, cCacheSubnetGroups, id)
		if err != nil {
			return nil, subnetGroupNotFound(id)
		}
		t := fn(g.Tags)
		_, err = store.Update(s.env.Store, cCacheSubnetGroups, id, func(x *SubnetGroup) error { x.Tags = t; return nil })
		return t, err
	}
	return nil, apiErr("InvalidARN", "Unsupported resource type in %s", arn)
}

func (s *Service) ecListTags(q *awsapi.Req) (any, error) {
	arn := q.Param("ResourceName")
	if err := q.Authorize("elasticache:ListTagsForResource", arn); err != nil {
		return nil, err
	}
	t, err := s.editCacheTags(arn, func(t core.Tags) core.Tags { return t })
	if err != nil {
		return nil, err
	}
	return map[string]any{"TagList": tagList(t)}, nil
}

func (s *Service) ecAddTags(q *awsapi.Req) (any, error) {
	arn := q.Param("ResourceName")
	if err := q.Authorize("elasticache:AddTagsToResource", arn); err != nil {
		return nil, err
	}
	in := tagsIn(q)
	if len(in) == 0 {
		return nil, apiErr("MissingParameter", "The request must contain the parameter Tags")
	}
	t, err := s.editCacheTags(arn, func(t core.Tags) core.Tags {
		out := core.Tags{}
		for k, v := range t {
			out[k] = v
		}
		for k, v := range in {
			out[k] = v
		}
		return out
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"TagList": tagList(t)}, nil
}

func (s *Service) ecRemoveTags(q *awsapi.Req) (any, error) {
	arn := q.Param("ResourceName")
	if err := q.Authorize("elasticache:RemoveTagsFromResource", arn); err != nil {
		return nil, err
	}
	keys := list(q, "TagKeys", "member")
	t, err := s.editCacheTags(arn, func(t core.Tags) core.Tags {
		out := core.Tags{}
		for k, v := range t {
			if !slices.Contains(keys, k) {
				out[k] = v
			}
		}
		return out
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"TagList": tagList(t)}, nil
}
