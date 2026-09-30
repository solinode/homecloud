package ec2_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/svc/ecs"
	"github.com/homecloudhq/homecloud/cli/internal/svc/elb"
	"github.com/homecloudhq/homecloud/cli/internal/svc/rds"
)

// Security groups on the other resources that have them: RDS, ElastiCache,
// ECS tasks and load balancers, tested against instances as the clients.

// tcp reports whether from can open a TCP connection to host:port right now.
func (f *filterEnv) tcp(from node, host string, port int) bool {
	cid := f.container(from)
	if cid == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	res, err := f.h.Env.Docker.Exec(ctx, cid, []string{"sh", "-c", fmt.Sprintf("nc -w 2 %s %d </dev/null", host, port)}, nil)
	return err == nil && res.ExitCode == 0
}

func (f *filterEnv) eventuallyTCP(t *testing.T, want bool, from node, host string, port int, what string) {
	t.Helper()
	end := time.Now().Add(90 * time.Second)
	for time.Now().Before(end) {
		if f.tcp(from, host, port) == want {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("%s: %s -> %s:%d reachable = %v, want %v", what, from.id, host, port, !want, want)
}

func (f *filterEnv) steadyTCP(t *testing.T, want bool, from node, host string, port int, what string) {
	t.Helper()
	for i := 0; i < 2; i++ {
		if got := f.tcp(from, host, port); got != want {
			t.Fatalf("%s: %s -> %s:%d reachable = %v, want %v", what, from.id, host, port, got, want)
		}
	}
}

func TestSecurityGroupsProtectDatabasesAndCaches(t *testing.T) {
	f := newFilterEnv(t)
	h := f.h
	rds.New(h.Env, f.vpc, h.Secrets).RegisterAWS()
	sgApp, sgDB, sgCache, sgOther := f.group(t, "app"), f.group(t, "db"), f.group(t, "cache"), f.group(t, "other")
	app, other := f.launch(t, sgApp), f.launch(t, sgOther)

	h.AWS(t, "rds", "create-db-subnet-group", "--db-subnet-group-name", "grp", "--db-subnet-group-description", "d", "--subnet-ids", f.subnet)
	h.AWS(t, "rds", "create-db-instance", "--db-instance-identifier", "sgdb", "--db-instance-class", "db.t3.micro", "--engine", "postgres",
		"--engine-version", "17", "--allocated-storage", "20", "--master-username", "app", "--master-user-password", "password123",
		"--backup-retention-period", "0", "--db-subnet-group-name", "grp", "--vpc-security-group-ids", sgDB)
	t.Cleanup(func() {
		_, _ = h.AWSErr(t, "rds", "delete-db-instance", "--db-instance-identifier", "sgdb", "--skip-final-snapshot")
		_, _ = h.AWSErr(t, "elasticache", "delete-cache-cluster", "--cache-cluster-id", "sgcache")
	})
	h.AWS(t, "elasticache", "create-cache-subnet-group", "--cache-subnet-group-name", "cgrp", "--cache-subnet-group-description", "d", "--subnet-ids", f.subnet)
	h.AWS(t, "elasticache", "create-cache-cluster", "--cache-cluster-id", "sgcache", "--engine", "redis", "--cache-node-type", "cache.t3.micro",
		"--num-cache-nodes", "1", "--cache-subnet-group-name", "cgrp", "--security-group-ids", sgCache)

	var dbHost, cacheHost string
	waitFor(t, "database available", 5*time.Minute, func() bool {
		d := h.AWSJSON(t, "rds", "describe-db-instances", "--db-instance-identifier", "sgdb")["DBInstances"].([]any)[0].(map[string]any)
		if d["DBInstanceStatus"] == "available" {
			dbHost = d["Endpoint"].(map[string]any)["Address"].(string)
		}
		return dbHost != ""
	})
	waitFor(t, "cache available", 5*time.Minute, func() bool {
		d := h.AWSJSON(t, "elasticache", "describe-cache-clusters", "--cache-cluster-id", "sgcache", "--show-cache-node-info")["CacheClusters"].([]any)[0].(map[string]any)
		if d["CacheClusterStatus"] == "available" {
			if n, ok := d["CacheNodes"].([]any); ok && len(n) > 0 {
				cacheHost = n[0].(map[string]any)["Endpoint"].(map[string]any)["Address"].(string)
			}
		}
		return cacheHost != ""
	})

	// Default deny for everyone.
	f.eventuallyTCP(t, false, app, dbHost, 5432, "database without rules")
	f.eventuallyTCP(t, false, app, cacheHost, 6379, "cache without rules")

	// RDS allows postgres from the app group only.
	h.AWS(t, "ec2", "authorize-security-group-ingress", "--group-id", sgDB, "--ip-permissions",
		fmt.Sprintf("IpProtocol=tcp,FromPort=5432,ToPort=5432,UserIdGroupPairs=[{GroupId=%s}]", sgApp))
	f.eventuallyTCP(t, true, app, dbHost, 5432, "database from the app group")
	f.steadyTCP(t, false, other, dbHost, 5432, "database from another group")
	f.steadyTCP(t, false, app, cacheHost, 6379, "the cache is a different group")

	// ElastiCache likewise.
	h.AWS(t, "ec2", "authorize-security-group-ingress", "--group-id", sgCache, "--ip-permissions",
		fmt.Sprintf("IpProtocol=tcp,FromPort=6379,ToPort=6379,UserIdGroupPairs=[{GroupId=%s}]", sgApp))
	f.eventuallyTCP(t, true, app, cacheHost, 6379, "cache from the app group")
	f.steadyTCP(t, false, other, cacheHost, 6379, "cache from another group")

	// Changing the database's groups takes effect too.
	h.AWS(t, "rds", "modify-db-instance", "--db-instance-identifier", "sgdb", "--vpc-security-group-ids", sgOther, "--apply-immediately")
	f.eventuallyTCP(t, false, app, dbHost, 5432, "database moved to another group")

	// The database's restart keeps the filter (fresh network namespace).
	h.AWS(t, "rds", "modify-db-instance", "--db-instance-identifier", "sgdb", "--vpc-security-group-ids", sgDB, "--apply-immediately")
	f.eventuallyTCP(t, true, app, dbHost, 5432, "database back in its group")
	h.AWS(t, "rds", "reboot-db-instance", "--db-instance-identifier", "sgdb")
	waitFor(t, "database rebooted", 5*time.Minute, func() bool {
		d := h.AWSJSON(t, "rds", "describe-db-instances", "--db-instance-identifier", "sgdb")["DBInstances"].([]any)[0].(map[string]any)
		return d["DBInstanceStatus"] == "available"
	})
	f.eventuallyTCP(t, true, app, dbHost, 5432, "after reboot, app allowed")
	f.eventuallyTCP(t, false, other, dbHost, 5432, "after reboot, others denied")
}

func TestSecurityGroupsProtectECSTasks(t *testing.T) {
	f := newFilterEnv(t)
	h := f.h
	e := ecs.New(h.Env, f.vpc, nil, h.Secrets)
	e.Routes(h.Router)
	sgApp, sgTask, sgOther := f.group(t, "app"), f.group(t, "task"), f.group(t, "other")
	app, other := f.launch(t, sgApp), f.launch(t, sgOther)

	h.Native(t, "POST", "/api/v1/ecs/task-definitions", map[string]any{"family": "sgweb", "image": "nginx:alpine", "container_port": 80})
	var task struct {
		ID          string `json:"id"`
		PrivateIP   string `json:"private_ip"`
		ContainerID string `json:"container_id"`
	}
	if err := json.Unmarshal(h.Native(t, "POST", "/api/v1/ecs/tasks", map[string]any{"task_definition": "sgweb", "subnet_id": f.subnet, "security_groups": []string{sgTask}}), &task); err != nil || task.PrivateIP == "" {
		t.Fatalf("run task: %v %+v", err, task)
	}
	t.Cleanup(func() {
		_, _ = h.NativeAs(t, h.AccessKeyID, h.SecretKey, "POST", "/api/v1/ecs/tasks/"+task.ID+"/stop", nil)
	})

	f.eventuallyTCP(t, false, app, task.PrivateIP, 80, "task without rules")
	h.AWS(t, "ec2", "authorize-security-group-ingress", "--group-id", sgTask, "--ip-permissions",
		fmt.Sprintf("IpProtocol=tcp,FromPort=80,ToPort=80,UserIdGroupPairs=[{GroupId=%s}]", sgApp))
	f.eventuallyTCP(t, true, app, task.PrivateIP, 80, "task from the app group")
	f.steadyTCP(t, false, other, task.PrivateIP, 80, "task from another group")
}

func TestLoadBalancerToTargetObeysTargetSecurityGroup(t *testing.T) {
	f := newFilterEnv(t)
	h := f.h
	lbSvc := elb.New(h.Env, f.vpc)
	lbSvc.RegisterAWS()
	lbSvc.Routes(h.Router)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { lbSvc.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	sn2 := h.AWSJSON(t, "ec2", "create-subnet", "--vpc-id", f.vpcID, "--cidr-block", strings.Replace(f.cidr(t), ".0.0/16", ".2.0/24", 1), "--availability-zone", "us-east-1b")["Subnet"].(map[string]any)["SubnetId"].(string)
	sgClient, sgLB, sgTarget, sgOther := f.group(t, "client"), f.group(t, "alb"), f.group(t, "target"), f.group(t, "other")
	client, other := f.launch(t, sgClient), f.launch(t, sgOther)
	target := f.launch(t, sgTarget)
	lbSvc.Resolve = func(id string) (string, string, bool) {
		if id == target.id {
			return target.ip, f.vpcID, true
		}
		return "", "", false
	}

	h.AWS(t, "ec2", "authorize-security-group-ingress", "--group-id", sgLB, "--ip-permissions",
		fmt.Sprintf("IpProtocol=tcp,FromPort=80,ToPort=80,UserIdGroupPairs=[{GroupId=%s}]", sgClient))
	tg := h.AWSJSON(t, "elbv2", "create-target-group", "--name", "sgtg", "--protocol", "HTTP", "--port", "80", "--vpc-id", f.vpcID, "--target-type", "instance",
		"--health-check-interval-seconds", "5", "--healthy-threshold-count", "2", "--unhealthy-threshold-count", "2")["TargetGroups"].([]any)[0].(map[string]any)["TargetGroupArn"].(string)
	h.AWS(t, "elbv2", "register-targets", "--target-group-arn", tg, "--targets", "Id="+target.id)
	lb := h.AWSJSON(t, "elbv2", "create-load-balancer", "--name", "sgalb", "--scheme", "internal", "--subnets", f.subnet, sn2, "--security-groups", sgLB)["LoadBalancers"].([]any)[0].(map[string]any)
	lbArn, lbDNS := lb["LoadBalancerArn"].(string), lb["DNSName"].(string)
	t.Cleanup(func() {
		_, _ = h.AWSErr(t, "elbv2", "delete-load-balancer", "--load-balancer-arn", lbArn)
		_, _ = h.AWSErr(t, "elbv2", "delete-target-group", "--target-group-arn", tg)
	})
	h.AWS(t, "elbv2", "create-listener", "--load-balancer-arn", lbArn, "--protocol", "HTTP", "--port", "80", "--default-actions", "Type=forward,TargetGroupArn="+tg)
	waitFor(t, "load balancer active", 3*time.Minute, func() bool {
		d := h.AWSJSON(t, "elbv2", "describe-load-balancers", "--load-balancer-arns", lbArn)["LoadBalancers"].([]any)[0].(map[string]any)
		return d["State"].(map[string]any)["Code"] == "active"
	})

	health := func() string {
		d := h.AWSJSON(t, "elbv2", "describe-target-health", "--target-group-arn", tg)["TargetHealthDescriptions"].([]any)
		if len(d) == 0 {
			return ""
		}
		return d[0].(map[string]any)["TargetHealth"].(map[string]any)["State"].(string)
	}
	// Through the balancer: the client sees the target's page only when the
	// target's own group lets the balancer's group in.
	viaLB := func() bool {
		cid := f.container(client)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		res, err := f.h.Env.Docker.Exec(ctx, cid, []string{"wget", "-q", "-T", "3", "-t", "1", "-O", "-", "http://" + lbDNS + "/"}, nil)
		return err == nil && res.ExitCode == 0 && strings.Contains(res.Stdout, "nginx")
	}
	waitFor(t, "target unhealthy without a rule", 2*time.Minute, func() bool { return health() == "unhealthy" })
	if viaLB() {
		t.Fatal("the target answered through the balancer although its group allows nothing")
	}

	h.AWS(t, "ec2", "authorize-security-group-ingress", "--group-id", sgTarget, "--ip-permissions",
		fmt.Sprintf("IpProtocol=tcp,FromPort=80,ToPort=80,UserIdGroupPairs=[{GroupId=%s}]", sgLB))
	waitFor(t, "target healthy once its group allows the balancer's", 2*time.Minute, func() bool { return health() == "healthy" })
	waitFor(t, "client reaches the target through the balancer", 30*time.Second, viaLB)

	// The balancer's own group filters its clients.
	if f.tcp(other, lbDNS, 80) {
		t.Fatal("a client outside the balancer's group reached it")
	}
	// The target itself is not open to the client directly.
	if f.reach(client, target, 80) {
		t.Fatal("the client reached the target directly")
	}

	h.AWS(t, "ec2", "revoke-security-group-ingress", "--group-id", sgTarget, "--ip-permissions",
		fmt.Sprintf("IpProtocol=tcp,FromPort=80,ToPort=80,UserIdGroupPairs=[{GroupId=%s}]", sgLB))
	waitFor(t, "target unhealthy after revoke", 2*time.Minute, func() bool { return health() == "unhealthy" })
}
