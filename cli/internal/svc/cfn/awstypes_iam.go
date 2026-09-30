package cfn

import (
	"fmt"

	"github.com/homecloudhq/homecloud/cli/internal/core"
)

// IAM resources. They need CAPABILITY_IAM (CAPABILITY_NAMED_IAM when the
// template names them), as in AWS.

// inlinePolicies puts the Policies list of a role, user or group.
func inlinePolicies(x *xctx, kind, name string, in map[string]any) error {
	for _, p := range lv(in, "Policies") {
		pm, _ := p.(map[string]any)
		if _, err := x.Call("PUT", "/api/v1/iam/"+kind+"/"+esc(name)+"/inline-policies/"+esc(sv(pm, "PolicyName")), doc(pm["PolicyDocument"])); err != nil {
			return err
		}
	}
	return nil
}

func strs(v []any) []string {
	out := make([]string, 0, len(v))
	for _, e := range v {
		out = append(out, toStr(e))
	}
	return out
}

func iamARN(v *attrView, kind string) string {
	if a := attrStr(v, "arn"); a != "" {
		return a
	}
	path := sv(v.Props, "Path")
	if path == "" || path == "/" {
		path = "/"
	}
	return core.ARN(v.Account, "iam", kind+path+v.ID)
}

func init() {
	awsTypes["AWS::IAM::Role"] = awsType{
		IAM: true, Named: "RoleName",
		Create: func(x *xctx, in map[string]any) (string, map[string]any, error) {
			if err := req(in, "AssumeRolePolicyDocument"); err != nil {
				return "", nil, err
			}
			name := sv(in, "RoleName")
			if name == "" {
				name = x.GenName(64, false)
			}
			body := map[string]any{"name": name, "assume_role_policy": doc(in["AssumeRolePolicyDocument"])}
			for k, n := range map[string]string{"Path": "path", "Description": "description", "PermissionsBoundary": "permissions_boundary"} {
				if has(in, k) {
					body[n] = sv(in, k)
				}
			}
			setInt(body, "max_session_duration", in, "MaxSessionDuration")
			if l := lv(in, "ManagedPolicyArns"); len(l) > 0 {
				body["policies"] = strs(l)
			}
			if t := tagMap(in["Tags"]); t != nil {
				body["tags"] = t
			}
			out, err := x.Call("POST", "/api/v1/iam/roles", body)
			if err != nil {
				return "", nil, err
			}
			attrs, _ := out.(map[string]any)
			if err := inlinePolicies(x, "roles", name, in); err != nil {
				return name, attrs, err
			}
			return name, attrs, nil
		},
		Delete: func(x *xctx, r *Resource) error {
			_, err := x.Call("DELETE", "/api/v1/iam/roles/"+esc(r.native())+"?force=true", nil)
			return err
		},
		Att: func(v *attrView, n string) (any, bool) {
			switch n {
			case "Arn":
				return iamARN(v, "role"), true
			case "RoleId":
				return firstNonEmpty(attrStr(v, "id"), attrStr(v, "role_id")), true
			}
			return nil, false
		},
	}

	awsTypes["AWS::IAM::User"] = awsType{
		IAM: true, Named: "UserName",
		Create: func(x *xctx, in map[string]any) (string, map[string]any, error) {
			name := sv(in, "UserName")
			if name == "" {
				name = x.GenName(64, false)
			}
			body := map[string]any{"name": name}
			if has(in, "Path") {
				body["path"] = sv(in, "Path")
			}
			if l := lv(in, "Groups"); len(l) > 0 {
				body["groups"] = strs(l)
			}
			if l := lv(in, "ManagedPolicyArns"); len(l) > 0 {
				body["policies"] = strs(l)
			}
			if lp := mv(in, "LoginProfile"); lp != nil {
				body["password"] = sv(lp, "Password")
				body["password_reset_required"] = bv(lp, "PasswordResetRequired")
			}
			if has(in, "PermissionsBoundary") {
				body["permissions_boundary"] = sv(in, "PermissionsBoundary")
			}
			if t := tagMap(in["Tags"]); t != nil {
				body["tags"] = t
			}
			out, err := x.Call("POST", "/api/v1/iam/users", body)
			if err != nil {
				return "", nil, err
			}
			attrs, _ := out.(map[string]any)
			if err := inlinePolicies(x, "users", name, in); err != nil {
				return name, attrs, err
			}
			return name, attrs, nil
		},
		Delete: func(x *xctx, r *Resource) error {
			_, err := x.Call("DELETE", "/api/v1/iam/users/"+esc(r.native()), nil)
			return err
		},
		Att: func(v *attrView, n string) (any, bool) {
			if n == "Arn" {
				return iamARN(v, "user"), true
			}
			return nil, false
		},
	}

	awsTypes["AWS::IAM::Group"] = awsType{
		IAM: true, Named: "GroupName",
		Create: func(x *xctx, in map[string]any) (string, map[string]any, error) {
			name := sv(in, "GroupName")
			if name == "" {
				name = x.GenName(128, false)
			}
			body := map[string]any{"name": name}
			if has(in, "Path") {
				body["path"] = sv(in, "Path")
			}
			if l := lv(in, "ManagedPolicyArns"); len(l) > 0 {
				body["policies"] = strs(l)
			}
			out, err := x.Call("POST", "/api/v1/iam/groups", body)
			if err != nil {
				return "", nil, err
			}
			attrs, _ := out.(map[string]any)
			if err := inlinePolicies(x, "groups", name, in); err != nil {
				return name, attrs, err
			}
			return name, attrs, nil
		},
		Delete: func(x *xctx, r *Resource) error {
			_, err := x.Call("DELETE", "/api/v1/iam/groups/"+esc(r.native()), nil)
			return err
		},
		Att: func(v *attrView, n string) (any, bool) {
			if n == "Arn" {
				return iamARN(v, "group"), true
			}
			return nil, false
		},
	}

	// AWS::IAM::Policy: an inline policy put on roles, users and groups.
	awsTypes["AWS::IAM::Policy"] = awsType{
		IAM: true,
		Create: func(x *xctx, in map[string]any) (string, map[string]any, error) {
			if err := req(in, "PolicyName", "PolicyDocument"); err != nil {
				return "", nil, err
			}
			pn := sv(in, "PolicyName")
			attached := map[string]any{}
			for field, kind := range map[string]string{"Roles": "roles", "Users": "users", "Groups": "groups"} {
				for _, target := range lv(in, field) {
					if _, err := x.Call("PUT", "/api/v1/iam/"+kind+"/"+esc(toStr(target))+"/inline-policies/"+esc(pn), doc(in["PolicyDocument"])); err != nil {
						return "", attached, err
					}
					attached[kind] = append(anyList(attached[kind]), toStr(target))
				}
			}
			if len(attached) == 0 {
				return "", nil, fmt.Errorf("Property validation failure: [At least one of Roles, Users or Groups must be set]")
			}
			attached["policy_name"] = pn
			return x.Stack + "-" + x.Logical + "-" + randID(12), attached, nil
		},
		Delete: func(x *xctx, r *Resource) error {
			pn := sv(r.Attributes, "policy_name")
			for _, kind := range []string{"roles", "users", "groups"} {
				for _, target := range lv(r.Attributes, kind) {
					if _, err := x.Call("DELETE", "/api/v1/iam/"+kind+"/"+esc(toStr(target))+"/inline-policies/"+esc(pn), nil); err != nil && !gone(err) {
						return err
					}
				}
			}
			return nil
		},
	}

	awsTypes["AWS::IAM::ManagedPolicy"] = awsType{
		IAM: true, Named: "ManagedPolicyName",
		Create: func(x *xctx, in map[string]any) (string, map[string]any, error) {
			if err := req(in, "PolicyDocument"); err != nil {
				return "", nil, err
			}
			name := sv(in, "ManagedPolicyName")
			if name == "" {
				name = x.GenName(128, false)
			}
			body := map[string]any{"name": name, "document": doc(in["PolicyDocument"])}
			if has(in, "Path") {
				body["path"] = sv(in, "Path")
			}
			if has(in, "Description") {
				body["description"] = sv(in, "Description")
			}
			out, err := x.Call("POST", "/api/v1/iam/policies", body)
			if err != nil {
				return "", nil, err
			}
			attrs, _ := out.(map[string]any)
			arn := sv(attrs, "arn")
			if arn == "" {
				path := sv(in, "Path")
				if path == "" {
					path = "/"
				}
				arn = core.ARN(x.Account, "iam", "policy"+path+name)
			}
			attrs = merge(attrs, map[string]any{"arn": arn, "policy_name": name})
			for field, kind := range map[string]string{"Roles": "roles", "Users": "users", "Groups": "groups"} {
				for _, target := range lv(in, field) {
					if _, err := x.Call("POST", "/api/v1/iam/"+kind+"/"+esc(toStr(target))+"/policies", map[string]any{"policy": arn}); err != nil {
						return name, attrs, err
					}
					attrs[kind] = append(anyList(attrs[kind]), toStr(target))
				}
			}
			return name, attrs, nil
		},
		Delete: func(x *xctx, r *Resource) error {
			arn := sv(r.Attributes, "arn")
			for _, kind := range []string{"roles", "users", "groups"} {
				for _, target := range lv(r.Attributes, kind) {
					if _, err := x.Call("DELETE", "/api/v1/iam/"+kind+"/"+esc(toStr(target))+"/policies/"+esc(arn), nil); err != nil && !gone(err) {
						return err
					}
				}
			}
			_, err := x.Call("DELETE", "/api/v1/iam/policies/"+esc(r.native()), nil)
			return err
		},
		Ref: func(v *attrView) string { return firstNonEmpty(attrStr(v, "arn"), iamARN(v, "policy")) },
	}
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}
