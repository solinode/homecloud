package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/client"
	"github.com/spf13/cobra"
)

var output string

// RootCmd is the base command for the CLI.
var RootCmd = &cobra.Command{
	Use:   "homecloud",
	Short: "HomeCloud: a self-hosted cloud (compute, storage, databases, functions, queues) on your own hardware",
	Long: `HomeCloud runs AWS-style cloud services on your own machine.

Start the server once with 'homecloud serve', then manage resources from the
web console (http://127.0.0.1:8080) or this CLI.`,
	SilenceUsage:  true,
	SilenceErrors: true,
}

func Execute() {
	if err := RootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func init() {
	RootCmd.PersistentFlags().StringVarP(&output, "output", "o", "table", "output format: table or json")
}

func api() *client.Client {
	c, err := client.New()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	return c
}

// ---- output helpers ----

type column struct {
	Header string
	Field  string // dotted path into the JSON object
}

func cols(spec ...string) []column {
	out := make([]column, 0, len(spec))
	for _, s := range spec {
		h, f, ok := strings.Cut(s, "=")
		if !ok {
			f = h
			h = strings.ToUpper(strings.ReplaceAll(h, "_", " "))
		}
		out = append(out, column{Header: h, Field: f})
	}
	return out
}

func lookup(v any, path string) any {
	for _, p := range strings.Split(path, ".") {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[p]
	}
	return v
}

func format(v any) string {
	switch x := v.(type) {
	case nil:
		return "-"
	case string:
		if x == "" {
			return "-"
		}
		if t, err := time.Parse(time.RFC3339, x); err == nil && len(x) >= 20 {
			return t.Local().Format("2006-01-02 15:04")
		}
		return x
	case float64:
		if x == float64(int64(x)) {
			return fmt.Sprintf("%d", int64(x))
		}
		return fmt.Sprintf("%.2f", x)
	case bool:
		if x {
			return "yes"
		}
		return "no"
	case []any:
		parts := make([]string, 0, len(x))
		for _, e := range x {
			parts = append(parts, format(e))
		}
		if len(parts) == 0 {
			return "-"
		}
		return strings.Join(parts, ",")
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := []string{}
		for _, k := range keys {
			parts = append(parts, k+"="+format(x[k]))
		}
		if len(parts) == 0 {
			return "-"
		}
		return strings.Join(parts, ",")
	default:
		return fmt.Sprint(x)
	}
}

func printJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

// printList renders a JSON array as a table (or raw JSON with -o json).
func printList(v any, c []column) {
	if output == "json" {
		printJSON(v)
		return
	}
	items, _ := v.([]any)
	if len(items) == 0 {
		fmt.Println("(none)")
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	hs := make([]string, len(c))
	for i, col := range c {
		hs[i] = col.Header
	}
	fmt.Fprintln(w, strings.Join(hs, "\t"))
	for _, it := range items {
		row := make([]string, len(c))
		for i, col := range c {
			row[i] = format(lookup(it, col.Field))
		}
		fmt.Fprintln(w, strings.Join(row, "\t"))
	}
	w.Flush()
}

// printObject renders one JSON object as key/value lines.
func printObject(v any) {
	if output == "json" {
		printJSON(v)
		return
	}
	m, ok := v.(map[string]any)
	if !ok {
		printJSON(v)
		return
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, k := range keys {
		fmt.Fprintf(w, "%s\t%s\n", k, format(m[k]))
	}
	w.Flush()
}

// call performs a JSON request and prints the result as a list or object.
func call(method, path string, body any, c []column) error {
	var out any
	if err := api().Do(method, path, body, &out); err != nil {
		return err
	}
	if c != nil {
		printList(out, c)
	} else {
		printObject(out)
	}
	return nil
}

func tagsFlag(kv []string) map[string]string {
	if len(kv) == 0 {
		return nil
	}
	m := map[string]string{}
	for _, t := range kv {
		k, v, _ := strings.Cut(t, "=")
		m[k] = v
	}
	return m
}
