package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

func fnPath(name string, rest ...string) string {
	return "/api/v1/lambda/functions/" + url.PathEscape(name) + strings.Join(rest, "")
}

func qualifierQuery(q string) string {
	if q == "" {
		return ""
	}
	return "?qualifier=" + url.QueryEscape(q)
}

// lambdaFunctionCommands adds the function commands to the lambda group.
func lambdaFunctionCommands(l *cobra.Command) {
	sub(l, "ls", "List functions", cobra.NoArgs, func([]string) error {
		return call("GET", "/api/v1/lambda/functions", nil, cols("NAME=name", "RUNTIME=runtime", "PACKAGE=package_type", "MEMORY=memory_mb", "TIMEOUT=timeout_seconds", "STATE=state", "URL=function_url.url", "MODIFIED=last_modified"))
	})
	sub(l, "runtimes", "List supported runtimes", cobra.NoArgs, func([]string) error {
		return call("GET", "/api/v1/lambda/runtimes", nil, cols("NAME=name", "LABEL=label", "IMAGE=image", "HANDLER=default_handler"))
	})
	sub(l, "get NAME", "Show a function's configuration, environments and aliases", cobra.ExactArgs(1), func(a []string) error {
		return call("GET", fnPath(a[0]), nil, nil)
	})

	var in struct {
		Runtime, Handler, Code, Role, ImageURI, Arch, Subnet, DLQ string
		Memory, Timeout                                           int
		Env, Layers                                               []string
		Publish                                                   bool
	}
	body := func(cmd *cobra.Command) (map[string]any, error) {
		b := map[string]any{"memory_mb": in.Memory, "timeout_seconds": in.Timeout}
		if in.Handler != "" {
			b["handler"] = in.Handler
		}
		if in.Runtime != "" && in.ImageURI == "" {
			b["runtime"] = in.Runtime
		}
		if len(in.Env) > 0 {
			b["environment"] = tagsFlag(in.Env)
		}
		if cmd.Flags().Changed("role") {
			b["role"] = in.Role
		}
		if cmd.Flags().Changed("layers") {
			b["layers"] = in.Layers
		}
		if in.Arch != "" {
			b["architectures"] = []string{in.Arch}
		}
		if cmd.Flags().Changed("subnet") {
			b["subnet_id"] = in.Subnet
		}
		if cmd.Flags().Changed("dead-letter-target") {
			b["dead_letter_target"] = in.DLQ
		}
		return b, nil
	}
	configFlags := func(c *cobra.Command) {
		f := c.Flags()
		f.StringVar(&in.Handler, "handler", "", "handler, e.g. file.function (runtime default when empty)")
		f.IntVar(&in.Memory, "memory", 0, "memory in MB (128-10240)")
		f.IntVar(&in.Timeout, "timeout", 0, "timeout in seconds (1-900)")
		f.StringSliceVar(&in.Env, "env", nil, "environment variables KEY=VALUE")
		f.StringVar(&in.Role, "role", "", "execution role (name or ARN) whose credentials the function receives")
		f.StringSliceVar(&in.Layers, "layers", nil, "layer version ARNs (up to 5), extracted into /opt")
		f.StringVar(&in.Subnet, "subnet", "", "subnet to run in (default VPC when empty)")
		f.StringVar(&in.DLQ, "dead-letter-target", "", "SQS queue or SNS topic ARN for failed asynchronous events")
	}
	c := sub(l, "create NAME", "Create a function (from --code DIR, --image-uri, or a hello-world template)", cobra.ExactArgs(1), nil)
	c.RunE = func(cmd *cobra.Command, a []string) error {
		b, err := body(cmd)
		if err != nil {
			return err
		}
		b["name"] = a[0]
		if in.ImageURI != "" {
			b["package_type"], b["image_uri"] = "Image", in.ImageURI
		}
		if in.Code != "" {
			z, err := zipDir(in.Code)
			if err != nil {
				return err
			}
			b["code"] = map[string]string{"zip_base64": z}
		}
		b["publish"] = in.Publish
		return call("POST", "/api/v1/lambda/functions", b, nil)
	}
	f := c.Flags()
	f.StringVar(&in.Runtime, "runtime", "python3.12", "runtime (see `homecloud lambda runtimes`)")
	f.StringVar(&in.Code, "code", "", "directory or file with the function code")
	f.StringVar(&in.ImageURI, "image-uri", "", "container image (built FROM an AWS Lambda base image) instead of a code package")
	f.StringVar(&in.Arch, "arch", "", "x86_64 or arm64 (functions run on the host's architecture)")
	f.BoolVar(&in.Publish, "publish", false, "publish version 1 right away")
	configFlags(c)

	cfg := sub(l, "configure NAME", "Change a function's configuration", cobra.ExactArgs(1), nil)
	cfg.RunE = func(cmd *cobra.Command, a []string) error {
		b, err := body(cmd)
		if err != nil {
			return err
		}
		if cmd.Flags().Changed("runtime") {
			b["runtime"] = in.Runtime
		}
		return call("PATCH", fnPath(a[0]), b, nil)
	}
	cfg.Flags().StringVar(&in.Runtime, "runtime", "", "runtime")
	configFlags(cfg)

	var code, image string
	var publishCode bool
	u := sub(l, "deploy NAME", "Upload new code (or a new image) for a function", cobra.ExactArgs(1), func(a []string) error {
		b := map[string]any{"publish": publishCode}
		if image != "" {
			b["image_uri"] = image
		} else {
			z, err := zipDir(code)
			if err != nil {
				return err
			}
			b["zip_base64"] = z
		}
		return call("PUT", fnPath(a[0], "/code"), b, nil)
	})
	u.Flags().StringVar(&code, "code", ".", "directory or file with the function code")
	u.Flags().StringVar(&image, "image-uri", "", "new container image (image functions)")
	u.Flags().BoolVar(&publishCode, "publish", false, "publish a version after the update")

	var payload, qualifier string
	var logs, async bool
	iv := sub(l, "invoke NAME", "Invoke a function and print its result", cobra.ExactArgs(1), func(a []string) error {
		if payload == "" {
			payload = "{}"
		}
		q := url.Values{}
		if qualifier != "" {
			q.Set("qualifier", qualifier)
		}
		if async {
			q.Set("invocation_type", "Event")
		}
		path := fnPath(a[0], "/invoke")
		if len(q) > 0 {
			path += "?" + q.Encode()
		}
		var out map[string]any
		if err := api().Do("POST", path, bytes.NewReader([]byte(payload)), &out); err != nil {
			return err
		}
		if output == "json" || async {
			printJSON(out)
			return nil
		}
		if logs {
			fmt.Fprint(os.Stderr, out["logs"])
		}
		b, _ := json.MarshalIndent(out["payload"], "", "  ")
		fmt.Println(string(b))
		if out["function_error"] != nil && out["function_error"] != "" {
			return fmt.Errorf("function error (%v)", out["function_error"])
		}
		return nil
	})
	iv.Flags().StringVarP(&payload, "payload", "p", "", "JSON event")
	iv.Flags().BoolVar(&logs, "logs", false, "print the invocation's logs to stderr")
	iv.Flags().StringVarP(&qualifier, "qualifier", "q", "", "version or alias to invoke")
	iv.Flags().BoolVar(&async, "async", false, "invoke asynchronously (InvocationType Event)")

	var delQualifier string
	del := sub(l, "delete NAME", "Delete a function (or one published version with --qualifier)", cobra.ExactArgs(1), func(a []string) error {
		return call("DELETE", fnPath(a[0])+qualifierQuery(delQualifier), nil, nil)
	})
	del.Flags().StringVar(&delQualifier, "qualifier", "", "version to delete")

	var auth string
	url_ := sub(l, "url NAME", "Enable a function URL", cobra.ExactArgs(1), func(a []string) error {
		return call("PUT", fnPath(a[0], "/url"), map[string]any{"enabled": true, "auth_type": auth}, nil)
	})
	url_.Flags().StringVar(&auth, "auth", "NONE", "NONE or AWS_IAM (callers need lambda:InvokeFunctionUrl)")

	var batch int
	tr := sub(l, "trigger NAME QUEUE", "Invoke a function for messages arriving on an SQS queue", cobra.ExactArgs(2), func(a []string) error {
		return call("POST", "/api/v1/lambda/event-source-mappings", map[string]any{"function_name": a[0], "queue_name": a[1], "batch_size": batch}, nil)
	})
	tr.Flags().IntVar(&batch, "batch-size", 10, "messages per invocation")

	// Versions and aliases.
	var desc string
	pub := sub(l, "publish NAME", "Publish $LATEST as a new immutable version", cobra.ExactArgs(1), func(a []string) error {
		return call("POST", fnPath(a[0], "/versions"), map[string]string{"description": desc}, nil)
	})
	pub.Flags().StringVar(&desc, "description", "", "version description")
	sub(l, "versions NAME", "List a function's versions", cobra.ExactArgs(1), func(a []string) error {
		return call("GET", fnPath(a[0], "/versions"), nil, cols("VERSION=version", "DESCRIPTION=description", "CODE=code_sha256", "MODIFIED=last_modified"))
	})
	sub(l, "aliases NAME", "List a function's aliases", cobra.ExactArgs(1), func(a []string) error {
		return call("GET", fnPath(a[0], "/aliases"), nil, cols("ALIAS=name", "VERSION=function_version", "WEIGHTS=additional_version_weights", "ARN=arn"))
	})
	var weights []string
	al := sub(l, "alias NAME ALIAS VERSION", "Create or move an alias (optionally splitting traffic with --weight V=0.1)", cobra.ExactArgs(3), func(a []string) error {
		b := map[string]any{"name": a[1], "function_version": a[2]}
		w := map[string]float64{}
		for _, kv := range weights {
			k, v, _ := strings.Cut(kv, "=")
			f, err := strconv.ParseFloat(v, 64)
			if err != nil {
				return fmt.Errorf("--weight %s: want VERSION=WEIGHT", kv)
			}
			w[k] = f
		}
		b["additional_version_weights"] = w
		var aliases []map[string]any
		if err := api().Do("GET", fnPath(a[0], "/aliases"), nil, &aliases); err != nil {
			return err
		}
		for _, x := range aliases {
			if x["name"] == a[1] {
				return call("PATCH", fnPath(a[0], "/aliases/", url.PathEscape(a[1])), b, nil)
			}
		}
		return call("POST", fnPath(a[0], "/aliases"), b, nil)
	})
	al.Flags().StringSliceVar(&weights, "weight", nil, "route a share of traffic to another version: VERSION=WEIGHT (0-1)")
	sub(l, "delete-alias NAME ALIAS", "Delete an alias", cobra.ExactArgs(2), func(a []string) error {
		return call("DELETE", fnPath(a[0], "/aliases/", url.PathEscape(a[1])), nil, nil)
	})

	// Concurrency and asynchronous invocation.
	var unreserve bool
	cc := sub(l, "concurrency NAME [N]", "Reserve concurrency for a function (0 stops all invocations; --delete removes the reservation)", cobra.RangeArgs(1, 2), func(a []string) error {
		if unreserve {
			return call("DELETE", fnPath(a[0], "/concurrency"), nil, nil)
		}
		if len(a) < 2 {
			return fmt.Errorf("give the number of reserved concurrent executions, or --delete")
		}
		n, err := strconv.Atoi(a[1])
		if err != nil {
			return fmt.Errorf("N must be a number")
		}
		return call("PUT", fnPath(a[0], "/concurrency"), map[string]int{"reserved_concurrent_executions": n}, nil)
	})
	cc.Flags().BoolVar(&unreserve, "delete", false, "remove the reservation")
	var retries, maxAge int
	var onSuccess, onFailure, asyncQualifier string
	ac := sub(l, "async-config NAME", "Configure retries and destinations for asynchronous invocations", cobra.ExactArgs(1), nil)
	ac.RunE = func(cmd *cobra.Command, a []string) error {
		b := map[string]any{}
		if cmd.Flags().Changed("max-retries") {
			b["maximum_retry_attempts"] = retries
		}
		if cmd.Flags().Changed("max-age") {
			b["maximum_event_age_seconds"] = maxAge
		}
		if onSuccess != "" {
			b["on_success"] = onSuccess
		}
		if onFailure != "" {
			b["on_failure"] = onFailure
		}
		return call("PUT", fnPath(a[0], "/event-invoke-config")+qualifierQuery(asyncQualifier), b, nil)
	}
	ac.Flags().IntVar(&retries, "max-retries", 2, "retries after a function error (0-2)")
	ac.Flags().IntVar(&maxAge, "max-age", 21600, "discard events older than this many seconds (60-21600)")
	ac.Flags().StringVar(&onSuccess, "on-success", "", "destination ARN for successful invocations (SQS, SNS, Lambda or EventBridge bus)")
	ac.Flags().StringVar(&onFailure, "on-failure", "", "destination ARN for failed invocations")
	ac.Flags().StringVar(&asyncQualifier, "qualifier", "", "version or alias")

	// Layers.
	ly := &cobra.Command{Use: "layers", Short: "Lambda layers (code extracted into /opt)"}
	l.AddCommand(ly)
	sub(ly, "ls", "List layers (latest version of each)", cobra.NoArgs, func([]string) error {
		return call("GET", "/api/v1/lambda/layers", nil, cols("NAME=name", "VERSION=version", "ARN=arn", "SIZE=code_size", "CREATED=created_at"))
	})
	sub(ly, "versions LAYER", "List a layer's versions", cobra.ExactArgs(1), func(a []string) error {
		return call("GET", "/api/v1/lambda/layers/"+url.PathEscape(a[0])+"/versions", nil, cols("VERSION=version", "ARN=arn", "DESCRIPTION=description", "CREATED=created_at"))
	})
	var layerCode, layerDesc string
	var layerRuntimes []string
	lp := sub(ly, "publish LAYER", "Publish a layer version from --code DIR (e.g. a directory holding python/ or nodejs/)", cobra.ExactArgs(1), func(a []string) error {
		z, err := zipDir(layerCode)
		if err != nil {
			return err
		}
		return call("POST", "/api/v1/lambda/layers", map[string]any{"name": a[0], "zip_base64": z, "description": layerDesc, "compatible_runtimes": layerRuntimes}, nil)
	})
	lp.Flags().StringVar(&layerCode, "code", ".", "directory or file with the layer content")
	lp.Flags().StringVar(&layerDesc, "description", "", "description")
	lp.Flags().StringSliceVar(&layerRuntimes, "runtimes", nil, "compatible runtimes")
	sub(ly, "delete LAYER VERSION", "Delete a layer version", cobra.ExactArgs(2), func(a []string) error {
		return call("DELETE", "/api/v1/lambda/layers/"+url.PathEscape(a[0])+"/versions/"+url.PathEscape(a[1]), nil, nil)
	})
}
