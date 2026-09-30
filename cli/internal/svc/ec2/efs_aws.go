package ec2

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi"
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/store"
)

// The AWS EFS API (restJson1, signing name "elasticfilesystem", 2015-02-01):
// file systems, mount targets, access points, tags, and stored-only
// lifecycle, backup and file system policy settings.

// RegisterEFSAWS serves EFS over the AWS protocol.
func (s *Service) RegisterEFSAWS() {
	awsapi.Register(&awsapi.Service{
		Name:      "elasticfilesystem",
		REST:      s.serveEFS,
		RESTError: efsRESTError,
		ErrorCode: map[string]string{
			"ResourceNotFound": "FileSystemNotFound",
			"ValidationError":  "BadRequest",
			"BadRequest":       "BadRequest",
			"Conflict":         "IncorrectFileSystemLifeCycleState",
			"ResourceConflict": "IncorrectFileSystemLifeCycleState",
			"AccessDenied":     "AccessDeniedException",
			"InternalError":    "InternalServerError",
		},
	})
}

// efsRESTError writes EFS's error shape: {"ErrorCode": ..., "Message": ...}.
func efsRESTError(q *awsapi.Req, e *awsapi.Error) {
	q.W.Header().Set("X-Amzn-ErrorType", e.Code)
	q.W.Header().Set("Content-Type", "application/json")
	q.W.WriteHeader(e.Status)
	body := map[string]any{"ErrorCode": e.Code, "Message": e.Message}
	for k, v := range e.Fields {
		body[k] = v
	}
	_ = json.NewEncoder(q.W).Encode(body)
}

type efsHandler func(q *awsapi.Req, p map[string]string) (status int, out any, err error)

type efsRoute struct {
	method, op string
	path       []string
	h          efsHandler
}

func (s *Service) efsAWSRoutes() []efsRoute {
	const v = "/2015-02-01"
	r := func(method, pattern, op string, h efsHandler) efsRoute {
		return efsRoute{method: method, op: op, path: strings.Split(strings.Trim(v+pattern, "/"), "/"), h: h}
	}
	return []efsRoute{
		r("POST", "/file-systems", "CreateFileSystem", s.efsCreateFS),
		r("GET", "/file-systems", "DescribeFileSystems", s.efsDescribeFS),
		r("PUT", "/file-systems/{fs}", "UpdateFileSystem", s.efsUpdateFS),
		r("DELETE", "/file-systems/{fs}", "DeleteFileSystem", s.efsDeleteFS),
		r("GET", "/file-systems/{fs}/lifecycle-configuration", "DescribeLifecycleConfiguration", s.efsGetLifecycle),
		r("PUT", "/file-systems/{fs}/lifecycle-configuration", "PutLifecycleConfiguration", s.efsPutLifecycle),
		r("GET", "/file-systems/{fs}/backup-policy", "DescribeBackupPolicy", s.efsGetBackup),
		r("PUT", "/file-systems/{fs}/backup-policy", "PutBackupPolicy", s.efsPutBackup),
		r("GET", "/file-systems/{fs}/policy", "DescribeFileSystemPolicy", s.efsGetPolicy),
		r("PUT", "/file-systems/{fs}/policy", "PutFileSystemPolicy", s.efsPutPolicy),
		r("DELETE", "/file-systems/{fs}/policy", "DeleteFileSystemPolicy", s.efsDeletePolicy),
		r("GET", "/file-systems/replication-configurations", "DescribeReplicationConfigurations", s.efsReplications),
		r("POST", "/mount-targets", "CreateMountTarget", s.efsCreateMT),
		r("GET", "/mount-targets", "DescribeMountTargets", s.efsDescribeMT),
		r("DELETE", "/mount-targets/{mt}", "DeleteMountTarget", s.efsDeleteMT),
		r("GET", "/mount-targets/{mt}/security-groups", "DescribeMountTargetSecurityGroups", s.efsGetMTGroups),
		r("PUT", "/mount-targets/{mt}/security-groups", "ModifyMountTargetSecurityGroups", s.efsPutMTGroups),
		r("POST", "/access-points", "CreateAccessPoint", s.efsCreateAP),
		r("GET", "/access-points", "DescribeAccessPoints", s.efsDescribeAP),
		r("DELETE", "/access-points/{ap}", "DeleteAccessPoint", s.efsDeleteAP),
		r("POST", "/resource-tags/{id}", "TagResource", s.efsTag),
		r("DELETE", "/resource-tags/{id}", "UntagResource", s.efsUntag),
		r("GET", "/resource-tags/{id}", "ListTagsForResource", s.efsListTags),
		r("POST", "/create-tags/{fs}", "CreateTags", s.efsCreateTags),
		r("POST", "/delete-tags/{fs}", "DeleteTags", s.efsDeleteTags),
		r("GET", "/tags/{fs}", "DescribeTags", s.efsDescribeTags),
		r("GET", "/account-preferences", "DescribeAccountPreferences", s.efsAccountPrefs),
	}
}

func efsMatch(pattern, segs []string) (map[string]string, bool) {
	// The literal "replication-configurations" wins over the {fs} segment.
	if len(pattern) != len(segs) {
		return nil, false
	}
	p := map[string]string{}
	for i, t := range pattern {
		if strings.HasPrefix(t, "{") {
			if segs[i] == "" {
				return nil, false
			}
			p[t[1:len(t)-1]] = segs[i]
			continue
		}
		if t != segs[i] {
			return nil, false
		}
	}
	return p, true
}

func (s *Service) serveEFS(q *awsapi.Req) {
	segs := strings.Split(strings.Trim(q.R.URL.Path, "/"), "/")
	methodOK := false
	for _, rt := range s.efsAWSRoutes() {
		p, ok := efsMatch(rt.path, segs)
		if !ok {
			continue
		}
		methodOK = true
		if rt.method != q.R.Method {
			continue
		}
		q.Op = rt.op
		status, out, err := rt.h(q, p)
		if err != nil {
			q.Fail(err)
			return
		}
		if out == nil {
			q.W.WriteHeader(status)
			return
		}
		q.WriteJSON(status, out)
		return
	}
	if methodOK {
		q.Fail(awsapi.Errorf(http.StatusMethodNotAllowed, "MethodNotAllowed", "method %s is not allowed on %s", q.R.Method, q.R.URL.Path))
		return
	}
	q.Fail(awsapi.Errorf(http.StatusNotFound, "UnknownOperationException", "HomeCloud does not implement the EFS operation %s %s yet", q.R.Method, q.R.URL.Path))
}

// ---- helpers ----

func (s *Service) fsARN(id string) string { return s.env.ARN("elasticfilesystem", "file-system/"+id) }
func (s *Service) apARN(id string) string { return s.env.ARN("elasticfilesystem", "access-point/"+id) }

// resourceARN is the ARN of a file system or access point ID.
func (s *Service) resourceARN(id string) string {
	if strings.HasPrefix(id, "fsap-") {
		return s.apARN(id)
	}
	return s.fsARN(id)
}

func efsBind(q *awsapi.Req, v any) error {
	if len(strings.TrimSpace(string(q.Body))) == 0 {
		return nil
	}
	if err := json.Unmarshal(q.Body, v); err != nil {
		return awsapi.Errorf(http.StatusBadRequest, "BadRequest", "could not parse request body: %v", err)
	}
	return nil
}

type efsTagIn struct{ Key, Value string }

func efsTags(in []efsTagIn) core.Tags {
	if len(in) == 0 {
		return nil
	}
	t := core.Tags{}
	for _, x := range in {
		t[x.Key] = x.Value
	}
	return t
}

func efsTagList(t core.Tags) []map[string]string {
	keys := make([]string, 0, len(t))
	for k := range t {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := []map[string]string{}
	for _, k := range keys {
		out = append(out, map[string]string{"Key": k, "Value": t[k]})
	}
	return out
}

func efsPage(q *awsapi.Req, n int) (start, end int, next string) {
	start, _ = strconv.Atoi(q.R.URL.Query().Get("Marker"))
	limit, err := strconv.Atoi(q.R.URL.Query().Get("MaxItems"))
	if err != nil || limit <= 0 || limit > 100 {
		limit = 100
	}
	start = min(max(start, 0), n)
	end = min(start+limit, n)
	if end < n {
		next = strconv.Itoa(end)
	}
	return
}

func efsPaged(out map[string]any, q *awsapi.Req, next string) map[string]any {
	if m := q.R.URL.Query().Get("Marker"); m != "" {
		out["Marker"] = m
	}
	if next != "" {
		out["NextMarker"] = next
	}
	return out
}

func (s *Service) efsFSDesc(fs FileSystem) map[string]any {
	perf, thr := fs.PerformanceMode, fs.ThroughputMode
	if perf == "" {
		perf = "generalPurpose"
	}
	if thr == "" {
		thr = "bursting"
	}
	m := map[string]any{
		"OwnerId": s.env.AccountID, "CreationToken": fs.CreationToken, "FileSystemId": fs.ID, "FileSystemArn": fs.ARN,
		"CreationTime": awsapi.Epoch(fs.CreatedAt), "LifeCycleState": fs.State, "Name": fs.Name,
		"NumberOfMountTargets": len(s.mountTargets(fs.ID)),
		"SizeInBytes":          map[string]any{"Value": fs.SizeBytes, "Timestamp": awsapi.Epoch(orNow(fs)), "ValueInIA": 0, "ValueInStandard": fs.SizeBytes},
		"PerformanceMode":      perf, "ThroughputMode": thr, "Encrypted": fs.Encrypted,
		"Tags":                 efsTagList(fs.Tags),
		"FileSystemProtection": map[string]any{"ReplicationOverwriteProtection": "ENABLED"},
	}
	if fs.KmsKeyID != "" {
		m["KmsKeyId"] = fs.KmsKeyID
	}
	if thr == "provisioned" {
		m["ProvisionedThroughputInMibps"] = fs.Provisioned
	}
	if fs.AZName != "" {
		m["AvailabilityZoneName"], m["AvailabilityZoneId"] = fs.AZName, azID(fs.AZName)
	}
	return m
}

func orNow(fs FileSystem) time.Time {
	if fs.SizeUpdated.IsZero() {
		return fs.CreatedAt
	}
	return fs.SizeUpdated
}

func (s *Service) efsMTDesc(m MountTarget) map[string]any {
	return map[string]any{"OwnerId": s.env.AccountID, "MountTargetId": m.ID, "FileSystemId": m.FileSystemID, "SubnetId": m.SubnetID,
		"LifeCycleState": "available", "IpAddress": m.IP, "NetworkInterfaceId": m.ENI, "AvailabilityZoneId": azID(m.AZ),
		"AvailabilityZoneName": m.AZ, "VpcId": m.VpcID}
}

func (s *Service) efsAPDesc(a AccessPoint) map[string]any {
	root := map[string]any{"Path": a.RootPath}
	if a.CreationInfo != nil {
		root["CreationInfo"] = map[string]any{"OwnerUid": a.CreationInfo.OwnerUid, "OwnerGid": a.CreationInfo.OwnerGid, "Permissions": a.CreationInfo.Permissions}
	}
	m := map[string]any{"ClientToken": a.ClientToken, "AccessPointId": a.ID, "AccessPointArn": a.ARN, "FileSystemId": a.FileSystemID,
		"LifeCycleState": "available", "OwnerId": s.env.AccountID, "RootDirectory": root, "Tags": efsTagList(a.Tags)}
	if a.Name != "" {
		m["Name"] = a.Name
	}
	if a.PosixUser != nil {
		u := map[string]any{"Uid": a.PosixUser.Uid, "Gid": a.PosixUser.Gid}
		if len(a.PosixUser.SecondaryGids) > 0 {
			u["SecondaryGids"] = a.PosixUser.SecondaryGids
		}
		m["PosixUser"] = u
	}
	return m
}

func efsAPIErr(status int, code, format string, a ...any) error {
	return awsapi.Errorf(status, code, format, a...)
}

// ---- file systems ----

func (s *Service) efsCreateFS(q *awsapi.Req, p map[string]string) (int, any, error) {
	if err := q.Authorize("elasticfilesystem:CreateFileSystem", s.fsARN("*")); err != nil {
		return 0, nil, err
	}
	var in struct {
		CreationToken                string
		PerformanceMode              string
		Encrypted                    bool
		KmsKeyId                     string
		ThroughputMode               string
		ProvisionedThroughputInMibps float64
		AvailabilityZoneName         string
		Backup                       bool
		Tags                         []efsTagIn
	}
	if err := efsBind(q, &in); err != nil {
		return 0, nil, err
	}
	if in.CreationToken == "" {
		return 0, nil, efsAPIErr(http.StatusBadRequest, "BadRequest", "CreationToken is required")
	}
	tags := efsTags(in.Tags)
	if len(tags) > 0 {
		if err := q.Check("elasticfilesystem:TagResource", s.fsARN("*")); err != nil {
			return 0, nil, err
		}
	}
	if in.PerformanceMode != "" && in.PerformanceMode != "generalPurpose" && in.PerformanceMode != "maxIO" {
		return 0, nil, efsAPIErr(http.StatusBadRequest, "BadRequest", "PerformanceMode must be generalPurpose or maxIO")
	}
	if err := checkThroughput(in.ThroughputMode, in.ProvisionedThroughputInMibps); err != nil {
		return 0, nil, err
	}
	if in.AvailabilityZoneName != "" && !s.knownAZ(in.AvailabilityZoneName) {
		return 0, nil, efsAPIErr(http.StatusBadRequest, "BadRequest", "The availability zone %s does not exist", in.AvailabilityZoneName)
	}
	fs, created, err := s.createFileSystem(fsInput{Name: tags["Name"], Tags: tags, Token: in.CreationToken, Performance: in.PerformanceMode,
		Throughput: in.ThroughputMode, Provisioned: in.ProvisionedThroughputInMibps, Encrypted: in.Encrypted || in.KmsKeyId != "", KmsKeyID: in.KmsKeyId,
		AZName: in.AvailabilityZoneName, BackupEnabled: in.Backup || in.AvailabilityZoneName != "", skipNameCheck: true})
	if err != nil {
		return 0, nil, err
	}
	if !created {
		e := awsapi.Errorf(http.StatusConflict, "FileSystemAlreadyExists", "File system '%s' already exists with the creation token you provided.", fs.ID)
		e.Fields = map[string]any{"FileSystemId": fs.ID}
		return 0, nil, e
	}
	return http.StatusCreated, s.efsFSDesc(fs), nil
}

func (s *Service) knownAZ(name string) bool {
	for _, sn := range s.vpc.Subnets() {
		if sn.AvailabilityZone == name {
			return true
		}
	}
	return strings.HasPrefix(name, core.Region) && len(name) == len(core.Region)+1
}

func checkThroughput(mode string, provisioned float64) error {
	switch mode {
	case "", "bursting", "elastic":
	case "provisioned":
		if provisioned < 1 {
			return efsAPIErr(http.StatusBadRequest, "BadRequest", "ProvisionedThroughputInMibps is required (at least 1) when ThroughputMode is provisioned")
		}
	default:
		return efsAPIErr(http.StatusBadRequest, "BadRequest", "ThroughputMode must be bursting, provisioned or elastic")
	}
	return nil
}

func (s *Service) efsDescribeFS(q *awsapi.Req, p map[string]string) (int, any, error) {
	qs := q.R.URL.Query()
	id, token := qs.Get("FileSystemId"), qs.Get("CreationToken")
	res := "*"
	if id != "" {
		res = s.fsARN(id)
	}
	if err := q.Authorize("elasticfilesystem:DescribeFileSystems", res); err != nil {
		return 0, nil, err
	}
	all := store.List[FileSystem](s.env.Store, cFileSystems)
	sort.Slice(all, func(i, j int) bool {
		return all[i].CreatedAt.Before(all[j].CreatedAt) || (all[i].CreatedAt.Equal(all[j].CreatedAt) && all[i].ID < all[j].ID)
	})
	var match []FileSystem
	for _, fs := range all {
		if (id == "" || fs.ID == id) && (token == "" || fs.CreationToken == token) {
			match = append(match, fs)
		}
	}
	if id != "" && len(match) == 0 {
		return 0, nil, fsNotFound(id)
	}
	start, end, next := efsPage(q, len(match))
	out := []map[string]any{}
	for _, fs := range match[start:end] {
		out = append(out, s.efsFSDesc(fs))
	}
	return http.StatusOK, efsPaged(map[string]any{"FileSystems": out}, q, next), nil
}

func (s *Service) efsUpdateFS(q *awsapi.Req, p map[string]string) (int, any, error) {
	id := p["fs"]
	if err := q.Authorize("elasticfilesystem:UpdateFileSystem", s.fsARN(id)); err != nil {
		return 0, nil, err
	}
	var in struct {
		ThroughputMode               string
		ProvisionedThroughputInMibps float64
	}
	if err := efsBind(q, &in); err != nil {
		return 0, nil, err
	}
	fs, err := s.file(id)
	if err != nil {
		return 0, nil, err
	}
	if in.ThroughputMode == "" && in.ProvisionedThroughputInMibps == 0 {
		return 0, nil, efsAPIErr(http.StatusBadRequest, "BadRequest", "ThroughputMode or ProvisionedThroughputInMibps is required")
	}
	mode := in.ThroughputMode
	if mode == "" {
		mode = fs.ThroughputMode
	}
	prov := in.ProvisionedThroughputInMibps
	if prov == 0 && mode == "provisioned" {
		prov = fs.Provisioned
	}
	if err := checkThroughput(mode, prov); err != nil {
		return 0, nil, err
	}
	fs, err = s.editFS(id, func(x *FileSystem) error {
		x.ThroughputMode, x.Provisioned = mode, 0
		if mode == "provisioned" {
			x.Provisioned = prov
		}
		return nil
	})
	if err != nil {
		return 0, nil, err
	}
	return http.StatusAccepted, s.efsFSDesc(fs), nil
}

func (s *Service) efsDeleteFS(q *awsapi.Req, p map[string]string) (int, any, error) {
	id := p["fs"]
	if err := q.Authorize("elasticfilesystem:DeleteFileSystem", s.fsARN(id)); err != nil {
		return 0, nil, err
	}
	return http.StatusNoContent, nil, s.deleteFileSystem(id)
}

// Lifecycle management, backup and file system policies are stored and
// reported; HomeCloud does not tier files, back them up or evaluate the policy.

func (s *Service) efsFS(q *awsapi.Req, action, id string) (FileSystem, error) {
	if err := q.Authorize(action, s.fsARN(id)); err != nil {
		return FileSystem{}, err
	}
	return s.file(id)
}

func (s *Service) efsGetLifecycle(q *awsapi.Req, p map[string]string) (int, any, error) {
	fs, err := s.efsFS(q, "elasticfilesystem:DescribeLifecycleConfiguration", p["fs"])
	if err != nil {
		return 0, nil, err
	}
	pol := fs.Lifecycle
	if pol == nil {
		pol = []map[string]string{}
	}
	return http.StatusOK, map[string]any{"LifecyclePolicies": pol}, nil
}

func (s *Service) efsPutLifecycle(q *awsapi.Req, p map[string]string) (int, any, error) {
	if _, err := s.efsFS(q, "elasticfilesystem:PutLifecycleConfiguration", p["fs"]); err != nil {
		return 0, nil, err
	}
	var in struct{ LifecyclePolicies []map[string]string }
	if err := efsBind(q, &in); err != nil {
		return 0, nil, err
	}
	for _, pol := range in.LifecyclePolicies {
		for k := range pol {
			if k != "TransitionToIA" && k != "TransitionToPrimaryStorageClass" && k != "TransitionToArchive" {
				return 0, nil, efsAPIErr(http.StatusBadRequest, "BadRequest", "unknown lifecycle policy %s", k)
			}
		}
	}
	pol := in.LifecyclePolicies
	if pol == nil {
		pol = []map[string]string{}
	}
	if _, err := s.editFS(p["fs"], func(x *FileSystem) error { x.Lifecycle = pol; return nil }); err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]any{"LifecyclePolicies": pol}, nil
}

func (s *Service) efsGetBackup(q *awsapi.Req, p map[string]string) (int, any, error) {
	fs, err := s.efsFS(q, "elasticfilesystem:DescribeBackupPolicy", p["fs"])
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]any{"BackupPolicy": map[string]string{"Status": backupStatus(fs.Backup)}}, nil
}

func backupStatus(on bool) string {
	if on {
		return "ENABLED"
	}
	return "DISABLED"
}

func (s *Service) efsPutBackup(q *awsapi.Req, p map[string]string) (int, any, error) {
	if _, err := s.efsFS(q, "elasticfilesystem:PutBackupPolicy", p["fs"]); err != nil {
		return 0, nil, err
	}
	var in struct{ BackupPolicy struct{ Status string } }
	if err := efsBind(q, &in); err != nil {
		return 0, nil, err
	}
	switch in.BackupPolicy.Status {
	case "ENABLED", "DISABLED":
	default:
		return 0, nil, efsAPIErr(http.StatusBadRequest, "BadRequest", "BackupPolicy.Status must be ENABLED or DISABLED")
	}
	if _, err := s.editFS(p["fs"], func(x *FileSystem) error { x.Backup = in.BackupPolicy.Status == "ENABLED"; return nil }); err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]any{"BackupPolicy": map[string]string{"Status": in.BackupPolicy.Status}}, nil
}

func (s *Service) efsGetPolicy(q *awsapi.Req, p map[string]string) (int, any, error) {
	fs, err := s.efsFS(q, "elasticfilesystem:DescribeFileSystemPolicy", p["fs"])
	if err != nil {
		return 0, nil, err
	}
	if fs.Policy == "" {
		return 0, nil, efsAPIErr(http.StatusNotFound, "PolicyNotFound", "File system %s has no resource policy.", fs.ID)
	}
	return http.StatusOK, map[string]any{"FileSystemId": fs.ID, "Policy": fs.Policy}, nil
}

func (s *Service) efsPutPolicy(q *awsapi.Req, p map[string]string) (int, any, error) {
	if _, err := s.efsFS(q, "elasticfilesystem:PutFileSystemPolicy", p["fs"]); err != nil {
		return 0, nil, err
	}
	var in struct{ Policy string }
	if err := efsBind(q, &in); err != nil {
		return 0, nil, err
	}
	var doc map[string]any
	if json.Unmarshal([]byte(in.Policy), &doc) != nil || doc == nil {
		return 0, nil, efsAPIErr(http.StatusBadRequest, "InvalidPolicyException", "Policy must be a JSON policy document")
	}
	if _, err := s.editFS(p["fs"], func(x *FileSystem) error { x.Policy = in.Policy; return nil }); err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]any{"FileSystemId": p["fs"], "Policy": in.Policy}, nil
}

func (s *Service) efsDeletePolicy(q *awsapi.Req, p map[string]string) (int, any, error) {
	if _, err := s.efsFS(q, "elasticfilesystem:DeleteFileSystemPolicy", p["fs"]); err != nil {
		return 0, nil, err
	}
	_, err := s.editFS(p["fs"], func(x *FileSystem) error { x.Policy = ""; return nil })
	return http.StatusOK, map[string]any{}, err
}

func (s *Service) efsReplications(q *awsapi.Req, p map[string]string) (int, any, error) {
	id := p["fs"]
	if id == "" {
		id = q.R.URL.Query().Get("FileSystemId")
	}
	res := "*"
	if id != "" {
		res = s.fsARN(id)
	}
	if err := q.Authorize("elasticfilesystem:DescribeReplicationConfigurations", res); err != nil {
		return 0, nil, err
	}
	if id != "" {
		if _, err := s.file(id); err != nil {
			return 0, nil, err
		}
		return 0, nil, efsAPIErr(http.StatusNotFound, "ReplicationNotFound", "File system %s has no replication configuration.", id)
	}
	return http.StatusOK, map[string]any{"Replications": []any{}}, nil
}

func (s *Service) efsAccountPrefs(q *awsapi.Req, p map[string]string) (int, any, error) {
	if err := q.Authorize("elasticfilesystem:DescribeAccountPreferences", "*"); err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]any{"ResourceIdPreference": map[string]any{"ResourceIdType": "LONG_ID", "Resources": []string{"FILE_SYSTEM", "MOUNT_TARGET"}}}, nil
}

// ---- mount targets ----

func (s *Service) efsCreateMT(q *awsapi.Req, p map[string]string) (int, any, error) {
	var in struct {
		FileSystemId   string
		SubnetId       string
		IpAddress      string
		SecurityGroups []string
	}
	if err := efsBind(q, &in); err != nil {
		return 0, nil, err
	}
	if err := q.Authorize("elasticfilesystem:CreateMountTarget", s.fsARN(in.FileSystemId)); err != nil {
		return 0, nil, err
	}
	if in.FileSystemId == "" || in.SubnetId == "" {
		return 0, nil, efsAPIErr(http.StatusBadRequest, "BadRequest", "FileSystemId and SubnetId are required")
	}
	mt, err := s.createMountTarget(in.FileSystemId, mtInput{SubnetID: in.SubnetId, IPAddress: in.IpAddress, SecurityGroups: in.SecurityGroups})
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, s.efsMTDesc(mt), nil
}

func (s *Service) efsDescribeMT(q *awsapi.Req, p map[string]string) (int, any, error) {
	qs := q.R.URL.Query()
	fsID, mtID, apID := qs.Get("FileSystemId"), qs.Get("MountTargetId"), qs.Get("AccessPointId")
	if fsID == "" && mtID == "" && apID == "" {
		return 0, nil, efsAPIErr(http.StatusBadRequest, "BadRequest", "You must specify one of FileSystemId, MountTargetId or AccessPointId")
	}
	var mts []MountTarget
	switch {
	case mtID != "":
		mt, err := s.mountTarget(mtID)
		if err := s.authorizeMTRead(q, mt.FileSystemID, err); err != nil {
			return 0, nil, err
		}
		if err != nil {
			return 0, nil, err
		}
		mts = []MountTarget{mt}
	case apID != "":
		ap, err := s.accessPoint(apID)
		if err := s.authorizeMTRead(q, ap.FileSystemID, err); err != nil {
			return 0, nil, err
		}
		if err != nil {
			return 0, nil, err
		}
		mts = s.mountTargets(ap.FileSystemID)
	default:
		if err := q.Authorize("elasticfilesystem:DescribeMountTargets", s.fsARN(fsID)); err != nil {
			return 0, nil, err
		}
		if _, err := s.file(fsID); err != nil {
			return 0, nil, err
		}
		mts = s.mountTargets(fsID)
	}
	start, end, next := efsPage(q, len(mts))
	out := []map[string]any{}
	for _, m := range mts[start:end] {
		out = append(out, s.efsMTDesc(m))
	}
	return http.StatusOK, efsPaged(map[string]any{"MountTargets": out}, q, next), nil
}

// authorizeMTRead authorizes a read of the mount targets of a file system that
// was looked up by a child ID; when the lookup failed it authorizes "*" so a
// caller without any access learns nothing about what exists.
func (s *Service) authorizeMTRead(q *awsapi.Req, fsID string, lookup error) error {
	res := "*"
	if lookup == nil {
		res = s.fsARN(fsID)
	}
	return q.Authorize("elasticfilesystem:DescribeMountTargets", res)
}

func (s *Service) efsDeleteMT(q *awsapi.Req, p map[string]string) (int, any, error) {
	mt, err := s.mountTarget(p["mt"])
	res := "*"
	if err == nil {
		res = s.fsARN(mt.FileSystemID)
	}
	if aerr := q.Authorize("elasticfilesystem:DeleteMountTarget", res); aerr != nil {
		return 0, nil, aerr
	}
	if err != nil {
		return 0, nil, err
	}
	return http.StatusNoContent, nil, s.deleteMountTarget(mt.ID)
}

func (s *Service) efsGetMTGroups(q *awsapi.Req, p map[string]string) (int, any, error) {
	mt, err := s.mountTarget(p["mt"])
	res := "*"
	if err == nil {
		res = s.fsARN(mt.FileSystemID)
	}
	if aerr := q.Authorize("elasticfilesystem:DescribeMountTargetSecurityGroups", res); aerr != nil {
		return 0, nil, aerr
	}
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]any{"SecurityGroups": mt.SecurityGroups}, nil
}

func (s *Service) efsPutMTGroups(q *awsapi.Req, p map[string]string) (int, any, error) {
	mt, err := s.mountTarget(p["mt"])
	res := "*"
	if err == nil {
		res = s.fsARN(mt.FileSystemID)
	}
	if aerr := q.Authorize("elasticfilesystem:ModifyMountTargetSecurityGroups", res); aerr != nil {
		return 0, nil, aerr
	}
	if err != nil {
		return 0, nil, err
	}
	var in struct{ SecurityGroups []string }
	if err := efsBind(q, &in); err != nil {
		return 0, nil, err
	}
	return http.StatusNoContent, nil, s.setMountTargetGroups(mt.ID, in.SecurityGroups)
}

// ---- access points ----

func (s *Service) efsCreateAP(q *awsapi.Req, p map[string]string) (int, any, error) {
	var in struct {
		ClientToken  string
		FileSystemId string
		Tags         []efsTagIn
		PosixUser    *struct {
			Uid           int64
			Gid           int64
			SecondaryGids []int64
		}
		RootDirectory *struct {
			Path         string
			CreationInfo *struct {
				OwnerUid    int64
				OwnerGid    int64
				Permissions string
			}
		}
	}
	if err := efsBind(q, &in); err != nil {
		return 0, nil, err
	}
	if err := q.Authorize("elasticfilesystem:CreateAccessPoint", s.fsARN(in.FileSystemId)); err != nil {
		return 0, nil, err
	}
	if in.FileSystemId == "" {
		return 0, nil, efsAPIErr(http.StatusBadRequest, "BadRequest", "FileSystemId is required")
	}
	tags := efsTags(in.Tags)
	if len(tags) > 0 {
		if err := q.Check("elasticfilesystem:TagResource", s.apARN("*")); err != nil {
			return 0, nil, err
		}
	}
	ai := apInput{ClientToken: in.ClientToken, Tags: tags}
	if in.PosixUser != nil {
		ai.PosixUser = &PosixUser{Uid: in.PosixUser.Uid, Gid: in.PosixUser.Gid, SecondaryGids: in.PosixUser.SecondaryGids}
	}
	if in.RootDirectory != nil {
		ai.RootPath = in.RootDirectory.Path
		if ci := in.RootDirectory.CreationInfo; ci != nil {
			ai.CreationInfo = &CreationInfo{OwnerUid: ci.OwnerUid, OwnerGid: ci.OwnerGid, Permissions: ci.Permissions}
		}
	}
	ap, created, err := s.createAccessPoint(in.FileSystemId, ai)
	if err != nil {
		return 0, nil, err
	}
	if !created {
		e := awsapi.Errorf(http.StatusConflict, "AccessPointAlreadyExists", "Access point '%s' already exists with the client token you provided.", ap.ID)
		e.Fields = map[string]any{"AccessPointId": ap.ID}
		return 0, nil, e
	}
	return http.StatusOK, s.efsAPDesc(ap), nil
}

func (s *Service) efsDescribeAP(q *awsapi.Req, p map[string]string) (int, any, error) {
	qs := q.R.URL.Query()
	apID, fsID := qs.Get("AccessPointId"), qs.Get("FileSystemId")
	res := "*"
	switch {
	case apID != "":
		res = s.apARN(apID)
	case fsID != "":
		res = s.fsARN(fsID)
	}
	if err := q.Authorize("elasticfilesystem:DescribeAccessPoints", res); err != nil {
		return 0, nil, err
	}
	var aps []AccessPoint
	switch {
	case apID != "":
		ap, err := s.accessPoint(apID)
		if err != nil {
			return 0, nil, err
		}
		aps = []AccessPoint{ap}
	case fsID != "":
		if _, err := s.file(fsID); err != nil {
			return 0, nil, err
		}
		aps = s.accessPoints(fsID)
	default:
		aps = s.accessPoints("")
	}
	start, end, next := efsPage(q, len(aps))
	out := []map[string]any{}
	for _, a := range aps[start:end] {
		out = append(out, s.efsAPDesc(a))
	}
	res2 := map[string]any{"AccessPoints": out}
	if next != "" {
		res2["NextToken"] = next
	}
	return http.StatusOK, res2, nil
}

func (s *Service) efsDeleteAP(q *awsapi.Req, p map[string]string) (int, any, error) {
	if err := q.Authorize("elasticfilesystem:DeleteAccessPoint", s.apARN(p["ap"])); err != nil {
		return 0, nil, err
	}
	return http.StatusNoContent, nil, s.deleteAccessPoint(p["ap"])
}

// ---- tags ----

func (s *Service) efsTag(q *awsapi.Req, p map[string]string) (int, any, error) {
	id := p["id"]
	if err := q.Authorize("elasticfilesystem:TagResource", s.resourceARN(id)); err != nil {
		return 0, nil, err
	}
	var in struct{ Tags []efsTagIn }
	if err := efsBind(q, &in); err != nil {
		return 0, nil, err
	}
	return s.efsAddTags(id, in.Tags)
}

func (s *Service) efsAddTags(id string, add []efsTagIn) (int, any, error) {
	_, err := s.editEFSTags(id, func(t core.Tags) core.Tags {
		out := core.Tags{}
		for k, v := range t {
			out[k] = v
		}
		for _, x := range add {
			out[x.Key] = x.Value
		}
		return out
	})
	return http.StatusOK, nil, err
}

func (s *Service) efsUntag(q *awsapi.Req, p map[string]string) (int, any, error) {
	id := p["id"]
	if err := q.Authorize("elasticfilesystem:UntagResource", s.resourceARN(id)); err != nil {
		return 0, nil, err
	}
	return s.efsRemoveTags(id, q.R.URL.Query()["tagKeys"])
}

func (s *Service) efsRemoveTags(id string, keys []string) (int, any, error) {
	_, err := s.editEFSTags(id, func(t core.Tags) core.Tags {
		out := core.Tags{}
		for k, v := range t {
			if !contains(keys, k) {
				out[k] = v
			}
		}
		return out
	})
	return http.StatusOK, nil, err
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func (s *Service) efsListTags(q *awsapi.Req, p map[string]string) (int, any, error) {
	id := p["id"]
	if err := q.Authorize("elasticfilesystem:ListTagsForResource", s.resourceARN(id)); err != nil {
		return 0, nil, err
	}
	t, err := s.editEFSTags(id, func(t core.Tags) core.Tags { return t })
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]any{"Tags": efsTagList(t)}, nil
}

// The legacy tag calls (file systems only).

func (s *Service) efsCreateTags(q *awsapi.Req, p map[string]string) (int, any, error) {
	if err := q.Authorize("elasticfilesystem:CreateTags", s.fsARN(p["fs"])); err != nil {
		return 0, nil, err
	}
	var in struct{ Tags []efsTagIn }
	if err := efsBind(q, &in); err != nil {
		return 0, nil, err
	}
	st, out, err := s.efsAddTags(p["fs"], in.Tags)
	if st == http.StatusOK {
		st = http.StatusNoContent
	}
	return st, out, err
}

func (s *Service) efsDeleteTags(q *awsapi.Req, p map[string]string) (int, any, error) {
	if err := q.Authorize("elasticfilesystem:DeleteTags", s.fsARN(p["fs"])); err != nil {
		return 0, nil, err
	}
	var in struct{ TagKeys []string }
	if err := efsBind(q, &in); err != nil {
		return 0, nil, err
	}
	st, out, err := s.efsRemoveTags(p["fs"], in.TagKeys)
	if st == http.StatusOK {
		st = http.StatusNoContent
	}
	return st, out, err
}

func (s *Service) efsDescribeTags(q *awsapi.Req, p map[string]string) (int, any, error) {
	if err := q.Authorize("elasticfilesystem:DescribeTags", s.fsARN(p["fs"])); err != nil {
		return 0, nil, err
	}
	fs, err := s.file(p["fs"])
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]any{"Tags": efsTagList(fs.Tags)}, nil
}
