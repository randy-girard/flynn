package dbruntime

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

const (
	// DefaultPath is the host catalog cache. The controller table is the
	// cluster source of truth. A missing file is an empty catalog.
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

// Load reads a catalog file. A missing file is empty: database plugins add
// their engine presets on install. File rows keep builtin when they are the
// small/medium/large names for that engine.
func Load(path string) (Catalog, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return EmptyCatalog(), nil
		}
		return Catalog{}, err
	}
	var file Catalog
	if err := json.Unmarshal(raw, &file); err != nil {
		return Catalog{}, err
	}
	out := Catalog{AllowCustomSizes: file.AllowCustomSizes}
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
		if indexOf(out.Runtimes, r.Engine, r.Name) >= 0 {
			return Catalog{}, errors.New("duplicate database runtime " + r.Engine + "/" + r.Name)
		}
		if isBuiltinName(r.Name) {
			r.Builtin = true
		}
		out.Runtimes = append(out.Runtimes, r)
	}
	return out, nil
}

func isBuiltinName(name string) bool {
	switch name {
	case "small", "medium", "large":
		return true
	}
	return false
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
		if storedEngineMatches(r.Engine, engine) && r.Name == name {
			return i
		}
	}
	return -1
}
