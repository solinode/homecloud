package events

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// eventPath is a JSON path into an event ("$.detail.items[0].id").
type eventPath []pathSeg

type pathSeg struct {
	key   string
	index int
	isIdx bool
}

func parseEventPath(p string) (eventPath, error) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(p), "$")
	if !ok {
		return nil, fmt.Errorf("path %q must start with $", p)
	}
	var out eventPath
	for rest != "" {
		switch {
		case strings.HasPrefix(rest, "."):
			rest = rest[1:]
			end := strings.IndexAny(rest, ".[")
			if end < 0 {
				end = len(rest)
			}
			if end == 0 {
				return nil, fmt.Errorf("empty field in path %q", p)
			}
			out = append(out, pathSeg{key: rest[:end]})
			rest = rest[end:]
		case strings.HasPrefix(rest, "['") || strings.HasPrefix(rest, `["`):
			q := rest[1:2]
			end := strings.Index(rest[2:], q+"]")
			if end < 0 {
				return nil, fmt.Errorf("unterminated [ in path %q", p)
			}
			out = append(out, pathSeg{key: rest[2 : 2+end]})
			rest = rest[end+4:]
		case strings.HasPrefix(rest, "["):
			end := strings.IndexByte(rest, ']')
			if end < 0 {
				return nil, fmt.Errorf("unterminated [ in path %q", p)
			}
			n, err := strconv.Atoi(rest[1:end])
			if err != nil || n < 0 {
				return nil, fmt.Errorf("bad index in path %q", p)
			}
			out = append(out, pathSeg{index: n, isIdx: true})
			rest = rest[end+1:]
		default:
			return nil, fmt.Errorf("cannot parse path %q", p)
		}
	}
	return out, nil
}

func (p eventPath) get(doc any) (any, bool) {
	cur := doc
	for _, s := range p {
		if s.isIdx {
			arr, ok := cur.([]any)
			if !ok || s.index >= len(arr) {
				return nil, false
			}
			cur = arr[s.index]
			continue
		}
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = m[s.key]; !ok {
			return nil, false
		}
	}
	return cur, true
}

// transform renders an InputTransformer template. Placeholders <name> inside
// a JSON string are replaced by the value's text (escaped); outside strings by
// its JSON. Missing values render as null (or empty inside strings).
func transform(it *InputTransformer, r Rule, ev map[string]any, raw []byte) ([]byte, error) {
	vals := map[string]any{}
	present := map[string]bool{}
	for k, p := range it.InputPathsMap {
		ep, err := parseEventPath(p)
		if err != nil {
			return nil, err
		}
		vals[k], present[k] = ep.get(ev)
	}
	var generic any
	_ = json.Unmarshal(raw, &generic)
	vals["aws.events.event"], present["aws.events.event"] = generic, true
	vals["aws.events.event.json"], present["aws.events.event.json"] = generic, true
	vals["aws.events.rule-arn"], present["aws.events.rule-arn"] = r.ARN, true
	vals["aws.events.rule-name"], present["aws.events.rule-name"] = r.Name, true
	vals["aws.events.event.ingestion-time"], present["aws.events.event.ingestion-time"] = time.Now().UTC().Format(time.RFC3339), true

	tmpl := it.InputTemplate
	var b strings.Builder
	inString := false
	for i := 0; i < len(tmpl); i++ {
		c := tmpl[i]
		if c == '\\' && inString && i+1 < len(tmpl) {
			b.WriteByte(c)
			b.WriteByte(tmpl[i+1])
			i++
			continue
		}
		if c == '"' {
			inString = !inString
			b.WriteByte(c)
			continue
		}
		if c == '<' {
			end := strings.IndexByte(tmpl[i+1:], '>')
			if end > 0 {
				name := tmpl[i+1 : i+1+end]
				if _, known := present[name]; known {
					v := vals[name]
					switch {
					case inString:
						s, isStr := v.(string)
						if !isStr {
							if !present[name] {
								s = ""
							} else {
								j, _ := json.Marshal(v)
								s = string(j)
							}
						}
						j, _ := json.Marshal(s)
						b.Write(j[1 : len(j)-1])
					case !present[name]:
						b.WriteString("null")
					default:
						j, _ := json.Marshal(v)
						b.Write(j)
					}
					i += end + 1
					continue
				}
			}
		}
		b.WriteByte(c)
	}
	return []byte(b.String()), nil
}
