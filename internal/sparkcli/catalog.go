package sparkcli

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Catalog is the machine-readable tool list printed by `spark tools`. Spark
// only lists the tools the configured access levels allow.
type Catalog struct {
	Instructions string `json:"instructions"`
	Tools        []Tool `json:"tools"`
}

// Tool is one catalog entry.
type Tool struct {
	Name        string          `json:"name"`
	Title       string          `json:"title,omitempty"`
	Command     string          `json:"command"`
	Description string          `json:"description"`
	Parameters  []Param         `json:"parameters"`
	Annotations json.RawMessage `json:"annotations,omitempty"`
}

// Param is one tool parameter. Parameters without a Flag are positional and
// are passed in catalog order.
type Param struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Type        string `json:"type"`
	Required    bool   `json:"required,omitempty"`
	Flag        string `json:"flag,omitempty"`
	Items       *struct {
		Type string `json:"type"`
	} `json:"items,omitempty"`
}

// Param returns the parameter with the given name.
func (t *Tool) Param(name string) (Param, bool) {
	for _, p := range t.Parameters {
		if p.Name == name {
			return p, true
		}
	}
	return Param{}, false
}

// LoadCatalog runs `spark tools` and parses the result.
func (r *Runner) LoadCatalog(ctx context.Context, agent string) (*Catalog, error) {
	res, err := r.Run(ctx, Call{Args: []string{"tools"}, Timeout: 15 * time.Second, Agent: agent})
	if err != nil {
		return nil, err
	}
	return ParseCatalog(res.Stdout)
}

// ParseCatalog validates catalog JSON.
func ParseCatalog(data []byte) (*Catalog, error) {
	var c Catalog
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, E(CodeUnavailable, "cannot parse `spark tools` output: %s", err)
	}
	for i, t := range c.Tools {
		if t.Name == "" || t.Command == "" {
			return nil, E(CodeUnavailable, "catalog entry %d has no name or command", i)
		}
		for _, p := range t.Parameters {
			switch p.Type {
			case "string", "integer", "number", "boolean", "array":
			default:
				return nil, E(CodeUnavailable, "tool %s parameter %s has unsupported type %q", t.Name, p.Name, p.Type)
			}
		}
	}
	return &c, nil
}

// InputSchema builds the JSON Schema of a tool.
func (t *Tool) InputSchema() map[string]any {
	props := map[string]any{}
	var required []string
	for _, p := range t.Parameters {
		prop := map[string]any{"type": p.Type}
		if p.Description != "" {
			prop["description"] = p.Description
		}
		if p.Type == "array" {
			itemType := "string"
			if p.Items != nil && p.Items.Type != "" {
				itemType = p.Items.Type
			}
			prop["items"] = map[string]any{"type": itemType}
		}
		props[p.Name] = prop
		if p.Required {
			required = append(required, p.Name)
		}
	}
	schema := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

// BuildArgs turns decoded tool arguments into the CLI argument vector:
// command, flags in --flag=value form, then "--" and the positional values.
// The equals form and the terminator keep values that start with "-" from
// being read as options. Unknown arguments and type mismatches are rejected.
func (t *Tool) BuildArgs(in map[string]any) ([]string, error) {
	known := map[string]bool{}
	for _, p := range t.Parameters {
		known[p.Name] = true
	}
	for k := range in {
		if !known[k] {
			return nil, E(CodeInvalidArgument, "unknown argument %q for tool %s", k, t.Name)
		}
	}
	var flags, positional []string
	for _, p := range t.Parameters {
		v, ok := in[p.Name]
		if !ok || v == nil {
			if p.Required {
				return nil, E(CodeInvalidArgument, "%s is required", p.Name)
			}
			continue
		}
		values, err := scalarValues(p, v)
		if err != nil {
			return nil, err
		}
		if len(values) == 0 {
			if p.Required {
				return nil, E(CodeInvalidArgument, "%s must not be empty", p.Name)
			}
			continue
		}
		switch {
		case p.Flag == "":
			positional = append(positional, values...)
		case p.Type == "boolean":
			if values[0] == "true" {
				flags = append(flags, p.Flag)
			}
		default:
			for _, s := range values {
				flags = append(flags, p.Flag+"="+s)
			}
		}
	}
	args := append([]string{t.Command}, flags...)
	if len(positional) > 0 {
		args = append(args, "--")
		args = append(args, positional...)
	}
	return args, nil
}

// scalarValues converts one argument to its string forms; empty strings and
// empty arrays yield no values.
func scalarValues(p Param, v any) ([]string, error) {
	bad := func() error {
		return E(CodeInvalidArgument, "%s must be of type %s", p.Name, p.Type)
	}
	switch p.Type {
	case "array":
		arr, ok := v.([]any)
		if !ok {
			// Be lenient with a single scalar where a list is expected.
			if s, err := scalar(v, itemType(p)); err == nil && s != "" {
				return []string{s}, nil
			}
			return nil, bad()
		}
		var out []string
		for _, item := range arr {
			s, err := scalar(item, itemType(p))
			if err != nil {
				return nil, E(CodeInvalidArgument, "%s items must be of type %s", p.Name, itemType(p))
			}
			if s != "" {
				out = append(out, s)
			}
		}
		return out, nil
	case "boolean":
		b, ok := v.(bool)
		if !ok {
			return nil, bad()
		}
		return []string{strconv.FormatBool(b)}, nil
	default:
		s, err := scalar(v, p.Type)
		if err != nil {
			return nil, bad()
		}
		if s == "" {
			return nil, nil
		}
		return []string{s}, nil
	}
}

func itemType(p Param) string {
	if p.Items != nil && p.Items.Type != "" {
		return p.Items.Type
	}
	return "string"
}

func scalar(v any, typ string) (string, error) {
	switch typ {
	case "integer":
		switch n := v.(type) {
		case json.Number:
			if i, err := n.Int64(); err == nil {
				return strconv.FormatInt(i, 10), nil
			}
		case float64:
			if n == float64(int64(n)) {
				return strconv.FormatInt(int64(n), 10), nil
			}
		case string:
			if _, err := strconv.ParseInt(strings.TrimSpace(n), 10, 64); err == nil {
				return strings.TrimSpace(n), nil
			}
		}
		return "", fmt.Errorf("not an integer")
	case "number":
		switch n := v.(type) {
		case json.Number:
			return n.String(), nil
		case float64:
			return strconv.FormatFloat(n, 'f', -1, 64), nil
		}
		return "", fmt.Errorf("not a number")
	case "boolean":
		if b, ok := v.(bool); ok {
			return strconv.FormatBool(b), nil
		}
		return "", fmt.Errorf("not a boolean")
	default:
		switch s := v.(type) {
		case string:
			if strings.ContainsRune(s, 0) {
				return "", fmt.Errorf("NUL byte")
			}
			return s, nil
		case json.Number:
			// IDs are strings in the catalog but models often send numbers.
			return s.String(), nil
		case float64:
			return strconv.FormatFloat(s, 'f', -1, 64), nil
		}
		return "", fmt.Errorf("not a string")
	}
}
