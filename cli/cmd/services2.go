package cmd

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"os"
	"strings"

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
	c := sub(l, "create NAME", "Create a load balancer; --listen PORT=TARGET_GROUP", cobra.ExactArgs(1), func(a []string) error {
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
		return call("POST", "/api/v1/elb/load-balancers", map[string]any{"name": a[0], "scheme": scheme, "listeners": ls}, nil)
	})
	c.Flags().StringSliceVar(&listen, "listen", nil, "listener PORT=TARGET_GROUP")
	c.Flags().StringVar(&scheme, "scheme", "internet-facing", "internet-facing or internal")
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
