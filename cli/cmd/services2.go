package cmd

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

func init() {
	kmsCommands()
	ssmCommands()
	containerCommands()
	elbCommands()
}

func kmsCommands() {
	k := group("kms", "Encryption keys")
	sub(k, "ls", "List keys", cobra.NoArgs, func([]string) error {
		return call("GET", "/api/v1/kms/keys", nil, cols("ID=id", "ALIASES=aliases", "STATE=state", "ROTATION=rotation_enabled", "VERSIONS=key_versions", "DESCRIPTION=description"))
	})
	var alias, desc string
	var rotate bool
	c := sub(k, "create", "Create a symmetric key", cobra.NoArgs, func([]string) error {
		return call("POST", "/api/v1/kms/keys", map[string]any{"alias": alias, "description": desc, "rotation_enabled": rotate}, nil)
	})
	c.Flags().StringVar(&alias, "alias", "", "alias, e.g. alias/app")
	c.Flags().StringVar(&desc, "description", "", "description")
	c.Flags().BoolVar(&rotate, "rotate", false, "rotate the key material yearly")
	sub(k, "encrypt KEY PLAINTEXT", "Encrypt a string (prints the ciphertext blob)", cobra.ExactArgs(2), func(a []string) error {
		var out map[string]any
		if err := api().Do("POST", "/api/v1/kms/encrypt", map[string]any{"key_id": a[0], "plaintext": base64.StdEncoding.EncodeToString([]byte(a[1]))}, &out); err != nil {
			return err
		}
		fmt.Println(out["ciphertext_blob"])
		return nil
	})
	sub(k, "decrypt BLOB", "Decrypt a ciphertext blob", cobra.ExactArgs(1), func(a []string) error {
		var out map[string]any
		if err := api().Do("POST", "/api/v1/kms/decrypt", map[string]any{"ciphertext_blob": a[0]}, &out); err != nil {
			return err
		}
		b, _ := base64.StdEncoding.DecodeString(fmt.Sprint(out["plaintext"]))
		os.Stdout.Write(b)
		fmt.Println()
		return nil
	})
}

func ssmCommands() {
	s := group("ssm", "Parameter Store")
	var prefix string
	ls := sub(s, "ls", "List parameters", cobra.NoArgs, func([]string) error {
		return call("GET", "/api/v1/ssm/parameters?prefix="+url.QueryEscape(prefix), nil, cols("NAME=name", "TYPE=type", "VERSION=version", "MODIFIED=last_modified"))
	})
	ls.Flags().StringVar(&prefix, "prefix", "", "name prefix, e.g. /app/")
	var typ, desc string
	var overwrite bool
	p := sub(s, "put NAME VALUE", "Create or update a parameter", cobra.ExactArgs(2), func(a []string) error {
		body := map[string]any{"name": a[0], "value": a[1], "overwrite": overwrite}
		if typ != "" {
			body["type"] = typ
		}
		if desc != "" {
			body["description"] = desc
		}
		return call("PUT", "/api/v1/ssm/parameter", body, nil)
	})
	p.Flags().StringVar(&typ, "type", "", "String, StringList or SecureString")
	p.Flags().StringVar(&desc, "description", "", "description")
	p.Flags().BoolVar(&overwrite, "overwrite", false, "replace an existing parameter")
	var decrypt bool
	g := sub(s, "get NAME", "Print a parameter's value (NAME:3 or NAME:label selects a version)", cobra.ExactArgs(1), func(a []string) error {
		var out map[string]any
		if err := api().Do("GET", fmt.Sprintf("/api/v1/ssm/parameter?name=%s&with_decryption=%v", url.QueryEscape(a[0]), decrypt), nil, &out); err != nil {
			return err
		}
		if output == "json" {
			printJSON(out)
		} else {
			fmt.Println(out["value"])
		}
		return nil
	})
	g.Flags().BoolVar(&decrypt, "decrypt", false, "decrypt SecureString values")
	var recursive bool
	bp := sub(s, "path PATH", "Print every parameter under a path as NAME=VALUE", cobra.ExactArgs(1), func(a []string) error {
		var out []map[string]any
		if err := api().Do("GET", fmt.Sprintf("/api/v1/ssm/parameters-by-path?path=%s&recursive=%v&with_decryption=%v", url.QueryEscape(a[0]), recursive, decrypt), nil, &out); err != nil {
			return err
		}
		for _, p := range out {
			fmt.Printf("%s=%v\n", p["name"], p["value"])
		}
		return nil
	})
	bp.Flags().BoolVarP(&recursive, "recursive", "r", true, "include nested paths")
	bp.Flags().BoolVar(&decrypt, "decrypt", true, "decrypt SecureString values")
	sub(s, "delete NAME", "Delete a parameter", cobra.ExactArgs(1), func(a []string) error {
		return call("DELETE", "/api/v1/ssm/parameter?name="+url.QueryEscape(a[0]), nil, nil)
	})
}

func containerCommands() {
	e := group("efs", "Shared file systems")
	sub(e, "ls", "List file systems", cobra.NoArgs, func([]string) error {
		return call("GET", "/api/v1/efs/file-systems", nil, cols("ID=id", "NAME=name", "STATE=state", "MOUNTED BY=mounted_by", "CREATED=created_at"))
	})
	sub(e, "create NAME", "Create a file system (mount it with 'ec2 run --fs fs-id:/path')", cobra.ExactArgs(1), func(a []string) error {
		return call("POST", "/api/v1/efs/file-systems", map[string]any{"name": a[0]}, nil)
	})
	sub(e, "delete ID", "Delete a file system", cobra.ExactArgs(1), func(a []string) error { return call("DELETE", "/api/v1/efs/file-systems/"+a[0], nil, nil) })

	r := group("ecr", "Container registry")
	sub(r, "ls", "List repositories", cobra.NoArgs, func([]string) error {
		return call("GET", "/api/v1/ecr/repositories", nil, cols("NAME=name", "URI=uri", "CREATED=created_at"))
	})
	sub(r, "create NAME", "Create a repository", cobra.ExactArgs(1), func(a []string) error {
		return call("POST", "/api/v1/ecr/repositories", map[string]any{"name": a[0]}, nil)
	})
	sub(r, "images REPO", "List images in a repository", cobra.ExactArgs(1), func(a []string) error {
		var out map[string]any
		if err := api().Do("GET", "/api/v1/ecr/repositories/"+a[0], nil, &out); err != nil {
			return err
		}
		printList(out["images"], cols("TAGS=tags", "DIGEST=digest", "SIZE=size_bytes", "PUSHED=pushed_at"))
		return nil
	})
	var force bool
	d := sub(r, "delete REPO", "Delete a repository", cobra.ExactArgs(1), func(a []string) error {
		return call("DELETE", fmt.Sprintf("/api/v1/ecr/repositories/%s?force=%v", a[0], force), nil, nil)
	})
	d.Flags().BoolVar(&force, "force", false, "delete its images too")

	c := group("ecs", "Container services and tasks")
	sub(c, "services", "List services", cobra.NoArgs, func([]string) error {
		return call("GET", "/api/v1/ecs/services", nil, cols("NAME=name", "TASK DEF=task_definition", "DESIRED=desired_count", "RUNNING=running_count", "STATUS=status", "ENDPOINT=endpoint"))
	})
	sub(c, "tasks", "List tasks", cobra.NoArgs, func([]string) error {
		return call("GET", "/api/v1/ecs/tasks", nil, cols("ID=id", "SERVICE=service", "TASK DEF=task_definition", "STATUS=last_status", "IP=private_ip", "STOP REASON=stop_reason"))
	})
	var td struct {
		Image string
		CPU   float64
		Mem   int64
		Port  int
		Env   []string
		Sec   []string
		Cmd   string
	}
	reg := sub(c, "register FAMILY", "Register a task definition revision", cobra.ExactArgs(1), func(a []string) error {
		secrets := []map[string]string{}
		for _, s := range td.Sec {
			n, v, _ := strings.Cut(s, "=")
			secrets = append(secrets, map[string]string{"name": n, "value_from": v})
		}
		body := map[string]any{"family": a[0], "image": td.Image, "cpu": td.CPU, "memory_mb": td.Mem, "container_port": td.Port,
			"environment": tagsFlag(td.Env), "secrets": secrets}
		if td.Cmd != "" {
			body["command"] = []string{"/bin/sh", "-c", td.Cmd}
		}
		return call("POST", "/api/v1/ecs/task-definitions", body, nil)
	})
	f := reg.Flags()
	f.StringVar(&td.Image, "image", "", "container image")
	f.Float64Var(&td.CPU, "cpu", 0.25, "vCPUs")
	f.Int64Var(&td.Mem, "memory", 512, "memory in MB")
	f.IntVar(&td.Port, "port", 0, "container port")
	f.StringSliceVar(&td.Env, "env", nil, "environment KEY=VALUE")
	f.StringSliceVar(&td.Sec, "secret", nil, "secret env from Secrets Manager: KEY=secret-name[:json-key]")
	f.StringVar(&td.Cmd, "command", "", "shell command to run")
	var count int
	var tg string
	cs := sub(c, "create-service NAME TASK_DEF", "Run and maintain tasks of a task definition", cobra.ExactArgs(2), func(a []string) error {
		body := map[string]any{"name": a[0], "task_definition": a[1], "desired_count": count}
		if tg != "" {
			body["load_balancer"] = map[string]any{"target_group": tg}
		}
		return call("POST", "/api/v1/ecs/services", body, nil)
	})
	cs.Flags().IntVar(&count, "count", 1, "desired task count")
	cs.Flags().StringVar(&tg, "target-group", "", "register tasks with this load balancer target group")
	var newTD string
	up := sub(c, "update-service NAME", "Scale a service or deploy a new task definition", cobra.ExactArgs(1), func(a []string) error {
		body := map[string]any{"task_definition": newTD}
		if cmdCount := count; cmdCount >= 0 {
			body["desired_count"] = cmdCount
		}
		return call("PATCH", "/api/v1/ecs/services/"+a[0], body, nil)
	})
	up.Flags().IntVar(&count, "count", -1, "new desired count")
	up.Flags().StringVar(&newTD, "task-definition", "", "family or family:revision to deploy")
	sub(c, "delete-service NAME", "Stop all tasks and delete a service", cobra.ExactArgs(1), func(a []string) error {
		return call("DELETE", "/api/v1/ecs/services/"+a[0], nil, nil)
	})
	sub(c, "run TASK_DEF", "Run a one-off task", cobra.ExactArgs(1), func(a []string) error {
		return call("POST", "/api/v1/ecs/tasks", map[string]any{"task_definition": a[0]}, nil)
	})
	sub(c, "logs TASK_ID", "Print a task's output", cobra.ExactArgs(1), func(a []string) error {
		var out map[string]any
		if err := api().Do("GET", "/api/v1/ecs/tasks/"+a[0]+"/logs", nil, &out); err != nil {
			return err
		}
		fmt.Print(out["output"])
		return nil
	})
}

func elbCommands() {
	l := group("elb", "Load balancers and target groups")
	sub(l, "ls", "List load balancers", cobra.NoArgs, func([]string) error {
		return call("GET", "/api/v1/elb/load-balancers", nil, cols("NAME=name", "STATE=state", "SCHEME=scheme", "DNS=dns_name", "PORTS=public_ports"))
	})
	var listen []string
	var scheme string
	var httpsListen []string
	var redirect int
	c := sub(l, "create NAME", "Create a load balancer; --listen PORT=TARGET_GROUP, --https PORT=TARGET_GROUP@CERT_ARN", cobra.ExactArgs(1), func(a []string) error {
		ls := []map[string]any{}
		for _, x := range listen {
			p, tg, ok := strings.Cut(x, "=")
			if !ok {
				return fmt.Errorf("--listen must be PORT=TARGET_GROUP")
			}
			var port int
			fmt.Sscan(p, &port)
			ls = append(ls, map[string]any{"port": port, "default_target_group": tg})
		}
		for _, x := range httpsListen {
			p, rest, ok := strings.Cut(x, "=")
			tg, cert, ok2 := strings.Cut(rest, "@")
			if !ok || !ok2 {
				return fmt.Errorf("--https must be PORT=TARGET_GROUP@CERTIFICATE_ARN")
			}
			var port int
			fmt.Sscan(p, &port)
			ls = append(ls, map[string]any{"port": port, "protocol": "HTTPS", "default_target_group": tg, "certificate_arn": cert})
			if redirect > 0 {
				ls = append(ls, map[string]any{"port": redirect, "redirect_https_port": port})
			}
		}
		return call("POST", "/api/v1/elb/load-balancers", map[string]any{"name": a[0], "scheme": scheme, "listeners": ls}, nil)
	})
	c.Flags().StringSliceVar(&listen, "listen", nil, "listener PORT=TARGET_GROUP")
	c.Flags().StringVar(&scheme, "scheme", "internet-facing", "internet-facing or internal")
	c.Flags().StringSliceVar(&httpsListen, "https", nil, "HTTPS listener PORT=TARGET_GROUP@CERTIFICATE_ARN")
	c.Flags().IntVar(&redirect, "redirect-http", 0, "also listen on this HTTP port and redirect to the HTTPS listener")
	sub(l, "delete NAME", "Delete a load balancer", cobra.ExactArgs(1), func(a []string) error { return call("DELETE", "/api/v1/elb/load-balancers/"+a[0], nil, nil) })
	sub(l, "target-groups", "List target groups", cobra.NoArgs, func([]string) error {
		return call("GET", "/api/v1/elb/target-groups", nil, cols("NAME=name", "PORT=port", "VPC=vpc_id", "HEALTH PATH=health_check.path"))
	})
	var port int
	var path string
	tg := sub(l, "create-target-group NAME", "Create a target group", cobra.ExactArgs(1), func(a []string) error {
		return call("POST", "/api/v1/elb/target-groups", map[string]any{"name": a[0], "port": port, "health_check": map[string]any{"path": path}}, nil)
	})
	tg.Flags().IntVar(&port, "port", 80, "target port")
	tg.Flags().StringVar(&path, "health-path", "/", "health check path")
	sub(l, "register TARGET_GROUP ID...", "Register instances as targets", cobra.MinimumNArgs(2), func(a []string) error {
		ts := []map[string]any{}
		for _, id := range a[1:] {
			ts = append(ts, map[string]any{"id": id})
		}
		return call("POST", "/api/v1/elb/target-groups/"+a[0]+"/targets", map[string]any{"targets": ts}, nil)
	})
	sub(l, "health TARGET_GROUP", "Show target health", cobra.ExactArgs(1), func(a []string) error {
		var out map[string]any
		if err := api().Do("GET", "/api/v1/elb/target-groups/"+a[0], nil, &out); err != nil {
			return err
		}
		printList(out["targets"], cols("TARGET=id", "PORT=port", "IP=ip", "HEALTH=health", "REASON=reason"))
		return nil
	})
}

func init() {
	s := group("sfn", "Step Functions state machines")
	sub(s, "ls", "List state machines", cobra.NoArgs, func([]string) error {
		return call("GET", "/api/v1/sfn/state-machines", nil, cols("NAME=name", "STATUS=status", "EXECUTIONS=executions", "UPDATED=updated_at"))
	})
	sub(s, "create NAME DEFINITION_FILE", "Create a state machine from an Amazon States Language file", cobra.ExactArgs(2), func(a []string) error {
		def, err := jsonArg("@" + a[1])
		if err != nil {
			return err
		}
		return call("POST", "/api/v1/sfn/state-machines", map[string]any{"name": a[0], "definition": def}, nil)
	})
	sub(s, "update NAME DEFINITION_FILE", "Replace a state machine's definition", cobra.ExactArgs(2), func(a []string) error {
		def, err := jsonArg("@" + a[1])
		if err != nil {
			return err
		}
		return call("PUT", "/api/v1/sfn/state-machines/"+a[0], map[string]any{"definition": def}, nil)
	})
	var input string
	var wait bool
	st := sub(s, "start NAME", "Start an execution", cobra.ExactArgs(1), func(a []string) error {
		in, err := jsonArg(input)
		if err != nil {
			return err
		}
		c := api()
		var out map[string]any
		if err := c.Do("POST", "/api/v1/sfn/state-machines/"+a[0]+"/executions", map[string]any{"input": in}, &out); err != nil {
			return err
		}
		if !wait {
			printObject(out)
			return nil
		}
		for {
			var x map[string]any
			if err := c.Do("GET", "/api/v1/sfn/executions/"+fmt.Sprint(out["id"]), nil, &x); err != nil {
				return err
			}
			if x["status"] != "RUNNING" {
				delete(x, "history")
				printJSON(x)
				if x["status"] != "SUCCEEDED" {
					return fmt.Errorf("execution %v", x["status"])
				}
				return nil
			}
			time.Sleep(500 * time.Millisecond)
		}
	})
	st.Flags().StringVar(&input, "input", "{}", "input JSON (or @file)")
	st.Flags().BoolVar(&wait, "wait", true, "wait for the execution to finish")
	sub(s, "executions NAME", "List executions", cobra.ExactArgs(1), func(a []string) error {
		return call("GET", "/api/v1/sfn/state-machines/"+a[0]+"/executions", nil, cols("ID=id", "NAME=name", "STATUS=status", "STARTED=start_date", "STOPPED=stop_date"))
	})
	sub(s, "history EXECUTION_ID", "Show an execution's event history", cobra.ExactArgs(1), func(a []string) error {
		var x map[string]any
		if err := api().Do("GET", "/api/v1/sfn/executions/"+a[0], nil, &x); err != nil {
			return err
		}
		printList(x["history"], cols("ID=id", "TIME=timestamp", "TYPE=type", "STATE=state"))
		return nil
	})
}

func init() {
	c := group("cloudformation", "Infrastructure as code: stacks from YAML/JSON templates")
	c.Aliases = []string{"cfn"}
	sub(c, "ls", "List stacks", cobra.NoArgs, func([]string) error {
		return call("GET", "/api/v1/cloudformation/stacks", nil, cols("NAME=name", "STATUS=status", "RESOURCES=resources", "UPDATED=updated_at", "REASON=status_reason"))
	})
	var ps []string
	var noRollback, wait bool
	deploy := func(name, file string, update bool) error {
		var b []byte
		if file != "" {
			var err error
			if b, err = os.ReadFile(file); err != nil {
				return err
			}
		}
		params := map[string]any{}
		for _, p := range ps {
			k, v, _ := strings.Cut(p, "=")
			params[k] = v
		}
		c := api()
		seen := 0
		if update || file == "" {
			var before struct {
				Events []any `json:"events"`
			}
			_ = c.Do("GET", "/api/v1/cloudformation/stacks/"+name, nil, &before)
			seen = len(before.Events)
		}
		body := map[string]any{"name": name, "template": string(b), "parameters": params, "disable_rollback": noRollback}
		method, path := "POST", "/api/v1/cloudformation/stacks"
		if update {
			method, path = "PUT", "/api/v1/cloudformation/stacks/"+name
		}
		if file == "" {
			method, path, body = "DELETE", "/api/v1/cloudformation/stacks/"+name, nil
		}
		if err := c.Do(method, path, body, nil); err != nil {
			return err
		}
		if !wait {
			return nil
		}
		for {
			var st struct {
				Status       string           `json:"status"`
				StatusReason string           `json:"status_reason"`
				Events       []map[string]any `json:"events"`
				Outputs      map[string]any   `json:"outputs"`
			}
			if err := c.Do("GET", "/api/v1/cloudformation/stacks/"+name, nil, &st); err != nil {
				if strings.Contains(err.Error(), "StackNotFound") {
					return nil
				}
				return err
			}
			for i := len(st.Events) - 1 - seen; i >= 0; i-- {
				e := st.Events[i]
				reason, _ := e["reason"].(string)
				fmt.Printf("%s  %-32v %-18v %-20v %s\n", format(e["time"]), e["type"], e["logical_id"], e["status"], reason)
			}
			seen = len(st.Events)
			if !strings.HasSuffix(st.Status, "_IN_PROGRESS") {
				if len(st.Outputs) > 0 {
					fmt.Println("\nOutputs:")
					printObject(st.Outputs)
				}
				if strings.Contains(st.Status, "FAILED") || strings.Contains(st.Status, "ROLLBACK") {
					return fmt.Errorf("%s: %s", st.Status, st.StatusReason)
				}
				return nil
			}
			time.Sleep(time.Second)
		}
	}
	cr := sub(c, "create NAME TEMPLATE_FILE", "Create a stack", cobra.ExactArgs(2), func(a []string) error { return deploy(a[0], a[1], false) })
	up := sub(c, "update NAME TEMPLATE_FILE", "Update a stack (changed resources are replaced)", cobra.ExactArgs(2), func(a []string) error { return deploy(a[0], a[1], true) })
	for _, cmd := range []*cobra.Command{cr, up} {
		cmd.Flags().StringSliceVarP(&ps, "param", "p", nil, "parameter Key=Value")
		cmd.Flags().BoolVar(&wait, "wait", true, "stream events until the operation finishes")
	}
	cr.Flags().BoolVar(&noRollback, "no-rollback", false, "keep created resources when creation fails")
	sub(c, "describe NAME", "Show a stack's resources and outputs", cobra.ExactArgs(1), func(a []string) error {
		var st map[string]any
		if err := api().Do("GET", "/api/v1/cloudformation/stacks/"+a[0], nil, &st); err != nil {
			return err
		}
		if output == "json" {
			printJSON(st)
			return nil
		}
		fmt.Printf("%s  %v %v\n\n", a[0], st["status"], st["status_reason"])
		res := []any{}
		if m, ok := st["resources"].(map[string]any); ok {
			for _, r := range m {
				res = append(res, r)
			}
		}
		printList(res, cols("LOGICAL ID=logical_id", "TYPE=type", "PHYSICAL ID=physical_id", "STATUS=status"))
		if o, ok := st["outputs"].(map[string]any); ok && len(o) > 0 {
			fmt.Println("\nOutputs:")
			printObject(o)
		}
		return nil
	})
	del := sub(c, "delete NAME", "Delete a stack and its resources", cobra.ExactArgs(1), func(a []string) error { return deploy(a[0], "", false) })
	del.Flags().BoolVar(&wait, "wait", true, "stream events until the stack is gone")
	sub(c, "types", "List supported resource types", cobra.NoArgs, func([]string) error {
		var out []string
		if err := api().Do("GET", "/api/v1/cloudformation/resource-types", nil, &out); err != nil {
			return err
		}
		fmt.Println(strings.Join(out, "\n"))
		return nil
	})
}

func init() {
	c := group("cognito", "User pools for application sign-up and sign-in")
	sub(c, "pools", "List user pools", cobra.NoArgs, func([]string) error {
		return call("GET", "/api/v1/cognito/user-pools", nil, cols("ID=id", "NAME=name", "USERS=users", "CLIENTS=clients", "ISSUER=issuer"))
	})
	sub(c, "create-pool NAME", "Create a user pool", cobra.ExactArgs(1), func(a []string) error {
		return call("POST", "/api/v1/cognito/user-pools", map[string]any{"name": a[0]}, nil)
	})
	var secret bool
	cc := sub(c, "create-client POOL NAME", "Create an app client", cobra.ExactArgs(2), func(a []string) error {
		return call("POST", "/api/v1/cognito/user-pools/"+a[0]+"/clients", map[string]any{"name": a[1], "generate_secret": secret}, nil)
	})
	cc.Flags().BoolVar(&secret, "secret", false, "generate a client secret (for server-side apps)")
	sub(c, "users POOL", "List users in a pool", cobra.ExactArgs(1), func(a []string) error {
		return call("GET", "/api/v1/cognito/user-pools/"+a[0]+"/users", nil, cols("USERNAME=username", "STATUS=status", "ENABLED=enabled", "EMAIL=attributes.email", "GROUPS=groups", "LAST SIGN-IN=last_sign_in"))
	})
	var pw string
	var attrs []string
	cu := sub(c, "create-user POOL USERNAME", "Create a user (prints a temporary password when --password is omitted)", cobra.ExactArgs(2), func(a []string) error {
		return call("POST", "/api/v1/cognito/user-pools/"+a[0]+"/users", map[string]any{"username": a[1], "password": pw, "attributes": tagsFlag(attrs)}, nil)
	})
	cu.Flags().StringVar(&pw, "password", "", "permanent password")
	cu.Flags().StringSliceVar(&attrs, "attr", nil, "attributes key=value, e.g. email=a@b.c")
	var clientID string
	login := sub(c, "login POOL USERNAME PASSWORD", "Sign in and print tokens (handy for testing APIs)", cobra.ExactArgs(3), func(a []string) error {
		var out map[string]any
		resp, err := api().Request("POST", "/cognito/"+a[0]+"/auth", map[string]any{"client_id": clientID, "username": a[1], "password": a[2]}, nil)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return err
		}
		printObject(out)
		return nil
	})
	login.Flags().StringVar(&clientID, "client", "", "app client ID (required)")
	_ = login.MarkFlagRequired("client")
}

func init() {
	a := group("autoscaling", "EC2 Auto Scaling groups")
	a.Aliases = []string{"asg"}
	sub(a, "ls", "List groups", cobra.NoArgs, func([]string) error {
		return call("GET", "/api/v1/autoscaling/groups", nil, cols("NAME=name", "MIN=min_size", "DESIRED=desired_capacity", "MAX=max_size", "IMAGE=launch.image_id", "TYPE=launch.instance_type", "STATUS=status"))
	})
	var in struct {
		Image, Type       string
		Min, Max, Desired int
		SGs, Subnets, TGs []string
		CPUTarget         float64
	}
	c := sub(a, "create NAME", "Create a group", cobra.ExactArgs(1), func(args []string) error {
		body := map[string]any{"name": args[0], "launch": map[string]any{"image_id": in.Image, "instance_type": in.Type, "security_group_ids": in.SGs},
			"min_size": in.Min, "max_size": in.Max, "desired_capacity": in.Desired, "subnet_ids": in.Subnets, "target_groups": in.TGs}
		if in.CPUTarget > 0 {
			body["policies"] = []map[string]any{{"metric": "CPUUtilization", "target_value": in.CPUTarget}}
		}
		return call("POST", "/api/v1/autoscaling/groups", body, nil)
	})
	f := c.Flags()
	f.StringVar(&in.Image, "image", "ami-nginx", "image (AMI) id")
	f.StringVar(&in.Type, "type", "t3.micro", "instance type")
	f.IntVar(&in.Min, "min", 1, "minimum size")
	f.IntVar(&in.Max, "max", 3, "maximum size")
	f.IntVar(&in.Desired, "desired", 1, "desired capacity")
	f.StringSliceVar(&in.SGs, "sg", nil, "security group ids")
	f.StringSliceVar(&in.Subnets, "subnet", nil, "subnets to spread instances across")
	f.StringSliceVar(&in.TGs, "target-group", nil, "load balancer target groups to register instances with")
	f.Float64Var(&in.CPUTarget, "cpu-target", 0, "target-tracking policy: keep average CPU near this percent")
	var desired int
	sc := sub(a, "scale NAME", "Set the desired capacity", cobra.ExactArgs(1), func(args []string) error {
		return call("PATCH", "/api/v1/autoscaling/groups/"+args[0], map[string]any{"desired_capacity": desired}, nil)
	})
	sc.Flags().IntVar(&desired, "desired", 1, "desired capacity")
	sub(a, "activities NAME", "Show scaling activity", cobra.ExactArgs(1), func(args []string) error {
		var g map[string]any
		if err := api().Do("GET", "/api/v1/autoscaling/groups/"+args[0], nil, &g); err != nil {
			return err
		}
		printList(g["activities"], cols("TIME=time", "STATUS=status", "DESCRIPTION=description", "CAUSE=cause"))
		return nil
	})
	sub(a, "delete NAME", "Terminate the group's instances and delete it", cobra.ExactArgs(1), func(args []string) error {
		return call("DELETE", "/api/v1/autoscaling/groups/"+args[0], nil, nil)
	})
}

func init() {
	a := group("acm", "TLS certificates and the HomeCloud private CA")
	sub(a, "ls", "List certificates", cobra.NoArgs, func([]string) error {
		return call("GET", "/api/v1/acm/certificates", nil, cols("ID=id", "DOMAIN=domain_name", "TYPE=type", "STATUS=status", "EXPIRES=not_after"))
	})
	var sans []string
	var days int
	r := sub(a, "request DOMAIN", "Issue a certificate from the HomeCloud private CA", cobra.ExactArgs(1), func(args []string) error {
		return call("POST", "/api/v1/acm/certificates", map[string]any{"domain_name": args[0], "subject_alternative_names": sans, "valid_days": days}, nil)
	})
	r.Flags().StringSliceVar(&sans, "san", nil, "additional DNS names or IP addresses")
	r.Flags().IntVar(&days, "days", 395, "validity in days")
	var certFile, keyFile, chainFile string
	im := sub(a, "import", "Import a certificate issued elsewhere (e.g. Let's Encrypt)", cobra.NoArgs, func([]string) error {
		read := func(p string) (string, error) {
			if p == "" {
				return "", nil
			}
			b, err := os.ReadFile(p)
			return string(b), err
		}
		cert, err := read(certFile)
		if err != nil {
			return err
		}
		key, err := read(keyFile)
		if err != nil {
			return err
		}
		chain, err := read(chainFile)
		if err != nil {
			return err
		}
		return call("POST", "/api/v1/acm/certificates/import", map[string]any{"certificate": cert, "private_key": key, "certificate_chain": chain}, nil)
	})
	im.Flags().StringVar(&certFile, "cert", "", "PEM certificate file")
	im.Flags().StringVar(&keyFile, "key", "", "PEM private key file")
	im.Flags().StringVar(&chainFile, "chain", "", "PEM chain file (optional)")
	sub(a, "ca", "Print the private CA certificate (trust it on your devices)", cobra.NoArgs, func([]string) error {
		var out map[string]any
		if err := api().Do("GET", "/api/v1/acm/ca", nil, &out); err != nil {
			return err
		}
		fmt.Print(out["certificate"])
		return nil
	})
	sub(a, "delete ID", "Delete a certificate", cobra.ExactArgs(1), func(args []string) error {
		return call("DELETE", "/api/v1/acm/certificates/"+args[0], nil, nil)
	})
}
