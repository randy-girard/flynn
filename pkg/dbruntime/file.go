package dbruntime

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

const (
	// DefaultPath is the host catalog. It is not a controller table.
	// A missing file means the builtin presets and custom sizes disabled.
	DefaultPath = "/etc/flynn/db-runtimes.json"

	// EnvFile overrides DefaultPath for the host CLI and for flynn resource:add.
	EnvFile = "FLYNN_DB_RUNTIMES"
)

// Path is EnvFile when set, otherwise DefaultPath.
func Path() string {
	if p := os.Getenv(EnvFile); p != "" {
		return p
	}
	return DefaultPath
}

// Load reads a catalog file. A missing file returns BuiltinCatalog.
// Builtin small/medium/large stay present; file entries override their
// CPU, memory, and disk. Other entries are custom runtimes.
func Load(path string) (Catalog, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return BuiltinCatalog(), nil
		}
		return Catalog{}, err
	}
	var file Catalog
	if err := json.Unmarshal(raw, &file); err != nil {
		return Catalog{}, err
	}
	out := BuiltinCatalog()
	out.AllowCustomSizes = file.AllowCustomSizes
	for _, r := range file.Runtimes {
		eng, err := NormalizeEngine(r.Engine)
		if err != nil {
			return Catalog{}, err
		}
		r.Engine = eng
		r.Name = strings.ToLower(strings.TrimSpace(r.Name))
		if err := Validate(r); err != nil {
			return Catalog{}, err
		}
		if i := indexOf(out.Runtimes, r.Engine, r.Name); i >= 0 && out.Runtimes[i].Builtin {
			out.Runtimes[i].CPU = r.CPU
			out.Runtimes[i].Memory = r.Memory
			out.Runtimes[i].Disk = r.Disk
			continue
		}
		r.Builtin = false
		if indexOf(out.Runtimes, r.Engine, r.Name) >= 0 {
			return Catalog{}, errors.New("duplicate database runtime " + r.Engine + "/" + r.Name)
		}
		out.Runtimes = append(out.Runtimes, r)
	}
	return out, nil
}

// Save writes the full catalog, including builtins, so the next Load is stable.
func Save(path string, c Catalog) error {
	c.Runtimes = c.List()
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func indexOf(list []Runtime, engine, name string) int {
	for i, r := range list {
		if r.Engine == engine && r.Name == name {
			return i
		}
	}
	return -1
}
