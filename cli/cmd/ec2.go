package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

var instanceCols = cols("ID=id", "NAME=name", "STATE=state", "TYPE=instance_type", "IMAGE=image_id", "PRIVATE IP=private_ip", "PORTS=public_ports", "LAUNCHED=launch_time")

func init() {
	ec2 := &cobra.Command{Use: "ec2", Short: "Compute instances, images, volumes and instance types"}
	RootCmd.AddCommand(ec2)

	var run struct {
		Name, Image, Type, Subnet, UserData, UserDataFile string
		SGs, Tags, Volumes, FS                            []string
		Count                                             int
		Wait                                              bool
	}
	runCmd := &cobra.Command{
		Use:   "run",
		Short: "Launch instances",
		Example: `  homecloud ec2 run --name web --image ami-ubuntu-24-04 --type t3.small
  homecloud ec2 run --image ami-nginx --sg sg-0123 --volume 10:/data`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if run.UserDataFile != "" {
				b, err := os.ReadFile(run.UserDataFile)
				if err != nil {
					return err
				}
				run.UserData = string(b)
			}
			vols := []map[string]any{}
			for _, v := range run.Volumes {
				size, mount, ok := strings.Cut(v, ":")
				if !ok {
					return fmt.Errorf("--volume must be SIZE_GB:/mount/path or vol-id:/mount/path")
				}
				if strings.HasPrefix(size, "vol-") {
					vols = append(vols, map[string]any{"volume_id": size, "mount_path": mount})
				} else {
					var n int
					fmt.Sscan(size, &n)
					vols = append(vols, map[string]any{"size_gb": n, "mount_path": mount})
				}
			}
			fss := []map[string]any{}
			for _, v := range run.FS {
				id, mount, ok := strings.Cut(v, ":")
				if !ok {
					return fmt.Errorf("--fs must be fs-id:/mount/path")
				}
				fss = append(fss, map[string]any{"file_system_id": id, "mount_path": mount})
			}
			body := map[string]any{"file_systems": fss, "name": run.Name, "image_id": run.Image, "instance_type": run.Type, "subnet_id": run.Subnet,
				"security_group_ids": run.SGs, "user_data": run.UserData, "count": run.Count, "tags": tagsFlag(run.Tags), "volumes": vols}
			var out []map[string]any
			c := api()
			if err := c.Do("POST", "/api/v1/ec2/instances", body, &out); err != nil {
				return err
			}
			if run.Wait {
				for i, inst := range out {
					id := inst["id"].(string)
					for {
						var cur map[string]any
						if err := c.Do("GET", "/api/v1/ec2/instances/"+id, nil, &cur); err != nil {
							return err
						}
						if cur["state"] != "pending" {
							out[i] = cur
							break
						}
						time.Sleep(time.Second)
					}
				}
			}
			items := make([]any, len(out))
			for i := range out {
				items[i] = out[i]
			}
			printList(items, instanceCols)
			return nil
		},
	}
	f := runCmd.Flags()
	f.StringVar(&run.Name, "name", "", "instance name")
	f.StringVar(&run.Image, "image", "ami-ubuntu-24-04", "image (AMI) id; see `homecloud ec2 images`")
	f.StringVar(&run.Type, "type", "t3.micro", "instance type; see `homecloud ec2 types`")
	f.StringVar(&run.Subnet, "subnet", "", "subnet id (default subnet when empty)")
	f.StringSliceVar(&run.SGs, "sg", nil, "security group ids")
	f.StringVar(&run.UserData, "user-data", "", "shell script to run on first boot")
	f.StringVar(&run.UserDataFile, "user-data-file", "", "read user data from a file")
	f.StringSliceVar(&run.Volumes, "volume", nil, "attach a volume: SIZE_GB:/path (new) or vol-id:/path (existing)")
	f.StringSliceVar(&run.Tags, "tag", nil, "tags as key=value")
	f.StringSliceVar(&run.FS, "fs", nil, "mount a shared file system: fs-id:/path")
	f.IntVar(&run.Count, "count", 1, "number of instances")
	f.BoolVar(&run.Wait, "wait", true, "wait until instances leave the pending state")
	ec2.AddCommand(runCmd)

	ec2.AddCommand(&cobra.Command{
		Use: "ls", Aliases: []string{"list"}, Short: "List instances",
		RunE: func(cmd *cobra.Command, args []string) error {
			return call("GET", "/api/v1/ec2/instances", nil, instanceCols)
		},
	})
	ec2.AddCommand(&cobra.Command{
		Use: "describe ID", Short: "Show one instance", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return call("GET", "/api/v1/ec2/instances/"+args[0], nil, nil)
		},
	})
	for _, a := range []struct{ use, verb, method, suffix string }{
		{"start", "Start stopped instances", "POST", "/start"},
		{"stop", "Stop running instances", "POST", "/stop"},
		{"reboot", "Reboot instances", "POST", "/reboot"},
		{"terminate", "Terminate instances (deletes their disks)", "DELETE", ""},
	} {
		a := a
		ec2.AddCommand(&cobra.Command{
			Use: a.use + " ID...", Short: a.verb, Args: cobra.MinimumNArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				c := api()
				for _, id := range args {
					var out map[string]any
					if err := c.Do(a.method, "/api/v1/ec2/instances/"+id+a.suffix, nil, &out); err != nil {
						return fmt.Errorf("%s: %w", id, err)
					}
					// Wait for pending/stopping/shutting-down to settle.
					for i := 0; i < 120 && (out["state"] == "pending" || out["state"] == "stopping" || out["state"] == "shutting-down"); i++ {
						time.Sleep(500 * time.Millisecond)
						if err := c.Do("GET", "/api/v1/ec2/instances/"+id, nil, &out); err != nil {
							return err
						}
					}
					fmt.Printf("%s\t%v\n", id, out["state"])
				}
				return nil
			},
		})
	}
	ec2.AddCommand(&cobra.Command{
		Use: "logs ID", Short: "Show an instance's console output", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var out map[string]any
			if err := api().Do("GET", "/api/v1/ec2/instances/"+args[0]+"/console-output", nil, &out); err != nil {
				return err
			}
			fmt.Print(out["output"])
			return nil
		},
	})
	ec2.AddCommand(&cobra.Command{
		Use: "exec ID -- COMMAND", Short: "Run a shell command on an instance and print its output", Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			var out map[string]any
			if err := api().Do("POST", "/api/v1/ec2/instances/"+args[0]+"/commands", map[string]any{"command": strings.Join(args[1:], " ")}, &out); err != nil {
				return err
			}
			fmt.Print(out["stdout"])
			fmt.Fprint(os.Stderr, out["stderr"])
			if code, _ := out["exit_code"].(float64); code != 0 {
				os.Exit(int(code))
			}
			return nil
		},
	})
	ec2.AddCommand(&cobra.Command{
		Use: "ssh ID", Short: "Open an interactive shell on an instance (the server must run on this machine)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var out map[string]any
			if err := api().Do("GET", "/api/v1/ec2/instances/"+args[0], nil, &out); err != nil {
				return err
			}
			cid, _ := out["container_id"].(string)
			if out["state"] != "running" || cid == "" {
				return fmt.Errorf("instance %s is %v", args[0], out["state"])
			}
			sh := exec.Command("docker", "exec", "-it", cid, "/bin/sh", "-c", "if command -v bash >/dev/null; then exec bash -l; else exec sh -l; fi")
			sh.Stdin, sh.Stdout, sh.Stderr = os.Stdin, os.Stdout, os.Stderr
			return sh.Run()
		},
	})
	var imgName string
	createImage := &cobra.Command{
		Use: "create-image ID", Short: "Capture an instance's disk as a new image (AMI)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return call("POST", "/api/v1/ec2/instances/"+args[0]+"/image", map[string]any{"name": imgName}, nil)
		},
	}
	createImage.Flags().StringVar(&imgName, "name", "", "image name")
	ec2.AddCommand(createImage)
	ec2.AddCommand(&cobra.Command{
		Use: "images", Short: "List images (AMIs)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return call("GET", "/api/v1/ec2/images", nil, cols("ID=id", "NAME=name", "SOURCE=ref", "OWNER=owner"))
		},
	})
	ec2.AddCommand(&cobra.Command{
		Use: "types", Short: "List instance types",
		RunE: func(cmd *cobra.Command, args []string) error {
			return call("GET", "/api/v1/ec2/instance-types", nil, cols("TYPE=name", "VCPUS=vcpus", "MEMORY MB=memory_mb", "FAMILY=family"))
		},
	})
	ec2.AddCommand(&cobra.Command{
		Use: "volumes", Short: "List volumes",
		RunE: func(cmd *cobra.Command, args []string) error {
			return call("GET", "/api/v1/ec2/volumes", nil, cols("ID=id", "NAME=name", "SIZE GB=size_gb", "STATE=state", "ATTACHED TO=attached_to", "MOUNT=mount_path"))
		},
	})
}
