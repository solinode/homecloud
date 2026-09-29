package iam

import (
	"net/http"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi"
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/store"
	"golang.org/x/crypto/bcrypt"
)

// ---- users ----

func (s *Service) awsCreateUser(q *awsapi.Req) (any, error) {
	if err := required(q, "UserName"); err != nil {
		return nil, err
	}
	in := UserInput{Name: q.Param("UserName"), Path: q.Param("Path"), Tags: tagsParam(q), PermissionsBoundary: q.Param("PermissionsBoundary")}
	path, err := validPath(in.Path)
	if err != nil {
		return nil, err
	}
	keys := requestTagKeys(in.Tags)
	if in.PermissionsBoundary != "" {
		keys["iam:permissionsboundary"] = []string{core.CanonicalARN(in.PermissionsBoundary)}
	}
	if err := authorizeWith(q, "iam:CreateUser", s.userARN(in.Name, path), keys); err != nil {
		return nil, err
	}
	u, err := s.CreateUser(in, authzWith(q, keys))
	if err != nil {
		return nil, err
	}
	return map[string]any{"User": userXML(u, true)}, nil
}

func (s *Service) awsGetUser(q *awsapi.Req) (any, error) {
	name, err := s.callerUser(q)
	if err != nil {
		return nil, err
	}
	if err := s.authUser(q, "iam:GetUser", name); err != nil {
		return nil, err
	}
	u, err := s.getUserOr404(name)
	if err != nil {
		return nil, err
	}
	return map[string]any{"User": userXML(u, true)}, nil
}

func (s *Service) awsListUsers(q *awsapi.Req) (any, error) {
	if err := q.Authorize("iam:ListUsers", q.ARN("iam", "user/")); err != nil {
		return nil, err
	}
	var users []User
	for _, u := range store.List[User](s.env.Store, cUsers) {
		if pathPrefix(q, u.path()) {
			users = append(users, u)
		}
	}
	return page(q, users, func(u User) string { return u.Name }, "Users", func(u User) any { return userXML(u, false) })
}

func (s *Service) awsDeleteUser(q *awsapi.Req) (any, error) {
	if err := required(q, "UserName"); err != nil {
		return nil, err
	}
	name := q.Param("UserName")
	if err := s.authUser(q, "iam:DeleteUser", name); err != nil {
		return nil, err
	}
	return awsapi.NoResult{}, s.DeleteUser(name, false)
}

func (s *Service) awsUpdateUser(q *awsapi.Req) (any, error) {
	if err := required(q, "UserName"); err != nil {
		return nil, err
	}
	name, newName := q.Param("UserName"), q.Param("NewUserName")
	if err := s.authUser(q, "iam:UpdateUser", name); err != nil {
		return nil, err
	}
	if newName != "" && newName != name {
		// Renaming needs permission on the new name too, as in AWS.
		path := q.Param("NewPath")
		if u, err := s.getUserOr404(name); err == nil && path == "" {
			path = u.path()
		}
		if path == "" {
			path = "/"
		}
		if err := q.Authorize("iam:UpdateUser", s.userARN(newName, path)); err != nil {
			return nil, err
		}
	}
	_, err := s.RenameUser(name, newName, q.Param("NewPath"))
	return awsapi.NoResult{}, err
}

func (s *Service) awsTagUser(q *awsapi.Req) (any, error) {
	if err := required(q, "UserName"); err != nil {
		return nil, err
	}
	name, tags := q.Param("UserName"), tagsParam(q)
	if err := s.authUser(q, "iam:TagUser", name, requestTagKeys(tags)); err != nil {
		return nil, err
	}
	_, err := s.UpdateUser(name, func(u *User) (err error) { u.Tags, err = mergeTags(u.Tags, tags); return err })
	return awsapi.NoResult{}, err
}

func (s *Service) awsUntagUser(q *awsapi.Req) (any, error) {
	if err := required(q, "UserName"); err != nil {
		return nil, err
	}
	name, keys := q.Param("UserName"), q.List("TagKeys")
	if err := s.authUser(q, "iam:UntagUser", name, CondContext{"aws:tagkeys": keys}); err != nil {
		return nil, err
	}
	_, err := s.UpdateUser(name, func(u *User) error { u.Tags = dropTags(u.Tags, keys); return nil })
	return awsapi.NoResult{}, err
}

func (s *Service) awsListUserTags(q *awsapi.Req) (any, error) {
	if err := required(q, "UserName"); err != nil {
		return nil, err
	}
	if err := s.authUser(q, "iam:ListUserTags", q.Param("UserName")); err != nil {
		return nil, err
	}
	u, err := s.getUserOr404(q.Param("UserName"))
	if err != nil {
		return nil, err
	}
	return tagsResult(u.Tags), nil
}

func loginProfileXML(u User) map[string]any {
	created := u.CreatedAt
	if u.PasswordSetAt != nil {
		created = *u.PasswordSetAt
	}
	return map[string]any{"LoginProfile": map[string]any{"UserName": u.Name, "CreateDate": created, "PasswordResetRequired": u.PasswordResetRequired}}
}

func (s *Service) awsCreateLoginProfile(q *awsapi.Req) (any, error) {
	name, err := s.callerUser(q)
	if err != nil {
		return nil, err
	}
	if err := s.authUser(q, "iam:CreateLoginProfile", name); err != nil {
		return nil, err
	}
	if q.Param("Password") == "" {
		return nil, required(q, "Password")
	}
	reset := q.ParamBool("PasswordResetRequired", false)
	u, err := s.SetLoginProfile(name, q.Param("Password"), &reset, true, false)
	if err != nil {
		return nil, err
	}
	return loginProfileXML(u), nil
}

func (s *Service) awsGetLoginProfile(q *awsapi.Req) (any, error) {
	name, err := s.callerUser(q)
	if err != nil {
		return nil, err
	}
	if err := s.authUser(q, "iam:GetLoginProfile", name); err != nil {
		return nil, err
	}
	u, err := s.getUserOr404(name)
	if err != nil {
		return nil, err
	}
	if u.PasswordHash == "" {
		return nil, noSuchEntity("Login Profile for User %s cannot be found.", name)
	}
	return loginProfileXML(u), nil
}

func (s *Service) awsUpdateLoginProfile(q *awsapi.Req) (any, error) {
	if err := required(q, "UserName"); err != nil {
		return nil, err
	}
	name := q.Param("UserName")
	if err := s.authUser(q, "iam:UpdateLoginProfile", name); err != nil {
		return nil, err
	}
	var reset *bool
	if q.Param("PasswordResetRequired") != "" {
		v := q.ParamBool("PasswordResetRequired", false)
		reset = &v
	}
	_, err := s.SetLoginProfile(name, q.Param("Password"), reset, false, true)
	return awsapi.NoResult{}, err
}

func (s *Service) awsDeleteLoginProfile(q *awsapi.Req) (any, error) {
	name, err := s.callerUser(q)
	if err != nil {
		return nil, err
	}
	if err := s.authUser(q, "iam:DeleteLoginProfile", name); err != nil {
		return nil, err
	}
	_, err = s.DeleteLoginProfile(name, true)
	return awsapi.NoResult{}, err
}

// awsChangePassword lets users change their own console password.
func (s *Service) awsChangePassword(q *awsapi.Req) (any, error) {
	if err := required(q, "OldPassword", "NewPassword"); err != nil {
		return nil, err
	}
	name, err := s.callerUser(q)
	if err != nil {
		return nil, err
	}
	if q.Param("UserName") != "" && q.Param("UserName") != q.P.UserName {
		return nil, awsapi.Errorf(http.StatusBadRequest, "InvalidInput", "ChangePassword changes the caller's own password")
	}
	if err := s.authUser(q, "iam:ChangePassword", name); err != nil {
		return nil, err
	}
	u, err := s.getUserOr404(name)
	if err != nil {
		return nil, err
	}
	if u.PasswordHash == "" || bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(q.Param("OldPassword"))) != nil {
		return nil, awsapi.Errorf(http.StatusForbidden, "AccessDenied", "The old password is incorrect.")
	}
	reset := false
	_, err = s.SetLoginProfile(name, q.Param("NewPassword"), &reset, false, true)
	return awsapi.NoResult{}, err
}

func (s *Service) awsPutUserBoundary(q *awsapi.Req) (any, error) {
	if err := required(q, "UserName", "PermissionsBoundary"); err != nil {
		return nil, err
	}
	b := q.Param("PermissionsBoundary")
	if err := s.authUser(q, "iam:PutUserPermissionsBoundary", q.Param("UserName"), CondContext{"iam:permissionsboundary": {core.CanonicalARN(b)}}); err != nil {
		return nil, err
	}
	_, err := s.SetUserBoundary(q.Param("UserName"), b)
	return awsapi.NoResult{}, err
}

func (s *Service) awsDeleteUserBoundary(q *awsapi.Req) (any, error) {
	if err := required(q, "UserName"); err != nil {
		return nil, err
	}
	if err := s.authUser(q, "iam:DeleteUserPermissionsBoundary", q.Param("UserName")); err != nil {
		return nil, err
	}
	_, err := s.SetUserBoundary(q.Param("UserName"), "")
	return awsapi.NoResult{}, err
}

// ---- access keys ----

func (s *Service) awsCreateAccessKey(q *awsapi.Req) (any, error) {
	name, err := s.callerUser(q)
	if err != nil {
		return nil, err
	}
	if err := s.authUser(q, "iam:CreateAccessKey", name); err != nil {
		return nil, err
	}
	k, secret, err := s.CreateAccessKey(name)
	if err != nil {
		return nil, err
	}
	m := keyXML(k)
	m["SecretAccessKey"] = secret
	return map[string]any{"AccessKey": m}, nil
}

func (s *Service) awsListAccessKeys(q *awsapi.Req) (any, error) {
	name, err := s.callerUser(q)
	if err != nil {
		return nil, err
	}
	if err := s.authUser(q, "iam:ListAccessKeys", name); err != nil {
		return nil, err
	}
	if _, err := s.getUserOr404(name); err != nil {
		return nil, err
	}
	return page(q, s.UserKeys(name), func(k AccessKey) string { return k.AccessKeyID }, "AccessKeyMetadata", func(k AccessKey) any { return keyXML(k) })
}

func (s *Service) awsUpdateAccessKey(q *awsapi.Req) (any, error) {
	if err := required(q, "AccessKeyId", "Status"); err != nil {
		return nil, err
	}
	name, err := s.callerUser(q)
	if err != nil {
		return nil, err
	}
	if err := s.authUser(q, "iam:UpdateAccessKey", name); err != nil {
		return nil, err
	}
	_, err = s.UpdateAccessKey(name, q.Param("AccessKeyId"), q.Param("Status"))
	return awsapi.NoResult{}, err
}

func (s *Service) awsDeleteAccessKey(q *awsapi.Req) (any, error) {
	if err := required(q, "AccessKeyId"); err != nil {
		return nil, err
	}
	name, err := s.callerUser(q)
	if err != nil {
		return nil, err
	}
	if err := s.authUser(q, "iam:DeleteAccessKey", name); err != nil {
		return nil, err
	}
	return awsapi.NoResult{}, s.DeleteAccessKey(name, q.Param("AccessKeyId"))
}

func (s *Service) awsGetAccessKeyLastUsed(q *awsapi.Req) (any, error) {
	if err := required(q, "AccessKeyId"); err != nil {
		return nil, err
	}
	k, err := store.Get[AccessKey](s.env.Store, cKeys, q.Param("AccessKeyId"))
	if err != nil {
		if err := q.Authorize("iam:GetAccessKeyLastUsed", "*"); err != nil {
			return nil, err
		}
		return nil, noSuchEntity("The Access Key with id %s cannot be found.", q.Param("AccessKeyId"))
	}
	if err := s.authUser(q, "iam:GetAccessKeyLastUsed", k.UserName); err != nil {
		return nil, err
	}
	lu := map[string]any{"ServiceName": "N/A", "Region": "N/A"}
	if k.LastUsed != nil {
		lu = map[string]any{"LastUsedDate": *k.LastUsed, "ServiceName": "iam", "Region": core.Region}
	}
	return map[string]any{"UserName": k.UserName, "AccessKeyLastUsed": lu}, nil
}

// ---- groups ----

func (s *Service) awsCreateGroup(q *awsapi.Req) (any, error) {
	if err := required(q, "GroupName"); err != nil {
		return nil, err
	}
	path, err := validPath(q.Param("Path"))
	if err != nil {
		return nil, err
	}
	if err := q.Authorize("iam:CreateGroup", s.groupARN(q.Param("GroupName"), path)); err != nil {
		return nil, err
	}
	g, err := s.CreateGroup(q.Param("GroupName"), path, nil, q.Authorize)
	if err != nil {
		return nil, err
	}
	return map[string]any{"Group": groupXML(g)}, nil
}

func (s *Service) awsGetGroup(q *awsapi.Req) (any, error) {
	if err := required(q, "GroupName"); err != nil {
		return nil, err
	}
	name := q.Param("GroupName")
	if err := s.authGroup(q, "iam:GetGroup", name); err != nil {
		return nil, err
	}
	g, err := s.getGroupOr404(name)
	if err != nil {
		return nil, err
	}
	var users []User
	for _, n := range s.groupMembers(name) {
		if u, err := s.getUserOr404(n); err == nil {
			users = append(users, u)
		}
	}
	out, err := page(q, users, func(u User) string { return u.Name }, "Users", func(u User) any { return userXML(u, false) })
	if err != nil {
		return nil, err
	}
	out["Group"] = groupXML(g)
	return out, nil
}

func (s *Service) awsListGroups(q *awsapi.Req) (any, error) {
	if err := q.Authorize("iam:ListGroups", q.ARN("iam", "group/")); err != nil {
		return nil, err
	}
	var groups []Group
	for _, g := range store.List[Group](s.env.Store, cGroups) {
		if pathPrefix(q, g.path()) {
			groups = append(groups, g)
		}
	}
	return page(q, groups, func(g Group) string { return g.Name }, "Groups", func(g Group) any { return groupXML(g) })
}

func (s *Service) awsListGroupsForUser(q *awsapi.Req) (any, error) {
	if err := required(q, "UserName"); err != nil {
		return nil, err
	}
	name := q.Param("UserName")
	if err := s.authUser(q, "iam:ListGroupsForUser", name); err != nil {
		return nil, err
	}
	u, err := s.getUserOr404(name)
	if err != nil {
		return nil, err
	}
	var groups []Group
	for _, n := range u.Groups {
		if g, err := s.getGroupOr404(n); err == nil {
			groups = append(groups, g)
		}
	}
	return page(q, groups, func(g Group) string { return g.Name }, "Groups", func(g Group) any { return groupXML(g) })
}

func (s *Service) awsDeleteGroup(q *awsapi.Req) (any, error) {
	if err := required(q, "GroupName"); err != nil {
		return nil, err
	}
	if err := s.authGroup(q, "iam:DeleteGroup", q.Param("GroupName")); err != nil {
		return nil, err
	}
	return awsapi.NoResult{}, s.DeleteGroup(q.Param("GroupName"), false)
}

func (s *Service) awsUpdateGroup(q *awsapi.Req) (any, error) {
	if err := required(q, "GroupName"); err != nil {
		return nil, err
	}
	name, newName := q.Param("GroupName"), q.Param("NewGroupName")
	if err := s.authGroup(q, "iam:UpdateGroup", name); err != nil {
		return nil, err
	}
	if newName != "" && newName != name {
		if err := q.Authorize("iam:UpdateGroup", s.groupARN(newName, "/")); err != nil {
			return nil, err
		}
	}
	_, err := s.RenameGroup(name, newName, q.Param("NewPath"))
	return awsapi.NoResult{}, err
}

func (s *Service) awsAddUserToGroup(q *awsapi.Req) (any, error) {
	if err := required(q, "GroupName", "UserName"); err != nil {
		return nil, err
	}
	if err := s.authGroup(q, "iam:AddUserToGroup", q.Param("GroupName")); err != nil {
		return nil, err
	}
	_, err := s.AddUserToGroup(q.Param("GroupName"), q.Param("UserName"))
	return awsapi.NoResult{}, err
}

func (s *Service) awsRemoveUserFromGroup(q *awsapi.Req) (any, error) {
	if err := required(q, "GroupName", "UserName"); err != nil {
		return nil, err
	}
	if err := s.authGroup(q, "iam:RemoveUserFromGroup", q.Param("GroupName")); err != nil {
		return nil, err
	}
	_, err := s.RemoveUserFromGroup(q.Param("GroupName"), q.Param("UserName"))
	return awsapi.NoResult{}, err
}
