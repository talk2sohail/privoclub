package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"go.yaml.in/yaml/v3"
)

// MaxFileBytes is the largest configuration file that is read. A file of
// settings is a few hundred bytes; the limit keeps a wrong path (a log, a
// database) from being slurped into memory.
const MaxFileBytes = 64 << 10

// FileValues holds the settings of a configuration file, keyed by variable
// name (PRIVO_LISTEN), as text.
type FileValues map[string]string

// ReadFile reads and checks a configuration file (ADR 0015). It reports every
// problem at once, naming the file and the key and never a value, so a secret
// pasted into the wrong place is not repeated in a log.
func ReadFile(path string) (FileValues, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("config file: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("config file %s: not a regular file", path)
	}
	f, err := os.Open(path) //nolint:gosec // operator-supplied path is the point
	if err != nil {
		return nil, fmt.Errorf("config file: %w", err)
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, MaxFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("config file %s: %w", path, err)
	}
	if len(b) > MaxFileBytes {
		return nil, fmt.Errorf("config file %s: larger than %d KiB", path, MaxFileBytes>>10)
	}
	return parseFile(path, b)
}

func parseFile(path string, b []byte) (FileValues, error) {
	dec := yaml.NewDecoder(bytes.NewReader(b))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) { // nothing but comments, or nothing at all
			return FileValues{}, nil
		}
		return nil, fmt.Errorf("config file %s: not valid yaml: %w", path, err)
	}
	var next yaml.Node
	switch err := dec.Decode(&next); {
	case err == nil:
		return nil, fmt.Errorf("config file %s: more than one document; keep the settings in one", path)
	case !errors.Is(err, io.EOF):
		return nil, fmt.Errorf("config file %s: not valid yaml: %w", path, err)
	}
	root := &doc
	if root.Kind == yaml.DocumentNode && len(root.Content) == 1 {
		root = root.Content[0]
	}
	if root.Kind == yaml.ScalarNode && root.Tag == "!!null" {
		return FileValues{}, nil
	}
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("config file %s: line %d: the file must be a mapping of setting names to values", path, root.Line)
	}

	out := FileValues{}
	seen := map[string]bool{}
	var problems []string
	add := func(line int, format string, args ...any) {
		problems = append(problems, fmt.Sprintf("line %d: ", line)+fmt.Sprintf(format, args...))
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		k, v := root.Content[i], root.Content[i+1]
		if k.Kind != yaml.ScalarNode {
			add(k.Line, "a setting name must be plain text")
			continue
		}
		key := k.Value
		if seen[key] {
			add(k.Line, "%q appears twice", key)
			continue
		}
		seen[key] = true
		s, ok := byKey(key)
		switch {
		case !ok:
			add(k.Line, "unknown setting %q", key)
			continue
		case s.Secret:
			add(k.Line, "%q is a secret and cannot be set in the file; set %q to the path of a file that holds it", key, Setting{Name: s.Name + "_FILE"}.Key())
			continue
		case s.NoFile:
			add(k.Line, "%q cannot be set in the file; it names the file (use --config or the %s variable)", key, s.Name)
			continue
		}
		switch {
		case v.Kind == yaml.AliasNode:
			add(v.Line, "%q: an alias is not supported; write the value out", key)
		case v.Kind != yaml.ScalarNode:
			add(v.Line, "%q must be a single value, not a list or a mapping", key)
		case v.Tag == "!!null":
			// "listen:" with nothing after it is the same as no line.
		default:
			out[s.Name] = v.Value
		}
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("config file %s:\n  %s", path, strings.Join(problems, "\n  "))
	}
	return out, nil
}

// Layer returns a lookup that reads the environment first and then the file.
// A variable that is empty or only spaces counts as unset. Names the file
// cannot hold, secrets among them, are read from the environment as before.
func Layer(env func(string) string, file FileValues) func(string) string {
	return func(name string) string {
		if v := env(name); strings.TrimSpace(v) != "" {
			return v
		}
		if v, ok := file[name]; ok {
			return v
		}
		return env(name)
	}
}

// Source names where a setting's effective value comes from.
const (
	SourceDefault     = "default"
	SourceFile        = "file"
	SourceEnvironment = "environment"
)

// Entry is one row of `privo config show`.
type Entry struct {
	Name string
	// Value is the effective text, empty when the default applies. A secret is
	// only ever "set" or "unset".
	Value  string
	Source string
}

// Describe lists every registered setting with its effective value and source.
func Describe(env func(string) string, file FileValues) []Entry {
	rows := make([]Entry, 0, len(Settings))
	for _, s := range Settings {
		e := Entry{Name: s.Name, Source: SourceDefault}
		if v := env(s.Name); strings.TrimSpace(v) != "" {
			e.Value, e.Source = v, SourceEnvironment
		} else if v, ok := file[s.Name]; ok && strings.TrimSpace(v) != "" {
			e.Value, e.Source = v, SourceFile
		}
		if s.Secret {
			e.Value = "unset"
			if e.Source != SourceDefault {
				e.Value = "set"
			}
		}
		rows = append(rows, e)
	}
	return rows
}
