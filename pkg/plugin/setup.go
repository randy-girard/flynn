package plugin

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/randy-girard/flynn/pkg/random"
)

const nonInteractiveEnv = "FLYNN_PLUGIN_NONINTERACTIVE"

// ExpandClusterVars replaces ${KEY} with cluster[KEY] (no nested expansion).
func ExpandClusterVars(s string, cluster map[string]string) string {
	if s == "" || cluster == nil {
		return s
	}
	out := s
	for k, v := range cluster {
		out = strings.ReplaceAll(out, "${"+k+"}", v)
	}
	return out
}

func setupEnvOverride(envKey string) string {
	if v := strings.TrimSpace(os.Getenv("FLYNN_PLUGIN_SETUP_" + envKey)); v != "" {
		return v
	}
	return strings.TrimSpace(os.Getenv(envKey))
}

func (in *Installer) interactive() bool {
	if in.Interactive != nil {
		return in.Interactive()
	}
	if strings.EqualFold(os.Getenv(nonInteractiveEnv), "1") ||
		strings.EqualFold(os.Getenv(nonInteractiveEnv), "true") {
		return false
	}
	f, ok := in.reader().(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	if err != nil {
		return false
	}
	return st.Mode()&os.ModeCharDevice != 0
}

func (in *Installer) reader() io.Reader {
	if in.Stdin != nil {
		return in.Stdin
	}
	return os.Stdin
}

func (in *Installer) writer() io.Writer {
	if in.Stdout != nil {
		return in.Stdout
	}
	return os.Stdout
}

// applySetup fills cluster with answers from setup prompts. Interactive
// installs ask on a TTY; otherwise Default/Generate/env overrides are used.
func (in *Installer) applySetup(m *Manifest, cluster map[string]string) error {
	if m == nil || len(m.Setup) == 0 {
		return nil
	}
	ask := in.interactive()
	inr := bufio.NewReader(in.reader())
	for _, p := range m.Setup {
		if !setupWhenMatches(p.When, cluster) {
			continue
		}
		key := strings.TrimSpace(p.Env)
		if cluster[key] != "" {
			if _, err := applySetupChoice(key, cluster[key], p.Choices); err != nil {
				return err
			}
			continue
		}
		def := ExpandClusterVars(p.Default, cluster)
		val := setupEnvOverride(key)
		if val == "" && ask {
			label := p.Prompt
			if label == "" {
				label = key
			}
			hint := def
			if hint == "" && len(p.Choices) > 0 {
				hint = strings.Join(p.Choices, "/")
			}
			if p.Generate && hint == "" {
				hint = "generated"
			}
			if hint != "" {
				fmt.Fprintf(in.writer(), "%s [%s]: ", label, hint)
			} else {
				fmt.Fprintf(in.writer(), "%s: ", label)
			}
			line, err := inr.ReadString('\n')
			if err != nil && err != io.EOF {
				return fmt.Errorf("setup %s: %w", key, err)
			}
			val = strings.TrimSpace(line)
		}
		if val == "" {
			val = def
		}
		if val == "" && p.Generate {
			val = random.Hex(16)
			if !p.Secret {
				in.logf("generated %s=%s", key, val)
			} else {
				in.logf("generated %s (hidden)", key)
			}
		}
		if canonical, err := applySetupChoice(key, val, p.Choices); err != nil {
			return err
		} else if canonical != "" {
			val = canonical
		}
		if val == "" && !p.Optional {
			return fmt.Errorf("setup %s: required (set FLYNN_PLUGIN_SETUP_%s, pass a TTY, or give setup.default)", key, key)
		}
		if val != "" {
			cluster[key] = val
		}
	}
	return nil
}

func setupWhenMatches(when string, cluster map[string]string) bool {
	when = strings.TrimSpace(when)
	if when == "" {
		return true
	}
	if strings.HasPrefix(when, "!") {
		key := strings.TrimSpace(strings.TrimPrefix(when, "!"))
		return strings.TrimSpace(cluster[key]) == ""
	}
	key, want, ok := strings.Cut(when, "=")
	got := strings.TrimSpace(cluster[strings.TrimSpace(key)])
	if !ok {
		return got != ""
	}
	return strings.EqualFold(got, strings.TrimSpace(want))
}

func applySetupChoice(key, val string, choices []string) (string, error) {
	val = strings.TrimSpace(val)
	if val == "" || len(choices) == 0 {
		return val, nil
	}
	for _, c := range choices {
		if strings.EqualFold(val, strings.TrimSpace(c)) {
			return strings.TrimSpace(c), nil
		}
	}
	return "", fmt.Errorf("setup %s: %q is not one of %s", key, val, strings.Join(choices, ", "))
}
