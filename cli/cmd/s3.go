package cmd

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// parseS3 splits "s3://bucket/key" into bucket and key.
func parseS3(u string) (bucket, key string, ok bool) {
	rest, ok := strings.CutPrefix(u, "s3://")
	if !ok {
		return "", "", false
	}
	bucket, key, _ = strings.Cut(rest, "/")
	return bucket, key, bucket != ""
}

func objURL(bucket, key string) string {
	return "/api/v1/s3/buckets/" + url.PathEscape(bucket) + "/object?key=" + url.QueryEscape(key)
}

func init() {
	s3 := &cobra.Command{Use: "s3", Short: "Object storage (S3-compatible)"}
	RootCmd.AddCommand(s3)

	var public, versioning bool
	mb := &cobra.Command{
		Use: "mb s3://BUCKET", Short: "Make a bucket", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			b, _, ok := parseS3(args[0])
			if !ok {
				b = args[0]
			}
			if err := api().Do("POST", "/api/v1/s3/buckets", map[string]any{"name": b, "public": public, "versioning": versioning}, nil); err != nil {
				return err
			}
			fmt.Println("make_bucket:", b)
			return nil
		},
	}
	mb.Flags().BoolVar(&public, "public", false, "allow anonymous read of objects")
	mb.Flags().BoolVar(&versioning, "versioning", false, "keep every version of every object")
	s3.AddCommand(mb)

	var force bool
	rb := &cobra.Command{
		Use: "rb s3://BUCKET", Short: "Remove a bucket", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			b, _, ok := parseS3(args[0])
			if !ok {
				b = args[0]
			}
			q := ""
			if force {
				q = "?force=true"
			}
			if err := api().Do("DELETE", "/api/v1/s3/buckets/"+url.PathEscape(b)+q, nil, nil); err != nil {
				return err
			}
			fmt.Println("remove_bucket:", b)
			return nil
		},
	}
	rb.Flags().BoolVar(&force, "force", false, "delete all objects first")
	s3.AddCommand(rb)

	var recursive bool
	ls := &cobra.Command{
		Use: "ls [s3://BUCKET[/PREFIX]]", Short: "List buckets or objects", Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return call("GET", "/api/v1/s3/buckets", nil, cols("CREATED=created_at", "BUCKET=name", "PUBLIC=public"))
			}
			b, prefix, ok := parseS3(args[0])
			if !ok {
				return fmt.Errorf("expected s3://bucket[/prefix]")
			}
			var out struct {
				Prefixes []string         `json:"prefixes"`
				Objects  []map[string]any `json:"objects"`
			}
			q := "?prefix=" + url.QueryEscape(prefix)
			if recursive {
				q += "&recursive=true"
			}
			if err := api().Do("GET", "/api/v1/s3/buckets/"+url.PathEscape(b)+"/objects"+q, nil, &out); err != nil {
				return err
			}
			if output == "json" {
				printJSON(out)
				return nil
			}
			for _, p := range out.Prefixes {
				fmt.Printf("%30s %s\n", "PRE", p)
			}
			for _, o := range out.Objects {
				fmt.Printf("%s %10v %s\n", format(o["last_modified"]), format(o["size"]), o["key"])
			}
			return nil
		},
	}
	ls.Flags().BoolVarP(&recursive, "recursive", "r", false, "list every object under the prefix")
	s3.AddCommand(ls)

	s3.AddCommand(&cobra.Command{
		Use: "cp SRC DST", Short: "Copy a file to or from S3", Args: cobra.ExactArgs(2),
		Example: "  homecloud s3 cp ./photo.jpg s3://photos/2025/photo.jpg\n  homecloud s3 cp s3://photos/2025/photo.jpg .",
		RunE: func(cmd *cobra.Command, args []string) error {
			c := api()
			sb, sk, srcS3 := parseS3(args[0])
			db, dk, dstS3 := parseS3(args[1])
			switch {
			case !srcS3 && dstS3:
				f, err := os.Open(args[0])
				if err != nil {
					return err
				}
				defer f.Close()
				st, _ := f.Stat()
				if dk == "" || strings.HasSuffix(dk, "/") {
					dk += filepath.Base(args[0])
				}
				req := http.Header{"Content-Type": {"application/octet-stream"}}
				resp, err := c.Request("PUT", objURL(db, dk), f, req)
				if err != nil {
					return err
				}
				resp.Body.Close()
				fmt.Printf("upload: %s to s3://%s/%s (%d bytes)\n", args[0], db, dk, st.Size())
			case srcS3 && !dstS3:
				resp, err := c.Request("GET", objURL(sb, sk), nil, nil)
				if err != nil {
					return err
				}
				defer resp.Body.Close()
				dst := args[1]
				if fi, err := os.Stat(dst); err == nil && fi.IsDir() {
					dst = filepath.Join(dst, path.Base(sk))
				}
				f, err := os.Create(dst)
				if err != nil {
					return err
				}
				n, err := io.Copy(f, resp.Body)
				f.Close()
				if err != nil {
					return err
				}
				fmt.Printf("download: s3://%s/%s to %s (%d bytes)\n", sb, sk, dst, n)
			case srcS3 && dstS3:
				if dk == "" || strings.HasSuffix(dk, "/") {
					dk += path.Base(sk)
				}
				if err := c.Do("POST", "/api/v1/s3/buckets/"+url.PathEscape(db)+"/copy", map[string]any{"source_bucket": sb, "source_key": sk, "key": dk}, nil); err != nil {
					return err
				}
				fmt.Printf("copy: s3://%s/%s to s3://%s/%s\n", sb, sk, db, dk)
			default:
				return fmt.Errorf("one of SRC or DST must be an s3:// URL")
			}
			return nil
		},
	})

	var rmRecursive bool
	rm := &cobra.Command{
		Use: "rm s3://BUCKET/KEY", Short: "Delete an object (or a prefix with --recursive)", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			b, k, ok := parseS3(args[0])
			if !ok || k == "" {
				return fmt.Errorf("expected s3://bucket/key")
			}
			u := objURL(b, k)
			if rmRecursive {
				u += "&recursive=true"
			}
			if err := api().Do("DELETE", u, nil, nil); err != nil {
				return err
			}
			fmt.Println("delete:", args[0])
			return nil
		},
	}
	rm.Flags().BoolVarP(&rmRecursive, "recursive", "r", false, "delete everything under the prefix")
	s3.AddCommand(rm)

	var expires int
	presign := &cobra.Command{
		Use: "presign s3://BUCKET/KEY", Short: "Create a time-limited download URL", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			b, k, ok := parseS3(args[0])
			if !ok || k == "" {
				return fmt.Errorf("expected s3://bucket/key")
			}
			var out map[string]any
			if err := api().Do("POST", "/api/v1/s3/buckets/"+url.PathEscape(b)+"/presign", map[string]any{"key": k, "expires_seconds": expires}, &out); err != nil {
				return err
			}
			fmt.Println(out["url"])
			return nil
		},
	}
	presign.Flags().IntVar(&expires, "expires-in", 3600, "seconds until the URL expires")
	s3.AddCommand(presign)

	s3.AddCommand(&cobra.Command{
		Use: "credentials", Short: "Show the S3 endpoint and keys for AWS SDKs / aws-cli",
		RunE: func(cmd *cobra.Command, args []string) error { return call("GET", "/api/v1/s3/credentials", nil, nil) },
	})
}
