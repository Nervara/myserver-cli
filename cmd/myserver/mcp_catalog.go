package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

const mcpCatalogWarnBytes = 192 * 1024

type mcpCatalogReport struct {
	OK             bool
	ToolCount      int
	CatalogBytes   int
	DuplicateNames int
	InvalidSchemas int
	Issues         []string
	Warnings       []string
}

func validateMCPToolCatalog(tools []map[string]any) mcpCatalogReport {
	report := mcpCatalogReport{OK: true, ToolCount: len(tools)}
	if raw, err := json.Marshal(map[string]any{"tools": tools}); err == nil {
		report.CatalogBytes = len(raw)
		if report.CatalogBytes > mcpCatalogWarnBytes {
			report.Warnings = append(report.Warnings, fmt.Sprintf("catalog is %d bytes; consider reducing tool descriptions or generated tools", report.CatalogBytes))
		}
	}

	seen := map[string]int{}
	for i, tool := range tools {
		name, _ := tool["name"].(string)
		name = strings.TrimSpace(name)
		if name == "" {
			report.addIssue("tool[%d] has empty name", i)
		} else {
			seen[name]++
			if seen[name] == 2 {
				report.DuplicateNames++
				report.addIssue("duplicate tool name %q", name)
			}
		}

		desc, _ := tool["description"].(string)
		if strings.TrimSpace(desc) == "" {
			report.addIssue("tool %q has empty description", displayToolName(name, i))
		}
		if containsSensitiveExample(desc) {
			report.addIssue("tool %q description appears to contain a secret/token example", displayToolName(name, i))
		}

		schema, ok := tool["inputSchema"].(map[string]any)
		if !ok || schema == nil {
			report.InvalidSchemas++
			report.addIssue("tool %q has missing or invalid inputSchema", displayToolName(name, i))
			continue
		}
		if issues := validateMCPInputSchema(displayToolName(name, i), schema); len(issues) > 0 {
			report.InvalidSchemas++
			for _, issue := range issues {
				report.addIssue("%s", issue)
			}
		}
	}

	sort.Strings(report.Issues)
	sort.Strings(report.Warnings)
	report.OK = len(report.Issues) == 0
	return report
}

func validateMCPInputSchema(name string, schema map[string]any) []string {
	var issues []string
	if schema["type"] != "object" {
		issues = append(issues, fmt.Sprintf("tool %q inputSchema.type must be object", name))
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok || props == nil {
		issues = append(issues, fmt.Sprintf("tool %q inputSchema.properties must be an object", name))
		return issues
	}
	for propName, rawProp := range props {
		prop, ok := rawProp.(map[string]any)
		if !ok || prop == nil {
			issues = append(issues, fmt.Sprintf("tool %q property %q must be a schema object", name, propName))
			continue
		}
		if strings.TrimSpace(fmt.Sprint(prop["type"])) == "" {
			issues = append(issues, fmt.Sprintf("tool %q property %q is missing type", name, propName))
		}
		if desc, _ := prop["description"].(string); containsSensitiveExample(desc) {
			issues = append(issues, fmt.Sprintf("tool %q property %q description appears to contain a secret/token example", name, propName))
		}
	}
	for _, required := range schemaRequiredFields(schema["required"]) {
		if _, ok := props[required]; !ok {
			issues = append(issues, fmt.Sprintf("tool %q requires missing property %q", name, required))
		}
	}
	return issues
}

func schemaRequiredFields(raw any) []string {
	switch v := raw.(type) {
	case nil:
		return nil
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return []string{fmt.Sprint(v)}
	}
}

func containsSensitiveExample(s string) bool {
	lower := strings.ToLower(s)
	for _, marker := range []string{
		"access_token\":\"",
		"refresh_token\":\"",
		"bearer ey",
		"password123",
		"secret-token",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func displayToolName(name string, idx int) string {
	if name != "" {
		return name
	}
	return fmt.Sprintf("#%d", idx)
}

func (r *mcpCatalogReport) addIssue(format string, args ...any) {
	r.Issues = append(r.Issues, fmt.Sprintf(format, args...))
}

func runMCPDoctor(args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("mcp doctor does not accept positional arguments")
	}

	report := validateMCPToolCatalog(mcpToolDescriptors())
	if report.OK {
		fmt.Fprintln(os.Stdout, "MCP catalog OK")
	} else {
		fmt.Fprintln(os.Stdout, "MCP catalog FAILED")
	}
	fmt.Fprintf(os.Stdout, "- tools: %d\n", report.ToolCount)
	fmt.Fprintf(os.Stdout, "- duplicate names: %d\n", report.DuplicateNames)
	fmt.Fprintf(os.Stdout, "- invalid schemas: %d\n", report.InvalidSchemas)
	fmt.Fprintf(os.Stdout, "- catalog bytes: %d\n", report.CatalogBytes)
	if len(report.Warnings) > 0 {
		fmt.Fprintln(os.Stdout, "")
		fmt.Fprintln(os.Stdout, "Warnings:")
		for _, warning := range report.Warnings {
			fmt.Fprintf(os.Stdout, "- %s\n", warning)
		}
	}
	if len(report.Issues) > 0 {
		fmt.Fprintln(os.Stdout, "")
		fmt.Fprintln(os.Stdout, "Issues:")
		for _, issue := range report.Issues {
			fmt.Fprintf(os.Stdout, "- %s\n", issue)
		}
		return fmt.Errorf("MCP catalog has %d issue(s)", len(report.Issues))
	}
	return nil
}
