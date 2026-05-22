package inspect

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// ImageMetadata holds image-level information recovered from docker inspect.
// OCI holds org.opencontainers.image.* values with the prefix stripped.
// Labels holds coral.* labels verbatim.
type ImageMetadata struct {
	OCI    map[string]string `json:"oci,omitempty"`
	Labels map[string]string `json:"labels,omitempty"`
}

// ociPromoted are rendered as structured prose rather than a generic table.
var ociPromoted = map[string]bool{
	"title": true, "description": true, "version": true, "authors": true,
	"url": true, "source": true, "documentation": true, "licenses": true,
}

// ParseBehaviors parses the flat JSON object from coral-inspect into a map of
// behavior name -> raw JSON, preserving fields the formatter doesn't recognize.
func ParseBehaviors(raw []byte) (map[string]json.RawMessage, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parsing behavior JSON: %w", err)
	}
	return m, nil
}

// FormatJSON wraps everything into a single object and pretty-prints.
// All behavior fields from the original JSON are preserved.
func FormatJSON(image string, meta ImageMetadata, behaviors map[string]json.RawMessage) ([]byte, error) {
	wrapper := struct {
		Image     string                     `json:"image"`
		OCI       map[string]string          `json:"oci,omitempty"`
		Labels    map[string]string          `json:"labels,omitempty"`
		Behaviors map[string]json.RawMessage `json:"behaviors"`
	}{image, meta.OCI, meta.Labels, behaviors}
	return json.MarshalIndent(wrapper, "", "  ")
}

// FormatMarkdown renders the full image digest as Markdown.
func FormatMarkdown(image string, meta ImageMetadata, behaviors map[string]json.RawMessage) string {
	var sb strings.Builder

	// Heading: prefer OCI title, fall back to image name.
	title := meta.OCI["title"]
	if title != "" {
		fmt.Fprintf(&sb, "# %s\n", title)
		fmt.Fprintf(&sb, "`%s`\n\n", image)
	} else {
		fmt.Fprintf(&sb, "# %s\n\n", image)
	}

	// Description as a paragraph.
	if desc := meta.OCI["description"]; desc != "" {
		fmt.Fprintf(&sb, "%s\n\n", desc)
	}

	// Version / authors / licenses on one line.
	var inlineFields []string
	for _, key := range []string{"version", "authors", "licenses"} {
		if v := meta.OCI[key]; v != "" {
			inlineFields = append(inlineFields, fmt.Sprintf("**%s**: %s", key, v))
		}
	}
	if len(inlineFields) > 0 {
		sb.WriteString(strings.Join(inlineFields, " · "))
		sb.WriteString("\n\n")
	}

	// URL / source / documentation as links.
	for _, key := range []string{"url", "source", "documentation"} {
		if v := meta.OCI[key]; v != "" {
			fmt.Fprintf(&sb, "**%s**: <%s>  \n", key, v)
		}
	}
	if v := meta.OCI["url"]; v != "" || meta.OCI["source"] != "" || meta.OCI["documentation"] != "" {
		_ = v
		sb.WriteString("\n")
	}

	// Any remaining OCI fields not handled above.
	var ociExtras []string
	for k := range meta.OCI {
		if !ociPromoted[k] {
			ociExtras = append(ociExtras, k)
		}
	}
	sort.Strings(ociExtras)
	if len(ociExtras) > 0 {
		sb.WriteString("| OCI Field | Value |\n")
		sb.WriteString("|-----------|-------|\n")
		for _, k := range ociExtras {
			fmt.Fprintf(&sb, "| `%s` | `%s` |\n", k, meta.OCI[k])
		}
		sb.WriteString("\n")
	}

	// Coral labels table.
	if len(meta.Labels) > 0 {
		sb.WriteString("| Label | Value |\n")
		sb.WriteString("|-------|-------|\n")
		for _, k := range sortedStringKeys(meta.Labels) {
			fmt.Fprintf(&sb, "| `%s` | `%s` |\n", k, meta.Labels[k])
		}
		sb.WriteString("\n")
	}

	if len(behaviors) == 0 {
		sb.WriteString("No behaviors exported.\n")
		return sb.String()
	}

	sb.WriteString("---\n\n")

	// Behaviors.
	knownBehaviorFields := map[string]bool{"description": true, "inputs": true, "outputs": true}

	for _, name := range sortedRawKeys(behaviors) {
		fields := rawFields(behaviors[name])

		fmt.Fprintf(&sb, "## `%s`\n", name)
		if desc := stringField(fields, "description"); desc != "" && desc != name {
			fmt.Fprintf(&sb, "%s\n", desc)
		}
		sb.WriteString("\n")

		if inputs := portsField(fields, "inputs"); len(inputs) > 0 {
			sb.WriteString("**Inputs**\n\n")
			writePortTable(&sb, inputs)
		}
		if outputs := portsField(fields, "outputs"); len(outputs) > 0 {
			sb.WriteString("**Outputs**\n\n")
			writePortTable(&sb, outputs)
		}

		var extras []string
		for k := range fields {
			if !knownBehaviorFields[k] {
				extras = append(extras, k)
			}
		}
		sort.Strings(extras)
		for _, k := range extras {
			fmt.Fprintf(&sb, "**%s**: %s\n\n", k, compactJSON(fields[k]))
		}

		sb.WriteString("---\n\n")
	}

	return sb.String()
}

func writePortTable(sb *strings.Builder, ports map[string]json.RawMessage) {
	hasDefault := false
	for _, raw := range ports {
		if _, ok := rawFields(raw)["default"]; ok {
			hasDefault = true
			break
		}
	}

	if hasDefault {
		sb.WriteString("| Port | Type | Default | Description |\n")
		sb.WriteString("|------|------|---------|-------------|\n")
	} else {
		sb.WriteString("| Port | Type | Description |\n")
		sb.WriteString("|------|------|-------------|\n")
	}

	for _, portName := range sortedRawKeys(ports) {
		pf := rawFields(ports[portName])
		typ := stringField(pf, "type")
		desc := stringField(pf, "description")
		if hasDefault {
			fmt.Fprintf(sb, "| `%s` | `%s` | %s | %s |\n",
				portName, typ, backtickOrEmpty(stringField(pf, "default")), desc)
		} else {
			fmt.Fprintf(sb, "| `%s` | `%s` | %s |\n", portName, typ, desc)
		}
	}
	sb.WriteString("\n")
}

func rawFields(raw json.RawMessage) map[string]json.RawMessage {
	var m map[string]json.RawMessage
	json.Unmarshal(raw, &m) //nolint:errcheck
	return m
}

func portsField(fields map[string]json.RawMessage, key string) map[string]json.RawMessage {
	raw, ok := fields[key]
	if !ok {
		return nil
	}
	var ports map[string]json.RawMessage
	json.Unmarshal(raw, &ports) //nolint:errcheck
	return ports
}

func stringField(fields map[string]json.RawMessage, key string) string {
	raw, ok := fields[key]
	if !ok {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return string(raw)
	}
	return s
}

func sortedRawKeys(m map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedStringKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func compactJSON(raw json.RawMessage) string {
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return string(raw)
	}
	return buf.String()
}

func backtickOrEmpty(s string) string {
	if s == "" {
		return ""
	}
	return "`" + s + "`"
}
