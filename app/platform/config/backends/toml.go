package backends

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

type TomlConfigBackend struct {
	path string
}

func NewTomlConfigBackend(path string) *TomlConfigBackend {
	return &TomlConfigBackend{path: path}
}

func (b *TomlConfigBackend) Load(context.Context) (map[string]any, error) {
	file, err := os.Open(b.path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{}, nil
		}
		return nil, err
	}
	defer file.Close()
	return parseBackendTOML(file)
}

func (b *TomlConfigBackend) ApplyPatch(_ context.Context, patch map[string]any) error {
	existing := map[string]any{}
	file, err := os.Open(b.path)
	if err == nil {
		existing, err = parseBackendTOML(file)
		closeErr := file.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	merged := deepMergeTOML(existing, patch)
	if err := os.MkdirAll(filepath.Dir(b.path), 0o755); err != nil {
		return err
	}
	out, err := os.Create(b.path)
	if err != nil {
		return err
	}
	defer out.Close()
	return writeBackendTOML(out, merged)
}

func (b *TomlConfigBackend) Clear(context.Context) error {
	if err := os.MkdirAll(filepath.Dir(b.path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(b.path, []byte{}, 0o644)
}

func (b *TomlConfigBackend) Version(context.Context) (any, error) {
	info, err := os.Stat(b.path)
	if err != nil {
		return float64(0), nil
	}
	return float64(info.ModTime().UnixNano()) / 1e9, nil
}

func (b *TomlConfigBackend) Close(context.Context) error {
	return nil
}

func deepMergeTOML(base, override map[string]any) map[string]any {
	result := map[string]any{}
	for key, value := range base {
		result[key] = value
	}
	for key, value := range override {
		baseNested, baseOK := result[key].(map[string]any)
		overrideNested, overrideOK := value.(map[string]any)
		if baseOK && overrideOK {
			result[key] = deepMergeTOML(baseNested, overrideNested)
			continue
		}
		result[key] = value
	}
	return result
}

func parseBackendTOML(r io.Reader) (map[string]any, error) {
	data := map[string]any{}
	current := data
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := stripBackendTOMLComment(strings.TrimSpace(scanner.Text()))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			current = ensureBackendTOMLSection(data, strings.TrimSpace(line[1:len(line)-1]))
			continue
		}
		key, rawValue, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		current[strings.TrimSpace(key)] = parseBackendTOMLValue(strings.TrimSpace(rawValue))
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return data, nil
}

func ensureBackendTOMLSection(root map[string]any, dotted string) map[string]any {
	current := root
	for _, part := range strings.Split(dotted, ".") {
		part = strings.TrimSpace(part)
		next, ok := current[part].(map[string]any)
		if !ok {
			next = map[string]any{}
			current[part] = next
		}
		current = next
	}
	return current
}

func stripBackendTOMLComment(line string) string {
	quote := rune(0)
	for index, char := range line {
		if quote != 0 {
			if char == quote && (quote != '"' || index == 0 || line[index-1] != '\\') {
				quote = 0
			}
			continue
		}
		if char == '"' || char == '\'' {
			quote = char
			continue
		}
		if char == '#' {
			return strings.TrimSpace(line[:index])
		}
	}
	return line
}

func parseBackendTOMLValue(raw string) any {
	if len(raw) >= 2 && raw[0] == '[' && raw[len(raw)-1] == ']' {
		return parseBackendTOMLArray(raw[1 : len(raw)-1])
	}
	if len(raw) >= 2 && raw[0] == '"' && raw[len(raw)-1] == '"' {
		if unquoted, err := strconv.Unquote(raw); err == nil {
			return unquoted
		}
		return raw[1 : len(raw)-1]
	}
	if len(raw) >= 2 && raw[0] == '\'' && raw[len(raw)-1] == '\'' {
		return raw[1 : len(raw)-1]
	}
	switch strings.ToLower(raw) {
	case "true":
		return true
	case "false":
		return false
	}
	if strings.ContainsAny(raw, ".eE") {
		if value, err := strconv.ParseFloat(raw, 64); err == nil {
			return value
		}
	}
	if value, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return value
	}
	return raw
}

func parseBackendTOMLArray(raw string) []any {
	values := []any{}
	for _, item := range splitBackendTOMLArrayItems(raw) {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		values = append(values, parseBackendTOMLValue(item))
	}
	return values
}

func splitBackendTOMLArrayItems(raw string) []string {
	items := []string{}
	start := 0
	depth := 0
	quote := byte(0)
	for index := 0; index < len(raw); index++ {
		char := raw[index]
		if quote != 0 {
			if char == quote && (quote != '"' || index == 0 || raw[index-1] != '\\') {
				quote = 0
			}
			continue
		}
		switch char {
		case '"', '\'':
			quote = char
		case '[':
			depth++
		case ']':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				items = append(items, raw[start:index])
				start = index + 1
			}
		}
	}
	items = append(items, raw[start:])
	return items
}

func writeBackendTOML(w io.Writer, data map[string]any) error {
	return writeBackendTOMLMap(w, "", data)
}

func writeBackendTOMLMap(w io.Writer, prefix string, data map[string]any) error {
	scalars, sections := splitBackendTOMLKeys(data)
	if prefix != "" {
		if _, err := fmt.Fprintf(w, "[%s]\n", prefix); err != nil {
			return err
		}
	}
	for _, key := range scalars {
		if _, err := fmt.Fprintf(w, "%s = %s\n", key, formatBackendTOMLValue(data[key])); err != nil {
			return err
		}
	}
	for _, key := range sections {
		if prefix != "" || len(scalars) > 0 {
			if _, err := fmt.Fprintln(w); err != nil {
				return err
			}
		}
		nested := data[key].(map[string]any)
		nextPrefix := key
		if prefix != "" {
			nextPrefix = prefix + "." + key
		}
		if err := writeBackendTOMLMap(w, nextPrefix, nested); err != nil {
			return err
		}
	}
	return nil
}

func splitBackendTOMLKeys(data map[string]any) ([]string, []string) {
	scalars := []string{}
	sections := []string{}
	for key, value := range data {
		if _, ok := value.(map[string]any); ok {
			sections = append(sections, key)
			continue
		}
		scalars = append(scalars, key)
	}
	sort.Strings(scalars)
	sort.Strings(sections)
	return scalars, sections
}

func formatBackendTOMLValue(value any) string {
	switch typed := value.(type) {
	case string:
		return strconv.Quote(typed)
	case bool:
		if typed {
			return "true"
		}
		return "false"
	case int:
		return strconv.FormatInt(int64(typed), 10)
	case int64:
		return strconv.FormatInt(typed, 10)
	case int32:
		return strconv.FormatInt(int64(typed), 10)
	case float64:
		return strconv.FormatFloat(typed, 'g', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(typed), 'g', -1, 32)
	default:
		if formatted, ok := formatBackendTOMLArray(typed); ok {
			return formatted
		}
		return strconv.Quote(fmt.Sprint(typed))
	}
}

func formatBackendTOMLArray(value any) (string, bool) {
	reflected := reflect.ValueOf(value)
	if !reflected.IsValid() {
		return "", false
	}
	if reflected.Kind() != reflect.Slice && reflected.Kind() != reflect.Array {
		return "", false
	}
	parts := make([]string, 0, reflected.Len())
	for index := 0; index < reflected.Len(); index++ {
		parts = append(parts, formatBackendTOMLValue(reflected.Index(index).Interface()))
	}
	return "[" + strings.Join(parts, ", ") + "]", true
}
