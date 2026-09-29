package cmd

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// sub adds a leaf command that performs one API call.
func sub(parent *cobra.Command, use, short string, args cobra.PositionalArgs, run func(args []string) error) *cobra.Command {
	c := &cobra.Command{Use: use, Short: short, Args: args, RunE: func(cmd *cobra.Command, a []string) error { return run(a) }}
	parent.AddCommand(c)
	return c
}

func group(use, short string) *cobra.Command {
	c := &cobra.Command{Use: use, Short: short}
	RootCmd.AddCommand(c)
	return c
}

func esc(s string) string { return url.PathEscape(s) }

// jsonArg parses a JSON flag value, or reads it from a file when it starts with '@'.
func jsonArg(v string) (any, error) {
	if v == "" {
		return nil, nil
	}
	if strings.HasPrefix(v, "@") {
		b, err := os.ReadFile(v[1:])
		if err != nil {
			return nil, err
		}
		v = string(b)
	}
	var out any
	if err := json.Unmarshal([]byte(v), &out); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	return out, nil
}

func init() {
	vpcCommands()
	iamCommands()
	secretsCommands()
	cloudwatchCommands()
	rdsCommands()
	lambdaCommands()
	queueCommands()
	dynamoCommands()
	eventsCommands()
}

func vpcCommands() {
	v := group("vpc", "Virtual private clouds, subnets and security groups")
	sub(v, "ls", "List VPCs", cobra.NoArgs, func([]string) error {
		return call("GET", "/api/v1/vpc/vpcs", nil, cols("ID=id", "NAME=name", "CIDR=cidr", "DEFAULT=default", "INTERNET=internet_access"))
	})
	var internet bool
	var name string
	c := sub(v, "create CIDR", "Create a VPC", cobra.ExactArgs(1), func(a []string) error {
		return call("POST", "/api/v1/vpc/vpcs", map[string]any{"name": name, "cidr": a[0], "internet_access": internet}, nil)
	})
	c.Flags().StringVar(&name, "name", "", "VPC name")
	c.Flags().BoolVar(&internet, "internet", true, "allow outbound internet access")
	sub(v, "delete ID", "Delete a VPC", cobra.ExactArgs(1), func(a []string) error { return call("DELETE", "/api/v1/vpc/vpcs/"+a[0], nil, nil) })
	sub(v, "subnets", "List subnets", cobra.NoArgs, func([]string) error {
		return call("GET", "/api/v1/vpc/subnets", nil, cols("ID=id", "VPC=vpc_id", "CIDR=cidr", "AZ=availability_zone", "FREE IPS=available_ips"))
	})
	var vpcID, az string
	sc := sub(v, "create-subnet CIDR", "Create a subnet", cobra.ExactArgs(1), func(a []string) error {
		return call("POST", "/api/v1/vpc/subnets", map[string]any{"vpc_id": vpcID, "cidr": a[0], "name": name, "availability_zone": az}, nil)
	})
	sc.Flags().StringVar(&vpcID, "vpc", "", "VPC id")
	sc.Flags().StringVar(&az, "az", "", "availability zone")
	sc.Flags().StringVar(&name, "name", "", "subnet name")
	sub(v, "sgs", "List security groups", cobra.NoArgs, func([]string) error {
		return call("GET", "/api/v1/vpc/security-groups", nil, cols("ID=id", "NAME=name", "VPC=vpc_id", "DESCRIPTION=description"))
	})
	var desc string
	sg := sub(v, "create-sg NAME", "Create a security group", cobra.ExactArgs(1), func(a []string) error {
		return call("POST", "/api/v1/vpc/security-groups", map[string]any{"name": a[0], "vpc_id": vpcID, "description": desc}, nil)
	})
	sg.Flags().StringVar(&vpcID, "vpc", "", "VPC id (default VPC when empty)")
	sg.Flags().StringVar(&desc, "description", "", "description")
	var cidr, proto string
	allow := sub(v, "allow SG_ID PORT[-PORT]", "Open a port range in a security group (published on the host at launch)", cobra.ExactArgs(2), func(a []string) error {
		from, to, _ := strings.Cut(a[1], "-")
		var f, t int
		fmt.Sscan(from, &f)
		if to != "" {
			fmt.Sscan(to, &t)
		}
		return call("POST", "/api/v1/vpc/security-groups/"+a[0]+"/ingress", map[string]any{"protocol": proto, "from_port": f, "to_port": t, "cidr": cidr}, nil)
	})
	allow.Flags().StringVar(&cidr, "cidr", "0.0.0.0/0", "source CIDR")
	allow.Flags().StringVar(&proto, "protocol", "tcp", "tcp or udp")
}

func iamCommands() {
	i := group("iam", "Users, groups, policies and access keys")
	sub(i, "users", "List users", cobra.NoArgs, func([]string) error {
		return call("GET", "/api/v1/iam/users", nil, cols("NAME=name", "CONSOLE=console_access", "GROUPS=groups", "POLICIES=attached_policies", "CREATED=created_at"))
	})
	var pw string
	var policies, groups []string
	cu := sub(i, "create-user NAME", "Create a user", cobra.ExactArgs(1), func(a []string) error {
		return call("POST", "/api/v1/iam/users", map[string]any{"name": a[0], "password": pw, "policies": policies, "groups": groups}, nil)
	})
	cu.Flags().StringVar(&pw, "password", "", "console password (omit for API-only users)")
	cu.Flags().StringSliceVar(&policies, "policy", nil, "managed policies to attach, e.g. S3ReadOnlyAccess")
	cu.Flags().StringSliceVar(&groups, "group", nil, "groups to join")
	sub(i, "delete-user NAME", "Delete a user", cobra.ExactArgs(1), func(a []string) error { return call("DELETE", "/api/v1/iam/users/"+a[0], nil, nil) })
	sub(i, "create-access-key USER", "Create an access key (the secret is shown once)", cobra.ExactArgs(1), func(a []string) error {
		return call("POST", "/api/v1/iam/users/"+a[0]+"/access-keys", nil, nil)
	})
	sub(i, "access-keys USER", "List a user's access keys", cobra.ExactArgs(1), func(a []string) error {
		return call("GET", "/api/v1/iam/users/"+a[0]+"/access-keys", nil, cols("ACCESS KEY=access_key_id", "STATUS=status", "CREATED=created_at", "LAST USED=last_used"))
	})
	sub(i, "attach USER POLICY", "Attach a policy to a user", cobra.ExactArgs(2), func(a []string) error {
		return call("POST", "/api/v1/iam/users/"+a[0]+"/policies", map[string]string{"policy": a[1]}, nil)
	})
	sub(i, "detach USER POLICY", "Detach a policy from a user", cobra.ExactArgs(2), func(a []string) error {
		return call("DELETE", "/api/v1/iam/users/"+a[0]+"/policies/"+a[1], nil, nil)
	})
	sub(i, "policies", "List policies", cobra.NoArgs, func([]string) error {
		return call("GET", "/api/v1/iam/policies", nil, cols("NAME=name", "MANAGED=managed", "ATTACHED=attachment_count", "DESCRIPTION=description"))
	})
	var doc, pdesc string
	cp := sub(i, "create-policy NAME", "Create a policy from a JSON document (--document '{...}' or @file.json)", cobra.ExactArgs(1), func(a []string) error {
		d, err := jsonArg(doc)
		if err != nil {
			return err
		}
		return call("POST", "/api/v1/iam/policies", map[string]any{"name": a[0], "description": pdesc, "document": d}, nil)
	})
	cp.Flags().StringVar(&doc, "document", "", "policy JSON or @file")
	cp.Flags().StringVar(&pdesc, "description", "", "description")
	sub(i, "groups", "List groups", cobra.NoArgs, func([]string) error {
		return call("GET", "/api/v1/iam/groups", nil, cols("NAME=name", "MEMBERS=members", "POLICIES=attached_policies"))
	})
	var res string
	sim := sub(i, "simulate USER ACTION...", "Check whether a user may perform actions", cobra.MinimumNArgs(2), func(a []string) error {
		return call("POST", "/api/v1/iam/simulate", map[string]any{"user": a[0], "actions": a[1:], "resource": res}, cols("ACTION=action", "RESOURCE=resource", "DECISION=decision"))
	})
	sim.Flags().StringVar(&res, "resource", "*", "resource ARN")
}

func secretsCommands() {
	s := group("secrets", "Secrets Manager")
	sub(s, "ls", "List secrets", cobra.NoArgs, func([]string) error {
		return call("GET", "/api/v1/secrets", nil, cols("NAME=name", "DESCRIPTION=description", "MANAGED BY=managed_by", "UPDATED=updated_at", "DELETION=deletion_date"))
	})
	var desc string
	c := sub(s, "create NAME VALUE", "Create a secret", cobra.ExactArgs(2), func(a []string) error {
		return call("POST", "/api/v1/secrets", map[string]any{"name": a[0], "value": a[1], "description": desc}, nil)
	})
	c.Flags().StringVar(&desc, "description", "", "description")
	sub(s, "get NAME", "Print a secret's current value", cobra.ExactArgs(1), func(a []string) error {
		var out map[string]any
		if err := api().Do("GET", "/api/v1/secrets/"+esc(a[0])+"/value", nil, &out); err != nil {
			return err
		}
		if output == "json" {
			printJSON(out)
		} else {
			fmt.Println(out["value"])
		}
		return nil
	})
	sub(s, "put NAME VALUE", "Store a new version of a secret", cobra.ExactArgs(2), func(a []string) error {
		return call("PUT", "/api/v1/secrets/"+esc(a[0])+"/value", map[string]any{"value": a[1]}, nil)
	})
	var force bool
	d := sub(s, "delete NAME", "Schedule a secret for deletion (7-day recovery window)", cobra.ExactArgs(1), func(a []string) error {
		q := ""
		if force {
			q = "?force=true"
		}
		return call("DELETE", "/api/v1/secrets/"+esc(a[0])+q, nil, nil)
	})
	d.Flags().BoolVar(&force, "force", false, "delete immediately without recovery")
}

func cloudwatchCommands() {
	l := group("logs", "CloudWatch Logs")
	sub(l, "groups", "List log groups", cobra.NoArgs, func([]string) error {
		return call("GET", "/api/v1/logs/groups", nil, cols("NAME=name", "SOURCE=source", "RETENTION DAYS=retention_days", "BYTES=stored_bytes"))
	})
	var filter, since string
	var follow bool
	t := sub(l, "tail GROUP", "Print (and optionally follow) events from a log group, e.g. /aws/lambda/fn or /hc/ec2/i-123", cobra.ExactArgs(1), func(a []string) error {
		c := api()
		start := time.Now().Add(-time.Hour)
		if since != "" {
			d, err := time.ParseDuration(since)
			if err != nil {
				return err
			}
			start = time.Now().Add(-d)
		}
		seen := map[string]bool{}
		for {
			var evs []map[string]any
			q := fmt.Sprintf("?start=%d&filter=%s&limit=1000", start.UnixMilli(), url.QueryEscape(filter))
			if err := c.Do("GET", "/api/v1/logs/groups/"+esc(a[0])+"/events"+q, nil, &evs); err != nil {
				return err
			}
			for _, e := range evs {
				k := fmt.Sprint(e["timestamp"], e["stream"], e["message"])
				if seen[k] {
					continue
				}
				seen[k] = true
				fmt.Printf("%s  %s\n", format(e["timestamp"]), e["message"])
			}
			if !follow {
				return nil
			}
			time.Sleep(2 * time.Second)
		}
	})
	t.Flags().StringVar(&filter, "filter", "", "only events containing this text")
	t.Flags().StringVar(&since, "since", "1h", "how far back to start")
	t.Flags().BoolVarP(&follow, "follow", "f", false, "keep polling for new events")

	cw := group("cloudwatch", "Metrics and alarms")
	var ns string
	m := sub(cw, "metrics", "List metrics", cobra.NoArgs, func([]string) error {
		q := ""
		if ns != "" {
			q = "?namespace=" + url.QueryEscape(ns)
		}
		return call("GET", "/api/v1/cloudwatch/metrics"+q, nil, cols("NAMESPACE=namespace", "METRIC=name", "DIMENSIONS=dimensions", "UNIT=unit"))
	})
	m.Flags().StringVar(&ns, "namespace", "", "filter by namespace, e.g. HC/EC2")
	var dims []string
	var stat string
	var period int
	st := sub(cw, "stats NAMESPACE METRIC", "Show a metric's statistics for the last hour", cobra.ExactArgs(2), func(a []string) error {
		q := map[string]any{"namespace": a[0], "name": a[1], "dimensions": tagsFlag(dims), "period": period, "stat": stat}
		var out []map[string]any
		if err := api().Do("POST", "/api/v1/cloudwatch/metrics/query", map[string]any{"queries": []any{q}}, &out); err != nil {
			return err
		}
		if len(out) == 0 {
			return nil
		}
		printList(out[0]["datapoints"], cols("TIME=timestamp", "AVERAGE=average", "MIN=minimum", "MAX=maximum", "SUM=sum", "SAMPLES=sample_count"))
		return nil
	})
	st.Flags().StringSliceVar(&dims, "dim", nil, "dimension key=value, e.g. InstanceId=i-123")
	st.Flags().StringVar(&stat, "stat", "Average", "statistic")
	st.Flags().IntVar(&period, "period", 60, "period in seconds")
	sub(cw, "alarms", "List alarms", cobra.NoArgs, func([]string) error {
		return call("GET", "/api/v1/cloudwatch/alarms", nil, cols("NAME=name", "STATE=state", "METRIC=metric", "THRESHOLD=threshold", "REASON=state_reason"))
	})

	ct := group("cloudtrail", "Audit log of API activity")
	var user string
	var errs bool
	ev := sub(ct, "events", "Show recent API activity", cobra.NoArgs, func([]string) error {
		q := url.Values{"user": {user}}
		if errs {
			q.Set("errors", "true")
		}
		return call("GET", "/api/v1/cloudtrail/events?"+q.Encode(), nil, cols("TIME=time", "USER=user", "ACTION=action", "RESOURCE=resource", "STATUS=status", "SOURCE IP=source_ip"))
	})
	ev.Flags().StringVar(&user, "user", "", "only this user")
	ev.Flags().BoolVar(&errs, "errors", false, "only failed calls")
}

var dbCols = cols("ID=id", "ENGINE=engine", "VERSION=engine_version", "CLASS=class", "STATUS=status", "ENDPOINT=endpoint.address", "PUBLIC PORT=endpoint.public_port")

func rdsCommands() {
	r := group("rds", "Managed databases (PostgreSQL, MySQL, MariaDB, MongoDB) and caches (Redis, Valkey, Memcached)")
	sub(r, "ls", "List database instances", cobra.NoArgs, func([]string) error { return call("GET", "/api/v1/rds/instances", nil, dbCols) })
	sub(r, "engines", "List engines, versions and instance classes", cobra.NoArgs, func([]string) error {
		var out map[string]any
		if err := api().Do("GET", "/api/v1/rds/engines", nil, &out); err != nil {
			return err
		}
		printList(out["engines"], cols("ENGINE=name", "KIND=kind", "VERSIONS=versions", "PORT=default_port"))
		fmt.Println()
		printList(out["classes"], cols("CLASS=name", "VCPUS=vcpus", "MEMORY MB=memory_mb"))
		return nil
	})
	var in struct {
		Engine, Version, Class, User, Password, DB string
		Public                                     bool
		Port, Retention                            int
	}
	c := sub(r, "create ID", "Create a database instance", cobra.ExactArgs(1), func(a []string) error {
		return call("POST", "/api/v1/rds/instances", map[string]any{"id": a[0], "engine": in.Engine, "engine_version": in.Version, "class": in.Class,
			"master_username": in.User, "master_password": in.Password, "db_name": in.DB, "publicly_accessible": in.Public, "port": in.Port,
			"backup_retention_days": in.Retention}, nil)
	})
	f := c.Flags()
	f.StringVar(&in.Engine, "engine", "postgres", "postgres, mysql, mariadb, mongodb, redis, valkey or memcached")
	f.StringVar(&in.Version, "version", "", "engine version (latest when empty)")
	f.StringVar(&in.Class, "class", "", "instance class, e.g. db.t3.small")
	f.StringVar(&in.User, "user", "", "master user name")
	f.StringVar(&in.Password, "password", "", "master password (generated and stored in Secrets Manager when empty)")
	f.StringVar(&in.DB, "db", "", "initial database name")
	f.BoolVar(&in.Public, "public", false, "publish the port on the host")
	f.IntVar(&in.Port, "port", 0, "fixed host port when --public")
	f.IntVar(&in.Retention, "backup-retention", 1, "days to keep daily automated backups (0 disables)")
	sub(r, "describe ID", "Show a database instance", cobra.ExactArgs(1), func(a []string) error { return call("GET", "/api/v1/rds/instances/"+a[0], nil, nil) })
	for _, act := range []string{"start", "stop", "reboot"} {
		act := act
		sub(r, act+" ID", strings.ToUpper(act[:1])+act[1:]+" a database instance", cobra.ExactArgs(1), func(a []string) error {
			return call("POST", "/api/v1/rds/instances/"+a[0]+"/"+act, nil, nil)
		})
	}
	var final bool
	d := sub(r, "delete ID", "Delete a database instance", cobra.ExactArgs(1), func(a []string) error {
		return call("DELETE", fmt.Sprintf("/api/v1/rds/instances/%s?final_snapshot=%v", a[0], final), nil, nil)
	})
	d.Flags().BoolVar(&final, "final-snapshot", false, "take a snapshot before deleting")
	var db string
	q := sub(r, "query ID SQL", "Run a statement (SQL, Redis command or MongoDB shell expression)", cobra.ExactArgs(2), func(a []string) error {
		var out map[string]any
		if err := api().Do("POST", "/api/v1/rds/instances/"+a[0]+"/query", map[string]any{"sql": a[1], "database": db}, &out); err != nil {
			return err
		}
		if output == "json" {
			printJSON(out)
			return nil
		}
		fmt.Print(out["output"])
		if e, _ := out["error"].(string); e != "" {
			fmt.Fprintln(os.Stderr, e)
		}
		return nil
	})
	q.Flags().StringVar(&db, "database", "", "database (defaults to the instance's)")
	sub(r, "password ID", "Print the master credentials", cobra.ExactArgs(1), func(a []string) error {
		var out map[string]any
		if err := api().Do("GET", "/api/v1/secrets/"+esc("rds!"+a[0])+"/value", nil, &out); err != nil {
			return err
		}
		fmt.Println(out["value"])
		return nil
	})
	var snapID string
	sn := sub(r, "snapshot ID", "Take a manual snapshot", cobra.ExactArgs(1), func(a []string) error {
		return call("POST", "/api/v1/rds/instances/"+a[0]+"/snapshots", map[string]any{"id": snapID}, nil)
	})
	sn.Flags().StringVar(&snapID, "name", "", "snapshot id")
	sub(r, "snapshots", "List snapshots", cobra.NoArgs, func([]string) error {
		return call("GET", "/api/v1/rds/snapshots", nil, cols("ID=id", "SOURCE=source_instance", "ENGINE=engine", "TYPE=type", "STATUS=status", "BYTES=size_bytes", "CREATED=created_at"))
	})
	sub(r, "restore SNAPSHOT NEW_ID", "Create a new instance from a snapshot", cobra.ExactArgs(2), func(a []string) error {
		return call("POST", "/api/v1/rds/snapshots/"+a[0]+"/restore", map[string]any{"id": a[1]}, nil)
	})
}

// zipDir packages a directory (or single file) as a base64 zip for Lambda.
func zipDir(path string) (string, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	st, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	root := path
	if !st.IsDir() {
		root = filepath.Dir(path)
	}
	err = filepath.Walk(path, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		if strings.HasPrefix(rel, ".git") || strings.Contains(rel, "__pycache__") {
			return nil
		}
		w, err := zw.Create(filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(w, f)
		return err
	})
	if err != nil {
		return "", err
	}
	if err := zw.Close(); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}

func lambdaCommands() {
	l := group("lambda", "Serverless functions and API Gateway")
	lambdaFunctionCommands(l)

	g := group("apigateway", "HTTP APIs that route requests to functions")
	sub(g, "ls", "List APIs", cobra.NoArgs, func([]string) error {
		return call("GET", "/api/v1/apigateway/apis", nil, cols("ID=id", "NAME=name", "ENDPOINT=endpoint", "CREATED=created_at"))
	})
	var cors bool
	ca := sub(g, "create NAME", "Create an API", cobra.ExactArgs(1), func(a []string) error {
		return call("POST", "/api/v1/apigateway/apis", map[string]any{"name": a[0], "cors": cors}, nil)
	})
	ca.Flags().BoolVar(&cors, "cors", false, "answer CORS preflight requests")
	var routeAuth string
	rt := sub(g, "route API_ID \"METHOD /path/{param}\" FUNCTION", "Add a route", cobra.ExactArgs(3), func(a []string) error {
		m, p, ok := strings.Cut(a[1], " ")
		if !ok {
			return fmt.Errorf("route must look like \"GET /items/{id}\"")
		}
		return call("POST", "/api/v1/apigateway/apis/"+a[0]+"/routes", map[string]any{"method": m, "path": p, "function_name": a[2], "authorization": routeAuth}, nil)
	})
	rt.Flags().StringVar(&routeAuth, "auth", "NONE", "NONE, or JWT to require a token from the API's Cognito authorizer")
	var authz string
	ra := sub(g, "route-auth API_ID ROUTE_ID", "Change a route's authorization (NONE or JWT)", cobra.ExactArgs(2), func(a []string) error {
		return call("PATCH", "/api/v1/apigateway/apis/"+a[0]+"/routes/"+a[1], map[string]any{"authorization": authz}, nil)
	})
	ra.Flags().StringVar(&authz, "auth", "JWT", "NONE or JWT")
	var pool, client string
	au := sub(g, "authorizer API_ID", "Set the API's Cognito authorizer (empty --pool removes it)", cobra.ExactArgs(1), func(a []string) error {
		return call("PATCH", "/api/v1/apigateway/apis/"+a[0], map[string]any{"authorizer": map[string]any{"user_pool_id": pool, "audience": client}}, nil)
	})
	au.Flags().StringVar(&pool, "pool", "", "user pool ID")
	au.Flags().StringVar(&client, "client", "", "app client ID (default: any client of the pool)")
}

func queueCommands() {
	q := group("sqs", "Message queues")
	sub(q, "ls", "List queues", cobra.NoArgs, func([]string) error {
		return call("GET", "/api/v1/sqs/queues", nil, cols("NAME=name", "FIFO=fifo", "AVAILABLE=approximate_number_of_messages", "IN FLIGHT=approximate_number_of_messages_not_visible", "DLQ=redrive_policy.dead_letter_queue"))
	})
	var fifo bool
	var dlq string
	var maxRecv, vis int
	c := sub(q, "create NAME", "Create a queue (FIFO names end in .fifo)", cobra.ExactArgs(1), func(a []string) error {
		b := map[string]any{"name": a[0], "fifo": fifo || strings.HasSuffix(a[0], ".fifo"), "content_based_deduplication": fifo || strings.HasSuffix(a[0], ".fifo")}
		if vis > 0 {
			b["visibility_timeout"] = vis
		}
		if dlq != "" {
			b["redrive_policy"] = map[string]any{"dead_letter_queue": dlq, "max_receive_count": maxRecv}
		}
		return call("POST", "/api/v1/sqs/queues", b, nil)
	})
	c.Flags().BoolVar(&fifo, "fifo", false, "FIFO queue with content-based deduplication")
	c.Flags().StringVar(&dlq, "dlq", "", "dead-letter queue name")
	c.Flags().IntVar(&maxRecv, "max-receives", 5, "receives before a message moves to the DLQ")
	c.Flags().IntVar(&vis, "visibility", 0, "visibility timeout in seconds")
	var group_ string
	s := sub(q, "send QUEUE BODY", "Send a message", cobra.ExactArgs(2), func(a []string) error {
		return call("POST", "/api/v1/sqs/queues/"+a[0]+"/messages", map[string]any{"body": a[1], "group_id": group_}, nil)
	})
	s.Flags().StringVar(&group_, "group", "", "message group (FIFO queues)")
	var max, wait int
	var del bool
	r := sub(q, "receive QUEUE", "Receive messages", cobra.ExactArgs(1), func(a []string) error {
		var out []map[string]any
		c := api()
		if err := c.Do("POST", "/api/v1/sqs/queues/"+a[0]+"/messages/receive", map[string]any{"max_messages": max, "wait_seconds": wait}, &out); err != nil {
			return err
		}
		items := make([]any, len(out))
		for i := range out {
			items[i] = out[i]
			if del {
				if err := c.Do("POST", "/api/v1/sqs/queues/"+a[0]+"/messages/delete", map[string]any{"receipt_handle": out[i]["receipt_handle"]}, nil); err != nil {
					return err
				}
			}
		}
		printList(items, cols("MESSAGE ID=message_id", "BODY=body", "RECEIVES=attributes.ApproximateReceiveCount"))
		return nil
	})
	r.Flags().IntVar(&max, "max", 1, "messages to receive (1-10)")
	r.Flags().IntVar(&wait, "wait", 0, "long-poll seconds (0-20)")
	r.Flags().BoolVar(&del, "delete", false, "delete messages after receiving")
	sub(q, "purge QUEUE", "Delete all messages", cobra.ExactArgs(1), func(a []string) error { return call("POST", "/api/v1/sqs/queues/"+a[0]+"/purge", nil, nil) })
	sub(q, "delete QUEUE", "Delete a queue", cobra.ExactArgs(1), func(a []string) error { return call("DELETE", "/api/v1/sqs/queues/"+a[0], nil, nil) })

	n := group("sns", "Pub/sub topics")
	sub(n, "ls", "List topics", cobra.NoArgs, func([]string) error {
		return call("GET", "/api/v1/sns/topics", nil, cols("NAME=name", "ARN=arn", "SUBSCRIPTIONS=subscriptions", "PUBLISHED=messages_published"))
	})
	sub(n, "create NAME", "Create a topic", cobra.ExactArgs(1), func(a []string) error { return call("POST", "/api/v1/sns/topics", map[string]any{"name": a[0]}, nil) })
	var raw bool
	sb := sub(n, "subscribe TOPIC PROTOCOL ENDPOINT", "Subscribe a queue, function or URL (protocol: sqs, lambda, http, https)", cobra.ExactArgs(3), func(a []string) error {
		return call("POST", "/api/v1/sns/topics/"+a[0]+"/subscriptions", map[string]any{"protocol": a[1], "endpoint": a[2], "raw_message_delivery": raw}, nil)
	})
	sb.Flags().BoolVar(&raw, "raw", false, "deliver the raw message instead of the JSON envelope")
	var subject string
	p := sub(n, "publish TOPIC MESSAGE", "Publish a message", cobra.ExactArgs(2), func(a []string) error {
		return call("POST", "/api/v1/sns/topics/"+a[0]+"/publish", map[string]any{"message": a[1], "subject": subject}, nil)
	})
	p.Flags().StringVar(&subject, "subject", "", "subject")
}

func dynamoCommands() {
	d := group("dynamodb", "Key-value and document tables")
	sub(d, "ls", "List tables", cobra.NoArgs, func([]string) error {
		return call("GET", "/api/v1/dynamodb/tables", nil, cols("NAME=name", "PARTITION KEY=partition_key.name", "SORT KEY=sort_key.name", "ITEMS=item_count", "BYTES=size_bytes"))
	})
	var pk, sk string
	c := sub(d, "create NAME", "Create a table", cobra.ExactArgs(1), func(a []string) error {
		key := func(s string) map[string]string {
			n, t, ok := strings.Cut(s, ":")
			if !ok {
				t = "S"
			}
			return map[string]string{"name": n, "type": t}
		}
		b := map[string]any{"name": a[0], "partition_key": key(pk)}
		if sk != "" {
			b["sort_key"] = key(sk)
		}
		return call("POST", "/api/v1/dynamodb/tables", b, nil)
	})
	c.Flags().StringVar(&pk, "pk", "id", "partition key as name[:S|N|B]")
	c.Flags().StringVar(&sk, "sk", "", "sort key as name[:S|N|B]")
	sub(d, "put TABLE ITEM_JSON", "Put an item", cobra.ExactArgs(2), func(a []string) error {
		it, err := jsonArg(a[1])
		if err != nil {
			return err
		}
		return call("POST", "/api/v1/dynamodb/tables/"+a[0]+"/items", map[string]any{"item": it}, nil)
	})
	sub(d, "get TABLE KEY_JSON", "Get an item", cobra.ExactArgs(2), func(a []string) error {
		k, err := jsonArg(a[1])
		if err != nil {
			return err
		}
		var out map[string]any
		if err := api().Do("POST", "/api/v1/dynamodb/tables/"+a[0]+"/items/get", map[string]any{"key": k}, &out); err != nil {
			return err
		}
		printJSON(out["item"])
		return nil
	})
	sub(d, "delete-item TABLE KEY_JSON", "Delete an item", cobra.ExactArgs(2), func(a []string) error {
		k, err := jsonArg(a[1])
		if err != nil {
			return err
		}
		return call("POST", "/api/v1/dynamodb/tables/"+a[0]+"/items/delete", map[string]any{"key": k}, nil)
	})
	sub(d, "query TABLE PARTITION_VALUE", "Query items in a partition", cobra.ExactArgs(2), func(a []string) error {
		var pv any = a[1]
		var n float64
		if _, err := fmt.Sscan(a[1], &n); err == nil && fmt.Sprint(n) == a[1] {
			pv = n
		}
		var out map[string]any
		if err := api().Do("POST", "/api/v1/dynamodb/tables/"+a[0]+"/query", map[string]any{"partition_value": pv}, &out); err != nil {
			return err
		}
		printJSON(out["items"])
		return nil
	})
	sub(d, "scan TABLE", "Scan all items", cobra.ExactArgs(1), func(a []string) error {
		var out map[string]any
		if err := api().Do("POST", "/api/v1/dynamodb/tables/"+a[0]+"/scan", map[string]any{}, &out); err != nil {
			return err
		}
		printJSON(out["items"])
		return nil
	})
	sub(d, "drop TABLE", "Delete a table", cobra.ExactArgs(1), func(a []string) error { return call("DELETE", "/api/v1/dynamodb/tables/"+a[0], nil, nil) })
}

func eventsCommands() {
	e := group("events", "EventBridge rules and schedules")
	sub(e, "rules", "List rules", cobra.NoArgs, func([]string) error {
		return call("GET", "/api/v1/events/rules", nil, cols("NAME=name", "STATE=state", "SCHEDULE=schedule_expression", "INVOCATIONS=invocations", "LAST=last_triggered", "NEXT=next_run"))
	})
	var targets []string
	var pattern, input string
	s := sub(e, "schedule NAME EXPRESSION", "Create a scheduled rule, e.g. 'rate(5 minutes)' or 'cron(0 9 ? * MON-FRI *)'", cobra.ExactArgs(2), func(a []string) error {
		ts := []map[string]string{}
		for _, t := range targets {
			ts = append(ts, map[string]string{"arn": t, "input": input})
		}
		return call("PUT", "/api/v1/events/rules/"+a[0], map[string]any{"schedule_expression": a[1], "targets": ts}, nil)
	})
	s.Flags().StringSliceVar(&targets, "target", nil, "target ARN (Lambda function, SQS queue or SNS topic)")
	s.Flags().StringVar(&input, "input", "", "constant JSON input for targets")
	r := sub(e, "rule NAME", "Create a rule that matches published events (--pattern JSON)", cobra.ExactArgs(1), func(a []string) error {
		p, err := jsonArg(pattern)
		if err != nil {
			return err
		}
		ts := []map[string]string{}
		for _, t := range targets {
			ts = append(ts, map[string]string{"arn": t})
		}
		return call("PUT", "/api/v1/events/rules/"+a[0], map[string]any{"event_pattern": p, "targets": ts}, nil)
	})
	r.Flags().StringVar(&pattern, "pattern", "", `event pattern JSON, e.g. '{"source":["shop"]}'`)
	r.Flags().StringSliceVar(&targets, "target", nil, "target ARN")
	var detail string
	p := sub(e, "put SOURCE DETAIL_TYPE", "Publish an event", cobra.ExactArgs(2), func(a []string) error {
		d, err := jsonArg(detail)
		if err != nil {
			return err
		}
		return call("POST", "/api/v1/events/events", map[string]any{"entries": []any{map[string]any{"source": a[0], "detail_type": a[1], "detail": d}}}, nil)
	})
	p.Flags().StringVar(&detail, "detail", "{}", "event detail JSON")
	sub(e, "delete NAME", "Delete a rule", cobra.ExactArgs(1), func(a []string) error { return call("DELETE", "/api/v1/events/rules/"+a[0], nil, nil) })
}
