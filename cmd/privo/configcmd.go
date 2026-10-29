package main

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"privo.club/privo/internal/config"
)

// configSource is where the settings of this run come from: the environment
// and, when one was named, a configuration file (ADR 0015).
type configSource struct {
	path string
	env  func(string) string
	file config.FileValues
}

// loadSource reads the configuration file when path is not empty. A file that
// was named and cannot be used is an error: nothing falls back to defaults.
func loadSource(path string, env func(string) string) (configSource, error) {
	src := configSource{path: path, env: env}
	if path == "" {
		return src, nil
	}
	file, err := config.ReadFile(path)
	if err != nil {
		return configSource{}, err
	}
	src.file = file
	return src, nil
}

// get is the lookup every setting is read through: environment, then file.
func (s configSource) get(name string) string { return config.Layer(s.env, s.file)(name) }

// configFlag takes a leading --config FILE or --config=FILE off args.
func configFlag(args []string) (string, []string, error) {
	if len(args) == 0 {
		return "", args, nil
	}
	switch a := args[0]; {
	case a == "--config":
		if len(args) < 2 || strings.TrimSpace(args[1]) == "" {
			return "", nil, errors.New("--config needs the path of a configuration file")
		}
		return strings.TrimSpace(args[1]), args[2:], nil
	case strings.HasPrefix(a, "--config="):
		p := strings.TrimSpace(strings.TrimPrefix(a, "--config="))
		if p == "" {
			return "", nil, errors.New("--config needs the path of a configuration file")
		}
		return p, args[1:], nil
	}
	return "", args, nil
}

// configCmd implements `privo config show`: every setting, its effective value
// and where it came from, then whatever is wrong with them. Secrets are shown
// only as set or unset.
func configCmd(args []string, stdout, stderr io.Writer, src configSource) int {
	if len(args) != 1 || args[0] != "show" {
		_, _ = fmt.Fprintln(stderr, "usage: privo config show")
		return 2
	}
	file := "(none)"
	if src.path != "" {
		file = src.path
	}
	_, _ = fmt.Fprintf(stdout, "config file: %s\n\n", file)
	tw := tabwriter.NewWriter(stdout, 0, 8, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "SETTING\tVALUE\tSOURCE")
	for _, e := range config.Describe(src.env, src.file) {
		v := e.Value
		if v == "" {
			v = "-"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n", e.Name, v, e.Source)
	}
	_ = tw.Flush()

	var problems []error
	if _, err := config.Load(src.get); err != nil {
		problems = append(problems, err)
	}
	if _, err := config.LoadBootstrapToken(src.get); err != nil && !errors.Is(err, config.ErrBootstrapUnreadable) {
		problems = append(problems, err)
	}
	if len(problems) > 0 {
		_, _ = fmt.Fprintf(stderr, "privo: invalid configuration:\n%v\n", errors.Join(problems...))
		return 2
	}
	return 0
}
