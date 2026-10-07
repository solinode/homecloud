package rds

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi"
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/store"
)

// The AWS RDS API (awsQuery, 2014-10-31) for the relational engines:
// instances, DB subnet groups, DB parameter groups and DB snapshots.

const xmlns = "http://rds.amazonaws.com/doc/2014-10-31/"

const (
	cSubnetGroups = "rds_subnet_groups"
	cParamGroups  = "rds_param_groups"
)

type awsOp func(q *awsapi.Req) (any, error)

// RegisterAWS serves RDS over the AWS protocol.
func (s *Service) RegisterAWS() {
	ops := map[string]awsOp{
		"CreateDBInstance":                   s.awsCreateInstance,
		"DescribeDBInstances":                s.awsDescribeInstances,
		"ModifyDBInstance":                   s.awsModifyInstance,
		"DeleteDBInstance":                   s.awsDeleteInstance,
		"RebootDBInstance":                   s.awsRebootInstance,
		"StartDBInstance":                    s.awsStartInstance,
		"StopDBInstance":                     s.awsStopInstance,
		"CreateDBSubnetGroup":                s.awsCreateSubnetGroup,
		"DescribeDBSubnetGroups":             s.awsDescribeSubnetGroups,
		"ModifyDBSubnetGroup":                s.awsModifySubnetGroup,
		"DeleteDBSubnetGroup":                s.awsDeleteSubnetGroup,
		"CreateDBParameterGroup":             s.awsCreateParamGroup,
		"DescribeDBParameterGroups":          s.awsDescribeParamGroups,
		"ModifyDBParameterGroup":             s.awsModifyParamGroup,
		"ResetDBParameterGroup":              s.awsResetParamGroup,
		"DeleteDBParameterGroup":             s.awsDeleteParamGroup,
		"DescribeDBParameters":               s.awsDescribeParameters,
		"DescribeEngineDefaultParameters":    s.awsDescribeEngineDefaults,
		"CreateDBSnapshot":                   s.awsCreateSnapshot,
		"DescribeDBSnapshots":                s.awsDescribeSnapshots,
		"DeleteDBSnapshot":                   s.awsDeleteSnapshot,
		"RestoreDBInstanceFromDBSnapshot":    s.awsRestore,
		"DescribeDBEngineVersions":           s.awsDescribeEngineVersions,
		"DescribeOrderableDBInstanceOptions": s.awsDescribeOrderable,
		"AddTagsToResource":                  s.awsAddTags,
		"RemoveTagsFromResource":             s.awsRemoveTags,
		"ListTagsForResource":                s.awsListTags,
		"DescribeDBLogFiles":                 s.awsDescribeLogFiles,
		"DescribePendingMaintenanceActions":  s.awsPendingMaintenance,
	}
	svc := &awsapi.Service{Name: "rds", XMLNS: xmlns, Ops: map[string]awsapi.Op{}}
	for name, fn := range ops {
		svc.Ops[name] = func(q *awsapi.Req) (any, error) {
			out, err := fn(q)
			if err != nil {
				return nil, rdsError(err)
			}
			if out == nil {
				return map[string]any{}, nil
			}
			return out, nil
		}
	}
	awsapi.Register(svc)
	s.registerElastiCache()
}

func apiErr(code, format string, a ...any) error {
	return awsapi.Errorf(http.StatusBadRequest, code, format, a...)
}

func notFound(code, format string, a ...any) error {
	return awsapi.Errorf(http.StatusNotFound, code, format, a...)
}

// rdsError maps HomeCloud errors to RDS error codes.
func rdsError(err error) error {
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
		status = http.StatusNotFound
		switch {
		case strings.HasPrefix(msg, "snapshot "):
			code = "DBSnapshotNotFound"
		case strings.HasPrefix(msg, "subnet "):
			code, status = "InvalidSubnet", http.StatusBadRequest
		case strings.HasPrefix(msg, "security group "):
			code, status = "InvalidParameterValue", http.StatusBadRequest
		default:
			code = "DBInstanceNotFound"
		}
	case "ResourceConflict", "Conflict", "AlreadyExists":
		if strings.HasPrefix(msg, "snapshot ") {
			code = "DBSnapshotAlreadyExists"
		} else {
			code = "DBInstanceAlreadyExists"
		}
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

// ---- request parsing ----

func has(q *awsapi.Req, name string) bool { _, ok := q.Form[name]; return ok }

// list reads "Name.Elem.N", "Name.member.N" or "Name.N".
func list(q *awsapi.Req, name, elem string) []string {
	for _, p := range []string{name + "." + elem + ".", name + ".member.", name + "."} {
		var out []string
		for i := 1; ; i++ {
			v, ok := q.Form[p+strconv.Itoa(i)]
			if !ok {
				break
			}
			out = append(out, v[0])
		}
		if len(out) > 0 {
			return out
		}
	}
	return nil
}

func structs(q *awsapi.Req, name, elem string) []map[string]string {
	if v := q.Structs(name + "." + elem); len(v) > 0 {
		return v
	}
	return q.Structs(name)
}

func tagsIn(q *awsapi.Req) core.Tags {
	ts := structs(q, "Tags", "Tag")
	if len(ts) == 0 {
		return nil
	}
	m := core.Tags{}
	for _, t := range ts {
		m[t["Key"]] = t["Value"]
	}
	return m
}

type filter struct {
	Name   string
	Values []string
}

func filtersIn(q *awsapi.Req) []filter {
	var out []filter
	for _, f := range structs(q, "Filters", "Filter") {
		fl := filter{Name: f["Name"]}
		for i := 1; ; i++ {
			v, ok := f["Values.Value."+strconv.Itoa(i)]
			if !ok {
				if v, ok = f["Values.member."+strconv.Itoa(i)]; !ok {
					break
				}
			}
			fl.Values = append(fl.Values, v)
		}
		out = append(out, fl)
	}
	return out
}

func filterMatch(fs []filter, name string, value string) bool {
	for _, f := range fs {
		if f.Name != name {
			continue
		}
		ok := false
		for _, v := range f.Values {
			// The id filters accept ARNs as well as names.
			if v == value || strings.HasSuffix(v, ":"+value) {
				ok = true
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

func named(name string, vals ...any) awsapi.Named { return awsapi.Named{Name: name, Values: vals} }

func tagList(t core.Tags) awsapi.Named {
	keys := make([]string, 0, len(t))
	for k := range t {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	n := awsapi.Named{Name: "Tag"}
	for _, k := range keys {
		n.Values = append(n.Values, map[string]any{"Key": k, "Value": t[k]})
	}
	return n
}

// ---- ARNs ----

func (s *Service) dbARN(id string) string     { return s.env.ARN("rds", "db:"+id) }
func (s *Service) snapARN(id string) string   { return s.env.ARN("rds", "snapshot:"+id) }
func (s *Service) subgrpARN(id string) string { return s.env.ARN("rds", "subgrp:"+id) }
func (s *Service) pgARN(id string) string     { return s.env.ARN("rds", "pg:"+id) }

// ---- instances ----

func resourceID(id string) string {
	h := sha1.Sum([]byte(id))
	return "db-" + strings.ToUpper(hex.EncodeToString(h[:])[:26])
}

func awsStatus(st string) string {
	if st == "restoring" {
		return "creating"
	}
	return st
}

var defaultSettings = map[string]string{
	"PreferredBackupWindow": "03:00-04:00", "PreferredMaintenanceWindow": "sun:05:00-sun:06:00",
	"AutoMinorVersionUpgrade": "true", "CopyTagsToSnapshot": "false", "StorageType": "gp2", "MultiAZ": "false",
	"StorageEncrypted": "false", "CACertificateIdentifier": "rds-ca-rsa2048-g1", "NetworkType": "IPV4",
	"IAMDatabaseAuthenticationEnabled": "false", "PerformanceInsightsEnabled": "false", "MonitoringInterval": "0",
}

// settingKeys maps the request attributes HomeCloud stores and reports without acting on them.
var settingKeys = map[string]string{
	"PreferredBackupWindow": "PreferredBackupWindow", "PreferredMaintenanceWindow": "PreferredMaintenanceWindow",
	"AutoMinorVersionUpgrade": "AutoMinorVersionUpgrade", "CopyTagsToSnapshot": "CopyTagsToSnapshot",
	"StorageType": "StorageType", "MultiAZ": "MultiAZ", "StorageEncrypted": "StorageEncrypted",
	"CACertificateIdentifier": "CACertificateIdentifier", "NetworkType": "NetworkType", "OptionGroupName": "OptionGroupName",
	"EnableIAMDatabaseAuthentication": "IAMDatabaseAuthenticationEnabled", "MonitoringInterval": "MonitoringInterval",
	"EnablePerformanceInsights": "PerformanceInsightsEnabled", "MaxAllocatedStorage": "MaxAllocatedStorage",
	"Iops": "Iops", "LicenseModel": "LicenseModel",
}

func settingsIn(q *awsapi.Req) map[string]string {
	m := map[string]string{}
	for in, out := range settingKeys {
		if has(q, in) {
			m[out] = q.Param(in)
		}
	}
	return m
}

func family(engine, version string) string {
	if engine == "postgres" {
		return "postgres" + strings.SplitN(version, ".", 2)[0]
	}
	p := strings.SplitN(version, ".", 3)
	if len(p) < 2 {
		return engine + p[0]
	}
	return engine + p[0] + "." + p[1]
}

func licenseModel(engine string) string {
	if engine == "postgres" {
		return "postgresql-license"
	}
	return "general-public-license"
}

func orDefault(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

func atoi(v string) int { n, _ := strconv.Atoi(v); return n }

func (s *Service) instanceXML(i Instance) map[string]any {
	set := func(k string) string {
		if v, ok := i.Settings[k]; ok {
			return v
		}
		return defaultSettings[k]
	}
	b := func(k string) bool { return set(k) == "true" }
	fam := family(i.Engine, i.EngineVersion)
	m := map[string]any{
		"DBInstanceIdentifier": i.ID, "DBInstanceArn": i.ARN, "DbiResourceId": resourceID(i.ID),
		"DBInstanceClass": i.Class, "Engine": i.Engine, "EngineVersion": i.EngineVersion,
		"DBInstanceStatus": awsStatus(i.Status), "MasterUsername": i.MasterUsername, "AllocatedStorage": i.StorageGB,
		"InstanceCreateTime": i.CreatedAt, "BackupRetentionPeriod": i.BackupRetentionDays,
		"PreferredBackupWindow": set("PreferredBackupWindow"), "PreferredMaintenanceWindow": set("PreferredMaintenanceWindow"),
		"MultiAZ": b("MultiAZ"), "AutoMinorVersionUpgrade": b("AutoMinorVersionUpgrade"), "PubliclyAccessible": i.PubliclyAccessible,
		"LicenseModel": orDefault(set("LicenseModel"), licenseModel(i.Engine)), "StorageType": set("StorageType"), "StorageEncrypted": b("StorageEncrypted"),
		"CopyTagsToSnapshot": b("CopyTagsToSnapshot"), "DeletionProtection": i.DeletionProtection,
		"IAMDatabaseAuthenticationEnabled": b("IAMDatabaseAuthenticationEnabled"), "PerformanceInsightsEnabled": b("PerformanceInsightsEnabled"),
		"CACertificateIdentifier": set("CACertificateIdentifier"), "NetworkType": set("NetworkType"),
		"AvailabilityZone":                 i.AvailabilityZone,
		"ReadReplicaDBInstanceIdentifiers": named("ReadReplicaDBInstanceIdentifier"),
		"DBSecurityGroups":                 named("DBSecurityGroup"),
		"StatusInfos":                      named("DBInstanceStatusInfo"),
		"DomainMemberships":                named("DomainMembership"),
		"MonitoringInterval":               atoi(set("MonitoringInterval")),
		"OptionGroupMemberships": named("OptionGroupMembership", map[string]any{
			"OptionGroupName": orDefault(set("OptionGroupName"), "default:"+strings.ReplaceAll(fam, ".", "-")), "Status": "in-sync"}),
		"DBParameterGroups": named("DBParameterGroup", map[string]any{
			"DBParameterGroupName": orDefault(i.ParameterGroup, "default."+fam), "ParameterApplyStatus": "in-sync"}),
		"TagList": tagList(i.Tags),
	}
	if n := atoi(set("MaxAllocatedStorage")); n > 0 {
		m["MaxAllocatedStorage"] = n
	}
	if n := atoi(set("Iops")); n > 0 {
		m["Iops"] = n
	}
	if i.DBName != "" {
		m["DBName"] = i.DBName
	}
	sgs := awsapi.Named{Name: "VpcSecurityGroupMembership"}
	for _, g := range i.SecurityGroups {
		sgs.Values = append(sgs.Values, map[string]any{"VpcSecurityGroupId": g, "Status": "active"})
	}
	m["VpcSecurityGroups"] = sgs
	if i.Status != "creating" && i.Status != "restoring" && i.Status != "failed" && i.Status != "deleting" {
		host, port := i.Endpoint.Address, i.Endpoint.Port
		if i.PubliclyAccessible && i.Endpoint.PublicHost != "" {
			host = i.Endpoint.PublicHost
			if i.Endpoint.PublicPort != 0 {
				port = i.Endpoint.PublicPort
			}
		}
		m["Endpoint"] = map[string]any{"Address": host, "Port": port, "HostedZoneId": "Z2R2ITUGPM61AM"}
	}
	if i.SubnetGroup != "" {
		if g, err := store.Get[SubnetGroup](s.env.Store, cSubnetGroups, i.SubnetGroup); err == nil {
			m["DBSubnetGroup"] = s.subnetGroupXML(g)
		}
	}
	if i.ManagedSecret {
		ms := map[string]any{"SecretStatus": "active"}
		if arn, ok := s.secrets.ARN(i.SecretName); ok {
			ms["SecretArn"] = arn
		}
		m["MasterUserSecret"] = ms
	}
	if i.Status == "stopped" || i.Status == "available" {
		m["LatestRestorableTime"] = core.Now()
	}
	return m
}

func (s *Service) awsInstance(id string) (Instance, error) {
	i, err := s.get(strings.ToLower(id))
	if err != nil || i.Kind != "relational" {
		return i, notFound("DBInstanceNotFound", "DBInstance %s not found.", id)
	}
	return i, nil
}

type placement struct {
	subnetID string
	sgs      []string
	group    string
}

// resolvePlacement checks the subnet group and security groups of a request.
func (s *Service) resolvePlacement(q *awsapi.Req) (placement, error) {
	var r placement
	if g := strings.ToLower(q.Param("DBSubnetGroupName")); g != "" {
		sg, err := store.Get[SubnetGroup](s.env.Store, cSubnetGroups, g)
		if err != nil {
			return r, notFound("DBSubnetGroupNotFoundFault", "DB subnet group '%s' does not exist.", g)
		}
		r.group = g
		if len(sg.SubnetIDs) > 0 {
			r.subnetID = sg.SubnetIDs[0]
		}
	}
	r.sgs = list(q, "VpcSecurityGroupIds", "VpcSecurityGroupId")
	for _, g := range r.sgs {
		if _, err := s.vpc.GetSecurityGroup(g); err != nil {
			return r, apiErr("InvalidParameterValue", "The security group '%s' does not exist", g)
		}
	}
	return r, nil
}

func (s *Service) awsCreateInstance(q *awsapi.Req) (any, error) {
	id := strings.ToLower(q.Param("DBInstanceIdentifier"))
	if err := q.Authorize("rds:CreateDBInstance", s.dbARN(id)); err != nil {
		return nil, err
	}
	tags := tagsIn(q)
	if len(tags) > 0 {
		if err := q.Check("rds:AddTagsToResource", s.dbARN(id)); err != nil {
			return nil, err
		}
	}
	if id == "" {
		return nil, apiErr("MissingParameter", "The request must contain the parameter DBInstanceIdentifier")
	}
	engine := strings.ToLower(q.Param("Engine"))
	if engine == "" {
		return nil, apiErr("MissingParameter", "The request must contain the parameter Engine")
	}
	e, ok := findEngine(engine)
	if !ok || e.Kind != "relational" {
		return nil, apiErr("InvalidParameterValue", "Invalid DB engine: %s (supported: postgres, mysql, mariadb)", q.Param("Engine"))
	}
	pl, err := s.resolvePlacement(q)
	if err != nil {
		return nil, err
	}
	in, err := s.createInputFrom(q, id, pl)
	if err != nil {
		return nil, err
	}
	in.Engine, in.Tags = engine, tags
	// Inside the VPC a database listens on its engine's port: asking for that
	// port (as Terraform's rds module does) is fine, any other is not.
	if !in.PubliclyAccessible && in.Port == e.DefaultPort {
		in.Port = 0
	}
	if in.Port != 0 && !in.PubliclyAccessible {
		return nil, apiErr("InvalidParameterValue", "Port %d is not supported for a database that is not publicly accessible; use %d", in.Port, e.DefaultPort)
	}
	i, err := s.provision(in, nil)
	if err != nil {
		return nil, err
	}
	return map[string]any{"DBInstance": s.instanceXML(i)}, nil
}

// createInputFrom reads the create/restore attributes shared by both calls.
// A Port for a database that is not publicly accessible is left in in.Port for the caller to check.
func (s *Service) createInputFrom(q *awsapi.Req, id string, pl placement) (createInput, error) {
	in := createInput{
		ID: id, EngineVersion: q.Param("EngineVersion"), Class: q.Param("DBInstanceClass"), StorageGB: q.ParamInt("AllocatedStorage", 0),
		MasterUsername: q.Param("MasterUsername"), MasterPassword: q.Param("MasterUserPassword"), DBName: q.Param("DBName"),
		SubnetID: pl.subnetID, SubnetGroup: pl.group, SecurityGroups: pl.sgs, PubliclyAccessible: q.ParamBool("PubliclyAccessible", false),
		DeletionProtection: q.ParamBool("DeletionProtection", false), ManagedSecret: q.ParamBool("ManageMasterUserPassword", false),
		Settings: settingsIn(q), Port: q.ParamInt("Port", 0),
	}
	if has(q, "BackupRetentionPeriod") {
		n := q.ParamInt("BackupRetentionPeriod", 1)
		in.BackupRetentionDays = &n
	}
	if pg := strings.ToLower(q.Param("DBParameterGroupName")); pg != "" {
		if _, err := s.paramGroup(pg); err != nil {
			return in, err
		}
		in.ParameterGroup = pg
	}
	if in.ManagedSecret && in.MasterPassword != "" {
		return in, apiErr("InvalidParameterCombination", "MasterUserPassword and ManageMasterUserPassword cannot both be specified")
	}
	if in.MasterPassword != "" && (len(in.MasterPassword) < 8 || strings.ContainsAny(in.MasterPassword, `"/@ '\`)) {
		return in, apiErr("InvalidParameterValue", "The parameter MasterUserPassword is not a valid password. It must be at least 8 characters and cannot contain '/', '\"', '@', quotes, backslashes or spaces")
	}
	return in, nil
}

func (s *Service) awsDescribeInstances(q *awsapi.Req) (any, error) {
	id := strings.ToLower(q.Param("DBInstanceIdentifier"))
	res := "*"
	if id != "" {
		res = s.dbARN(id)
	}
	if err := q.Authorize("rds:DescribeDBInstances", res); err != nil {
		return nil, err
	}
	if id != "" {
		i, err := s.awsInstance(id)
		if err != nil {
			return nil, err
		}
		return map[string]any{"DBInstances": named("DBInstance", s.instanceXML(i))}, nil
	}
	fs := filtersIn(q)
	all := store.List[Instance](s.env.Store, cInstances)
	slices.SortFunc(all, func(a, b Instance) int { return strings.Compare(a.ID, b.ID) })
	out := awsapi.Named{Name: "DBInstance"}
	for _, i := range all {
		if i.Kind != "relational" {
			continue
		}
		i = s.sync(i)
		if !filterMatch(fs, "db-instance-id", i.ID) || !filterMatch(fs, "engine", i.Engine) || !filterMatch(fs, "dbi-resource-id", resourceID(i.ID)) {
			continue
		}
		out.Values = append(out.Values, s.instanceXML(i))
	}
	return map[string]any{"DBInstances": out}, nil
}

func (s *Service) awsModifyInstance(q *awsapi.Req) (any, error) {
	id := strings.ToLower(q.Param("DBInstanceIdentifier"))
	if err := q.Authorize("rds:ModifyDBInstance", s.dbARN(id)); err != nil {
		return nil, err
	}
	i, err := s.awsInstance(id)
	if err != nil {
		return nil, err
	}
	in := modifyInput{Class: q.Param("DBInstanceClass"), StorageGB: q.ParamInt("AllocatedStorage", 0), Settings: settingsIn(q)}
	if has(q, "BackupRetentionPeriod") {
		n := q.ParamInt("BackupRetentionPeriod", 0)
		in.BackupRetentionDays = &n
	}
	if has(q, "DeletionProtection") {
		b := q.ParamBool("DeletionProtection", false)
		in.DeletionProtection = &b
	}
	if v := q.Param("EngineVersion"); v != "" && v != i.EngineVersion && !strings.HasPrefix(i.EngineVersion, v+".") && !strings.HasPrefix(v, i.EngineVersion+".") {
		return nil, apiErr("InvalidParameterCombination", "HomeCloud does not support engine version upgrades")
	}
	if in.StorageGB != 0 && in.StorageGB < i.StorageGB {
		return nil, apiErr("InvalidParameterCombination", "Invalid storage size for engine %s: allocated storage cannot be decreased", i.Engine)
	}
	if has(q, "PubliclyAccessible") && q.ParamBool("PubliclyAccessible", false) != i.PubliclyAccessible {
		return nil, apiErr("InvalidParameterCombination", "HomeCloud cannot change PubliclyAccessible after creation")
	}
	if pg := strings.ToLower(q.Param("DBParameterGroupName")); pg != "" {
		if _, err := s.paramGroup(pg); err != nil {
			return nil, err
		}
		in.ParameterGroup = &pg
	}
	if has(q, "VpcSecurityGroupIds.VpcSecurityGroupId.1") || has(q, "VpcSecurityGroupIds.member.1") {
		pl, err := s.resolvePlacement(q)
		if err != nil {
			return nil, err
		}
		in.SecurityGroups = pl.sgs
	}
	if pw := q.Param("MasterUserPassword"); pw != "" {
		if _, err := s.setPassword(q.R.Context(), id, pw); err != nil {
			return nil, err
		}
	}
	out, err := s.modifyInstance(id, in)
	if err != nil {
		return nil, err
	}
	return map[string]any{"DBInstance": s.instanceXML(out)}, nil
}

func (s *Service) awsDeleteInstance(q *awsapi.Req) (any, error) {
	id := strings.ToLower(q.Param("DBInstanceIdentifier"))
	if err := q.Authorize("rds:DeleteDBInstance", s.dbARN(id)); err != nil {
		return nil, err
	}
	i, err := s.awsInstance(id)
	if err != nil {
		return nil, err
	}
	final := strings.ToLower(q.Param("FinalDBSnapshotIdentifier"))
	if !q.ParamBool("SkipFinalSnapshot", false) {
		if final == "" {
			return nil, apiErr("InvalidParameterCombination", "FinalDBSnapshotIdentifier is required unless SkipFinalSnapshot is specified.")
		}
		if err := q.Check("rds:CreateDBSnapshot", s.snapARN(final)); err != nil {
			return nil, err
		}
	} else {
		final = ""
	}
	if err := s.deleteInstance(q.R.Context(), id, final, q.ParamBool("DeleteAutomatedBackups", true)); err != nil {
		return nil, err
	}
	i.Status = "deleting"
	return map[string]any{"DBInstance": s.instanceXML(i)}, nil
}

func (s *Service) awsRebootInstance(q *awsapi.Req) (any, error) {
	return s.awsLifecycle(q, "rds:RebootDBInstance", s.rebootInstance)
}

func (s *Service) awsStartInstance(q *awsapi.Req) (any, error) {
	return s.awsLifecycle(q, "rds:StartDBInstance", s.startInstance)
}

func (s *Service) awsStopInstance(q *awsapi.Req) (any, error) {
	if q.Param("DBSnapshotIdentifier") != "" {
		return nil, apiErr("InvalidParameterCombination", "HomeCloud does not take a snapshot when stopping; call CreateDBSnapshot first")
	}
	return s.awsLifecycle(q, "rds:StopDBInstance", s.stopInstance)
}

func (s *Service) awsLifecycle(q *awsapi.Req, action string, fn func(string) (Instance, error)) (any, error) {
	id := strings.ToLower(q.Param("DBInstanceIdentifier"))
	if err := q.Authorize(action, s.dbARN(id)); err != nil {
		return nil, err
	}
	if _, err := s.awsInstance(id); err != nil {
		return nil, err
	}
	i, err := fn(id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"DBInstance": s.instanceXML(i)}, nil
}

// ---- DB subnet groups ----

type SubnetGroup struct {
	Name        string    `json:"name"`
	Description string    `json:"description"`
	VpcID       string    `json:"vpc_id"`
	SubnetIDs   []string  `json:"subnet_ids"`
	Tags        core.Tags `json:"tags,omitempty"`
}

func (s *Service) subnetGroupXML(g SubnetGroup) map[string]any {
	subs := awsapi.Named{Name: "Subnet"}
	for _, id := range g.SubnetIDs {
		az := ""
		if sn, err := s.vpc.GetSubnet(id); err == nil {
			az = sn.AvailabilityZone
		}
		subs.Values = append(subs.Values, map[string]any{"SubnetIdentifier": id, "SubnetStatus": "Active",
			"SubnetAvailabilityZone": map[string]any{"Name": az}})
	}
	return map[string]any{"DBSubnetGroupName": g.Name, "DBSubnetGroupDescription": g.Description, "VpcId": g.VpcID,
		"SubnetGroupStatus": "Complete", "Subnets": subs, "DBSubnetGroupArn": s.subgrpARN(g.Name),
		"SupportedNetworkTypes": named("member", "IPV4")}
}

var groupNameRe = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,254}$`)

func (s *Service) checkSubnets(ids []string) (vpcID string, err error) {
	if len(ids) == 0 {
		return "", apiErr("InvalidParameterValue", "Please provide at least one subnet")
	}
	for _, id := range ids {
		sn, e := s.vpc.GetSubnet(id)
		if e != nil {
			return "", apiErr("InvalidSubnet", "The subnet ID '%s' is invalid", id)
		}
		if vpcID != "" && sn.VpcID != vpcID {
			return "", apiErr("InvalidSubnet", "Some input subnets in :[%s] are invalid: subnets must belong to the same VPC", strings.Join(ids, ","))
		}
		vpcID = sn.VpcID
	}
	return vpcID, nil
}

func (s *Service) awsCreateSubnetGroup(q *awsapi.Req) (any, error) {
	name := strings.ToLower(q.Param("DBSubnetGroupName"))
	if err := q.Authorize("rds:CreateDBSubnetGroup", s.subgrpARN(name)); err != nil {
		return nil, err
	}
	tags := tagsIn(q)
	if len(tags) > 0 {
		if err := q.Check("rds:AddTagsToResource", s.subgrpARN(name)); err != nil {
			return nil, err
		}
	}
	if !groupNameRe.MatchString(name) || name == "default" {
		return nil, apiErr("InvalidParameterValue", "The parameter DBSubnetGroupName is not a valid identifier.")
	}
	if store.Has(s.env.Store, cSubnetGroups, name) {
		return nil, apiErr("DBSubnetGroupAlreadyExists", "The DB subnet group '%s' already exists.", name)
	}
	ids := list(q, "SubnetIds", "SubnetIdentifier")
	vpcID, err := s.checkSubnets(ids)
	if err != nil {
		return nil, err
	}
	g := SubnetGroup{Name: name, Description: q.Param("DBSubnetGroupDescription"), VpcID: vpcID, SubnetIDs: ids, Tags: tags}
	if err := store.Put(s.env.Store, cSubnetGroups, name, g); err != nil {
		return nil, err
	}
	return map[string]any{"DBSubnetGroup": s.subnetGroupXML(g)}, nil
}

func (s *Service) awsDescribeSubnetGroups(q *awsapi.Req) (any, error) {
	name := strings.ToLower(q.Param("DBSubnetGroupName"))
	res := "*"
	if name != "" {
		res = s.subgrpARN(name)
	}
	if err := q.Authorize("rds:DescribeDBSubnetGroups", res); err != nil {
		return nil, err
	}
	out := awsapi.Named{Name: "DBSubnetGroup"}
	if name != "" {
		g, err := store.Get[SubnetGroup](s.env.Store, cSubnetGroups, name)
		if err != nil {
			return nil, notFound("DBSubnetGroupNotFoundFault", "DB subnet group '%s' not found.", name)
		}
		out.Values = append(out.Values, s.subnetGroupXML(g))
	} else {
		all := store.List[SubnetGroup](s.env.Store, cSubnetGroups)
		slices.SortFunc(all, func(a, b SubnetGroup) int { return strings.Compare(a.Name, b.Name) })
		for _, g := range all {
			out.Values = append(out.Values, s.subnetGroupXML(g))
		}
	}
	return map[string]any{"DBSubnetGroups": out}, nil
}

func (s *Service) awsModifySubnetGroup(q *awsapi.Req) (any, error) {
	name := strings.ToLower(q.Param("DBSubnetGroupName"))
	if err := q.Authorize("rds:ModifyDBSubnetGroup", s.subgrpARN(name)); err != nil {
		return nil, err
	}
	if !store.Has(s.env.Store, cSubnetGroups, name) {
		return nil, notFound("DBSubnetGroupNotFoundFault", "DB subnet group '%s' not found.", name)
	}
	ids := list(q, "SubnetIds", "SubnetIdentifier")
	vpcID, err := s.checkSubnets(ids)
	if err != nil {
		return nil, err
	}
	g, err := store.Update(s.env.Store, cSubnetGroups, name, func(x *SubnetGroup) error {
		x.SubnetIDs, x.VpcID = ids, vpcID
		if has(q, "DBSubnetGroupDescription") {
			x.Description = q.Param("DBSubnetGroupDescription")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"DBSubnetGroup": s.subnetGroupXML(g)}, nil
}

func (s *Service) awsDeleteSubnetGroup(q *awsapi.Req) (any, error) {
	name := strings.ToLower(q.Param("DBSubnetGroupName"))
	if err := q.Authorize("rds:DeleteDBSubnetGroup", s.subgrpARN(name)); err != nil {
		return nil, err
	}
	if !store.Has(s.env.Store, cSubnetGroups, name) {
		return nil, notFound("DBSubnetGroupNotFoundFault", "DB subnet group '%s' not found.", name)
	}
	for _, i := range store.List[Instance](s.env.Store, cInstances) {
		if i.Kind == "relational" && i.SubnetGroup == name {
			return nil, apiErr("InvalidDBSubnetGroupStateFault", "Cannot delete the subnet group '%s' because at least one database instance is still using it.", name)
		}
	}
	return nil, store.Delete(s.env.Store, cSubnetGroups, name)
}

// ---- DB parameter groups ----

type ParamValue struct {
	Value  string `json:"value"`
	Method string `json:"method"`
}

type ParamGroup struct {
	Name        string                `json:"name"`
	Family      string                `json:"family"`
	Description string                `json:"description"`
	Params      map[string]ParamValue `json:"params"`
	Tags        core.Tags             `json:"tags,omitempty"`
}

var familyRe = regexp.MustCompile(`^(postgres\d+|mysql\d+\.\d+|mariadb\d+\.\d+)$`)

type paramDef struct{ name, value, apply, dtype, allowed, desc string }

var pgDefaults = []paramDef{
	{"max_connections", "LEAST({DBInstanceClassMemory/9531392},5000)", "static", "integer", "6-8388607", "Sets the maximum number of concurrent connections."},
	{"shared_buffers", "{DBInstanceClassMemory/32768}", "static", "integer", "16-1073741823", "Sets the number of shared memory buffers used by the server."},
	{"work_mem", "", "dynamic", "integer", "64-2147483647", "Sets the maximum memory to be used for query workspaces."},
	{"maintenance_work_mem", "GREATEST({DBInstanceClassMemory/63963136*1024},65536)", "dynamic", "integer", "1024-2147483647", "Sets the maximum memory to be used for maintenance operations."},
	{"log_statement", "none", "dynamic", "string", "none,ddl,mod,all", "Sets the type of statements logged."},
	{"log_min_duration_statement", "", "dynamic", "integer", "-1-2147483647", "Sets the minimum execution time above which statements will be logged."},
	{"log_connections", "", "dynamic", "boolean", "0,1", "Logs each successful connection."},
	{"rds.force_ssl", "0", "dynamic", "boolean", "0,1", "Force SSL connections."},
	{"timezone", "UTC", "dynamic", "string", "", "Sets the time zone for displaying and interpreting time stamps."},
	{"random_page_cost", "4", "dynamic", "float", "0-1.79769313486232e+308", "Sets the planner's estimate of the cost of a nonsequentially fetched disk page."},
	{"statement_timeout", "", "dynamic", "integer", "0-2147483647", "Sets the maximum allowed duration of any statement."},
	{"shared_preload_libraries", "pg_stat_statements", "static", "string", "auto_explain,pg_stat_statements,pg_hint_plan,pgaudit", "Lists shared libraries to preload into server."},
}

var myDefaults = []paramDef{
	{"max_connections", "{DBInstanceClassMemory/12582880}", "dynamic", "integer", "1-100000", "The number of simultaneous client connections allowed."},
	{"character_set_server", "utf8mb4", "dynamic", "string", "", "The server default character set."},
	{"collation_server", "utf8mb4_0900_ai_ci", "dynamic", "string", "", "The server default collation."},
	{"innodb_buffer_pool_size", "{DBInstanceClassMemory*3/4}", "dynamic", "integer", "5242880-9223372036854775807", "The size in bytes of the buffer pool."},
	{"slow_query_log", "0", "dynamic", "boolean", "0,1", "Enables the slow query log."},
	{"long_query_time", "10", "dynamic", "float", "0-31536000", "Queries slower than this many seconds are logged."},
	{"general_log", "0", "dynamic", "boolean", "0,1", "Enables the general query log."},
	{"time_zone", "UTC", "dynamic", "string", "", "The server time zone."},
	{"sql_mode", "", "dynamic", "string", "", "The SQL modes."},
	{"wait_timeout", "28800", "dynamic", "integer", "1-31536000", "Seconds the server waits for activity on a noninteractive connection."},
	{"max_allowed_packet", "67108864", "dynamic", "integer", "1024-1073741824", "The maximum size of one packet."},
}

func defaultsFor(fam string) []paramDef {
	if strings.HasPrefix(fam, "postgres") {
		return pgDefaults
	}
	return myDefaults
}

// paramGroup loads a parameter group; "default.<family>" groups exist implicitly.
func (s *Service) paramGroup(name string) (ParamGroup, error) {
	if f, ok := strings.CutPrefix(name, "default."); ok && familyRe.MatchString(f) {
		return ParamGroup{Name: name, Family: f, Description: "Default parameter group for " + f, Params: map[string]ParamValue{}}, nil
	}
	g, err := store.Get[ParamGroup](s.env.Store, cParamGroups, name)
	if err != nil {
		return g, notFound("DBParameterGroupNotFound", "DBParameterGroup not found: %s", name)
	}
	return g, nil
}

func (s *Service) paramGroupXML(g ParamGroup) map[string]any {
	return map[string]any{"DBParameterGroupName": g.Name, "DBParameterGroupFamily": g.Family, "Description": g.Description,
		"DBParameterGroupArn": s.pgARN(g.Name)}
}

func (s *Service) awsCreateParamGroup(q *awsapi.Req) (any, error) {
	name := strings.ToLower(q.Param("DBParameterGroupName"))
	if err := q.Authorize("rds:CreateDBParameterGroup", s.pgARN(name)); err != nil {
		return nil, err
	}
	tags := tagsIn(q)
	if len(tags) > 0 {
		if err := q.Check("rds:AddTagsToResource", s.pgARN(name)); err != nil {
			return nil, err
		}
	}
	fam := q.Param("DBParameterGroupFamily")
	if !groupNameRe.MatchString(name) || strings.HasPrefix(name, "default.") {
		return nil, apiErr("InvalidParameterValue", "The parameter DBParameterGroupName is not a valid identifier.")
	}
	if !familyRe.MatchString(fam) {
		return nil, apiErr("InvalidParameterValue", "DBParameterGroupFamily %s is not supported (postgresN, mysqlN.N, mariadbN.N)", fam)
	}
	if store.Has(s.env.Store, cParamGroups, name) {
		return nil, apiErr("DBParameterGroupAlreadyExists", "Parameter group %s already exists", name)
	}
	g := ParamGroup{Name: name, Family: fam, Description: q.Param("Description"), Params: map[string]ParamValue{}, Tags: tags}
	if err := store.Put(s.env.Store, cParamGroups, name, g); err != nil {
		return nil, err
	}
	return map[string]any{"DBParameterGroup": s.paramGroupXML(g)}, nil
}

func (s *Service) awsDescribeParamGroups(q *awsapi.Req) (any, error) {
	name := strings.ToLower(q.Param("DBParameterGroupName"))
	res := "*"
	if name != "" {
		res = s.pgARN(name)
	}
	if err := q.Authorize("rds:DescribeDBParameterGroups", res); err != nil {
		return nil, err
	}
	out := awsapi.Named{Name: "DBParameterGroup"}
	if name != "" {
		g, err := s.paramGroup(name)
		if err != nil {
			return nil, err
		}
		out.Values = append(out.Values, s.paramGroupXML(g))
	} else {
		all := store.List[ParamGroup](s.env.Store, cParamGroups)
		slices.SortFunc(all, func(a, b ParamGroup) int { return strings.Compare(a.Name, b.Name) })
		for _, g := range all {
			out.Values = append(out.Values, s.paramGroupXML(g))
		}
	}
	return map[string]any{"DBParameterGroups": out}, nil
}

func (s *Service) editableGroup(q *awsapi.Req, action string) (ParamGroup, error) {
	name := strings.ToLower(q.Param("DBParameterGroupName"))
	if err := q.Authorize(action, s.pgARN(name)); err != nil {
		return ParamGroup{}, err
	}
	if strings.HasPrefix(name, "default.") {
		return ParamGroup{}, apiErr("InvalidDBParameterGroupState", "You cannot modify default DB parameter group %s", name)
	}
	return s.paramGroup(name)
}

func (s *Service) awsModifyParamGroup(q *awsapi.Req) (any, error) {
	g, err := s.editableGroup(q, "rds:ModifyDBParameterGroup")
	if err != nil {
		return nil, err
	}
	ps := structs(q, "Parameters", "Parameter")
	if len(ps) == 0 {
		return nil, apiErr("MissingParameter", "The request must contain the parameter Parameters")
	}
	g, err = store.Update(s.env.Store, cParamGroups, g.Name, func(x *ParamGroup) error {
		if x.Params == nil {
			x.Params = map[string]ParamValue{}
		}
		for _, p := range ps {
			x.Params[p["ParameterName"]] = ParamValue{Value: p["ParameterValue"], Method: orDefault(p["ApplyMethod"], "immediate")}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"DBParameterGroupName": g.Name}, nil
}

func (s *Service) awsResetParamGroup(q *awsapi.Req) (any, error) {
	g, err := s.editableGroup(q, "rds:ResetDBParameterGroup")
	if err != nil {
		return nil, err
	}
	ps := structs(q, "Parameters", "Parameter")
	all := q.ParamBool("ResetAllParameters", false)
	g, err = store.Update(s.env.Store, cParamGroups, g.Name, func(x *ParamGroup) error {
		if all || len(ps) == 0 {
			x.Params = map[string]ParamValue{}
			return nil
		}
		for _, p := range ps {
			delete(x.Params, p["ParameterName"])
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"DBParameterGroupName": g.Name}, nil
}

func (s *Service) awsDeleteParamGroup(q *awsapi.Req) (any, error) {
	g, err := s.editableGroup(q, "rds:DeleteDBParameterGroup")
	if err != nil {
		return nil, err
	}
	for _, i := range store.List[Instance](s.env.Store, cInstances) {
		if i.ParameterGroup == g.Name {
			return nil, apiErr("InvalidDBParameterGroupState", "One or more database instances are still members of this parameter group %s, so the group cannot be deleted", g.Name)
		}
	}
	return nil, store.Delete(s.env.Store, cParamGroups, g.Name)
}

func paramXML(d paramDef, source string) map[string]any {
	m := map[string]any{"ParameterName": d.name, "Source": source, "ApplyType": d.apply, "DataType": d.dtype,
		"IsModifiable": true, "Description": d.desc, "MinimumEngineVersion": "1"}
	if d.value != "" {
		m["ParameterValue"] = d.value
	}
	if d.allowed != "" {
		m["AllowedValues"] = d.allowed
	}
	return m
}

// parametersFor lists a group's parameters: engine defaults overlaid with user values.
func parametersFor(g ParamGroup, source string) []any {
	names := make([]string, 0, len(g.Params))
	for k := range g.Params {
		names = append(names, k)
	}
	sort.Strings(names)
	var out []any
	seen := map[string]bool{}
	for _, d := range defaultsFor(g.Family) {
		seen[d.name] = true
		if u, ok := g.Params[d.name]; ok {
			if source == "" || source == "user" {
				m := paramXML(d, "user")
				m["ParameterValue"], m["ApplyMethod"] = u.Value, u.Method
				out = append(out, m)
			}
		} else if source == "" || source == "engine-default" || source == "system" {
			out = append(out, paramXML(d, "engine-default"))
		}
	}
	if source == "" || source == "user" {
		for _, n := range names {
			if !seen[n] {
				u := g.Params[n]
				out = append(out, map[string]any{"ParameterName": n, "ParameterValue": u.Value, "Source": "user", "ApplyType": "dynamic",
					"DataType": "string", "IsModifiable": true, "ApplyMethod": u.Method})
			}
		}
	}
	return out
}

func (s *Service) awsDescribeParameters(q *awsapi.Req) (any, error) {
	name := strings.ToLower(q.Param("DBParameterGroupName"))
	if err := q.Authorize("rds:DescribeDBParameters", s.pgARN(name)); err != nil {
		return nil, err
	}
	g, err := s.paramGroup(name)
	if err != nil {
		return nil, err
	}
	return map[string]any{"Parameters": awsapi.Named{Name: "Parameter", Values: parametersFor(g, q.Param("Source"))}}, nil
}

func (s *Service) awsDescribeEngineDefaults(q *awsapi.Req) (any, error) {
	if err := q.Authorize("rds:DescribeEngineDefaultParameters", "*"); err != nil {
		return nil, err
	}
	fam := q.Param("DBParameterGroupFamily")
	if !familyRe.MatchString(fam) {
		return nil, apiErr("InvalidParameterValue", "DBParameterGroupFamily %s is not supported", fam)
	}
	var vals []any
	for _, d := range defaultsFor(fam) {
		vals = append(vals, paramXML(d, "engine-default"))
	}
	return map[string]any{"EngineDefaults": map[string]any{"DBParameterGroupFamily": fam, "Parameters": awsapi.Named{Name: "Parameter", Values: vals}}}, nil
}

// ---- snapshots ----

func (s *Service) snapshotXML(sn Snapshot) map[string]any {
	port, az, vpc := 0, "", ""
	var created any
	if e, ok := findEngine(sn.Engine); ok {
		port = e.DefaultPort
	}
	if i, err := store.Get[Instance](s.env.Store, cInstances, sn.SourceInstance); err == nil {
		az, vpc, created = i.AvailabilityZone, i.VpcID, i.CreatedAt
		if i.PubliclyAccessible && i.Endpoint.PublicPort != 0 {
			port = i.Endpoint.PublicPort
		}
	}
	m := map[string]any{"DBSnapshotIdentifier": sn.ID, "DBSnapshotArn": s.snapARN(sn.ID), "DBInstanceIdentifier": sn.SourceInstance,
		"SnapshotCreateTime": sn.CreatedAt, "Engine": sn.Engine, "EngineVersion": sn.EngineVersion, "AllocatedStorage": sn.StorageGB,
		"Status": sn.Status, "Port": port, "AvailabilityZone": az, "VpcId": vpc, "MasterUsername": sn.MasterUsername,
		"SnapshotType": sn.Type, "LicenseModel": licenseModel(sn.Engine), "StorageType": "gp2", "Encrypted": false,
		"PercentProgress": 100, "DbiResourceId": resourceID(sn.SourceInstance), "TagList": tagList(sn.Tags),
		"IAMDatabaseAuthenticationEnabled": false, "OptionGroupName": "default:" + sn.Engine,
	}
	if created != nil {
		m["InstanceCreateTime"] = created
	}
	return m
}

func (s *Service) awsCreateSnapshot(q *awsapi.Req) (any, error) {
	id, inst := strings.ToLower(q.Param("DBSnapshotIdentifier")), strings.ToLower(q.Param("DBInstanceIdentifier"))
	if err := q.Authorize("rds:CreateDBSnapshot", s.snapARN(id)); err != nil {
		return nil, err
	}
	if err := q.Check("rds:CreateDBSnapshot", s.dbARN(inst)); err != nil {
		return nil, err
	}
	tags := tagsIn(q)
	if len(tags) > 0 {
		if err := q.Check("rds:AddTagsToResource", s.snapARN(id)); err != nil {
			return nil, err
		}
	}
	if id == "" || inst == "" {
		return nil, apiErr("MissingParameter", "DBSnapshotIdentifier and DBInstanceIdentifier are required")
	}
	if !idRe.MatchString(id) {
		return nil, apiErr("InvalidParameterValue", "The parameter DBSnapshotIdentifier is not a valid identifier.")
	}
	if _, err := s.awsInstance(inst); err != nil {
		return nil, err
	}
	sn, err := s.takeSnapshot(q.R.Context(), inst, id, tags)
	if err != nil {
		return nil, err
	}
	return map[string]any{"DBSnapshot": s.snapshotXML(sn)}, nil
}

func (s *Service) awsDescribeSnapshots(q *awsapi.Req) (any, error) {
	id, inst := strings.ToLower(q.Param("DBSnapshotIdentifier")), strings.ToLower(q.Param("DBInstanceIdentifier"))
	res := "*"
	if id != "" {
		res = s.snapARN(id)
	}
	if err := q.Authorize("rds:DescribeDBSnapshots", res); err != nil {
		return nil, err
	}
	typ := q.Param("SnapshotType")
	fs := filtersIn(q)
	all := store.List[Snapshot](s.env.Store, cSnapshots)
	slices.SortFunc(all, func(a, b Snapshot) int { return a.CreatedAt.Compare(b.CreatedAt) })
	out := awsapi.Named{Name: "DBSnapshot"}
	for _, sn := range all {
		if sn.Kind != "relational" {
			continue
		}
		if (id != "" && sn.ID != id) || (inst != "" && sn.SourceInstance != inst) || (typ != "" && sn.Type != typ) {
			continue
		}
		if !filterMatch(fs, "db-instance-id", sn.SourceInstance) || !filterMatch(fs, "db-snapshot-id", sn.ID) ||
			!filterMatch(fs, "snapshot-type", sn.Type) || !filterMatch(fs, "engine", sn.Engine) {
			continue
		}
		out.Values = append(out.Values, s.snapshotXML(sn.view()))
	}
	if id != "" && len(out.Values) == 0 {
		return nil, notFound("DBSnapshotNotFound", "DBSnapshot not found: %s", id)
	}
	return map[string]any{"DBSnapshots": out}, nil
}

func (s *Service) awsDeleteSnapshot(q *awsapi.Req) (any, error) {
	id := strings.ToLower(q.Param("DBSnapshotIdentifier"))
	if err := q.Authorize("rds:DeleteDBSnapshot", s.snapARN(id)); err != nil {
		return nil, err
	}
	sn, err := store.Get[Snapshot](s.env.Store, cSnapshots, id)
	if err != nil {
		return nil, notFound("DBSnapshotNotFound", "DBSnapshot not found: %s", id)
	}
	if sn.Status == "creating" {
		return nil, apiErr("InvalidDBSnapshotState", "Cannot delete the snapshot %s because it is being created", id)
	}
	s.removeSnapshot(id)
	sn.Status = "deleted"
	return map[string]any{"DBSnapshot": s.snapshotXML(sn.view())}, nil
}

func (s *Service) awsRestore(q *awsapi.Req) (any, error) {
	id, snapID := strings.ToLower(q.Param("DBInstanceIdentifier")), strings.ToLower(q.Param("DBSnapshotIdentifier"))
	if err := q.Authorize("rds:RestoreDBInstanceFromDBSnapshot", s.dbARN(id)); err != nil {
		return nil, err
	}
	if err := q.Check("rds:RestoreDBInstanceFromDBSnapshot", s.snapARN(snapID)); err != nil {
		return nil, err
	}
	tags := tagsIn(q)
	if len(tags) > 0 {
		if err := q.Check("rds:AddTagsToResource", s.dbARN(id)); err != nil {
			return nil, err
		}
	}
	if id == "" || snapID == "" {
		return nil, apiErr("MissingParameter", "DBInstanceIdentifier and DBSnapshotIdentifier are required")
	}
	sn, err := store.Get[Snapshot](s.env.Store, cSnapshots, snapID)
	if err != nil || sn.Kind != "relational" {
		return nil, notFound("DBSnapshotNotFound", "DBSnapshot not found: %s", snapID)
	}
	pl, err := s.resolvePlacement(q)
	if err != nil {
		return nil, err
	}
	in, err := s.createInputFrom(q, id, pl)
	if err != nil {
		return nil, err
	}
	if e, ok := findEngine(sn.Engine); ok && !in.PubliclyAccessible && in.Port == e.DefaultPort {
		in.Port = 0
	}
	if in.Port != 0 && !in.PubliclyAccessible {
		return nil, apiErr("InvalidParameterValue", "Port %d is not supported for a database that is not publicly accessible", in.Port)
	}
	// A restored database keeps the snapshot's engine, credentials and database.
	in.Tags, in.Engine, in.EngineVersion, in.MasterUsername, in.MasterPassword, in.DBName = tags, sn.Engine, "", "", "", ""
	out, err := s.restoreSnapshot(snapID, in)
	if err != nil {
		return nil, err
	}
	return map[string]any{"DBInstance": s.instanceXML(out.(Instance))}, nil
}

// ---- engines and orderable options ----

func (s *Service) awsDescribeEngineVersions(q *awsapi.Req) (any, error) {
	if err := q.Authorize("rds:DescribeDBEngineVersions", "*"); err != nil {
		return nil, err
	}
	eng, ver, fam := q.Param("Engine"), q.Param("EngineVersion"), q.Param("DBParameterGroupFamily")
	out := awsapi.Named{Name: "DBEngineVersion"}
	for _, e := range engines {
		if e.Kind != "relational" || (eng != "" && eng != e.Name) {
			continue
		}
		for _, v := range e.Versions {
			f := family(e.Name, v)
			if (ver != "" && ver != v) || (fam != "" && fam != f) {
				continue
			}
			out.Values = append(out.Values, map[string]any{"Engine": e.Name, "EngineVersion": v, "DBParameterGroupFamily": f,
				"DBEngineDescription": e.Label, "DBEngineVersionDescription": e.Label + " " + v, "Status": "available",
				"SupportsLogExportsToCloudwatchLogs": false, "SupportsReadReplica": false, "SupportsParallelQuery": false,
				"SupportsGlobalDatabases": false, "SupportedEngineModes": named("member"),
				"ValidUpgradeTarget": named("UpgradeTarget"), "ExportableLogTypes": named("member")})
		}
	}
	return map[string]any{"DBEngineVersions": out}, nil
}

func (s *Service) awsDescribeOrderable(q *awsapi.Req) (any, error) {
	if err := q.Authorize("rds:DescribeOrderableDBInstanceOptions", "*"); err != nil {
		return nil, err
	}
	eng, ver, class := q.Param("Engine"), q.Param("EngineVersion"), q.Param("DBInstanceClass")
	var azs []any
	for _, a := range []string{"a", "b", "c"} {
		azs = append(azs, map[string]any{"Name": core.Region + a})
	}
	out := awsapi.Named{Name: "OrderableDBInstanceOption"}
	for _, e := range engines {
		if e.Kind != "relational" || (eng != "" && eng != e.Name) {
			continue
		}
		for _, v := range e.Versions {
			if ver != "" && ver != v {
				continue
			}
			for _, c := range classes {
				if c.Kind != "db" || (class != "" && class != c.Name) {
					continue
				}
				out.Values = append(out.Values, map[string]any{"Engine": e.Name, "EngineVersion": v, "DBInstanceClass": c.Name,
					"LicenseModel": licenseModel(e.Name), "AvailabilityZones": awsapi.Named{Name: "AvailabilityZone", Values: azs},
					"MultiAZCapable": false, "ReadReplicaCapable": false, "Vpc": true, "SupportsStorageEncryption": true,
					"StorageType": "gp2", "SupportsIops": false, "SupportsEnhancedMonitoring": false, "SupportsIAMDatabaseAuthentication": false,
					"SupportsPerformanceInsights": false, "MinStorageSize": 20, "MaxStorageSize": 65536, "SupportsStorageAutoscaling": false,
					"SupportsKerberosAuthentication": false, "OutpostCapable": false, "SupportedNetworkTypes": named("member", "IPV4")})
			}
		}
	}
	return map[string]any{"OrderableDBInstanceOptions": out}, nil
}

// ---- tags, log files, maintenance ----

// editTags applies fn to the tags of the resource named by arn and stores the result.
func (s *Service) editTags(arn string, fn func(core.Tags) core.Tags) (core.Tags, error) {
	parts := strings.SplitN(arn, ":", 7) // arn:aws:rds:region:account:kind:id
	if len(parts) != 7 || parts[2] != "rds" {
		return nil, apiErr("InvalidParameterValue", "Invalid resource name: %s", arn)
	}
	kind, id := parts[5], parts[6]
	switch kind {
	case "db":
		i, err := s.awsInstance(id)
		if err != nil {
			return nil, err
		}
		t := fn(i.Tags)
		_, err = store.Update(s.env.Store, cInstances, id, func(x *Instance) error { x.Tags = t; return nil })
		return t, err
	case "snapshot":
		sn, err := store.Get[Snapshot](s.env.Store, cSnapshots, id)
		if err != nil {
			return nil, notFound("DBSnapshotNotFound", "DBSnapshot not found: %s", id)
		}
		t := fn(sn.Tags)
		_, err = store.Update(s.env.Store, cSnapshots, id, func(x *Snapshot) error { x.Tags = t; return nil })
		return t, err
	case "subgrp":
		g, err := store.Get[SubnetGroup](s.env.Store, cSubnetGroups, id)
		if err != nil {
			return nil, notFound("DBSubnetGroupNotFoundFault", "DB subnet group '%s' not found.", id)
		}
		t := fn(g.Tags)
		_, err = store.Update(s.env.Store, cSubnetGroups, id, func(x *SubnetGroup) error { x.Tags = t; return nil })
		return t, err
	case "pg":
		g, err := s.paramGroup(id)
		if err != nil {
			return nil, err
		}
		t := fn(g.Tags)
		if strings.HasPrefix(id, "default.") {
			return t, nil
		}
		_, err = store.Update(s.env.Store, cParamGroups, id, func(x *ParamGroup) error { x.Tags = t; return nil })
		return t, err
	}
	return nil, apiErr("InvalidParameterValue", "Unsupported resource type in %s", arn)
}

func (s *Service) awsListTags(q *awsapi.Req) (any, error) {
	arn := q.Param("ResourceName")
	if err := q.Authorize("rds:ListTagsForResource", arn); err != nil {
		return nil, err
	}
	t, err := s.editTags(arn, func(t core.Tags) core.Tags { return t })
	if err != nil {
		return nil, err
	}
	return map[string]any{"TagList": tagList(t)}, nil
}

func (s *Service) awsAddTags(q *awsapi.Req) (any, error) {
	arn := q.Param("ResourceName")
	if err := q.Authorize("rds:AddTagsToResource", arn); err != nil {
		return nil, err
	}
	in := tagsIn(q)
	_, err := s.editTags(arn, func(t core.Tags) core.Tags {
		out := core.Tags{}
		for k, v := range t {
			out[k] = v
		}
		for k, v := range in {
			out[k] = v
		}
		return out
	})
	return nil, err
}

func (s *Service) awsRemoveTags(q *awsapi.Req) (any, error) {
	arn := q.Param("ResourceName")
	if err := q.Authorize("rds:RemoveTagsFromResource", arn); err != nil {
		return nil, err
	}
	keys := list(q, "TagKeys", "member")
	_, err := s.editTags(arn, func(t core.Tags) core.Tags {
		out := core.Tags{}
		for k, v := range t {
			if !slices.Contains(keys, k) {
				out[k] = v
			}
		}
		return out
	})
	return nil, err
}

func (s *Service) awsDescribeLogFiles(q *awsapi.Req) (any, error) {
	id := strings.ToLower(q.Param("DBInstanceIdentifier"))
	if err := q.Authorize("rds:DescribeDBLogFiles", s.dbARN(id)); err != nil {
		return nil, err
	}
	if _, err := s.awsInstance(id); err != nil {
		return nil, err
	}
	return map[string]any{"DescribeDBLogFiles": named("DescribeDBLogFilesDetails")}, nil
}

func (s *Service) awsPendingMaintenance(q *awsapi.Req) (any, error) {
	if err := q.Authorize("rds:DescribePendingMaintenanceActions", "*"); err != nil {
		return nil, err
	}
	return map[string]any{"PendingMaintenanceActions": named("ResourcePendingMaintenanceActions")}, nil
}
