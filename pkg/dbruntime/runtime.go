// Package dbruntime is the catalog of database instance sizes (CPU, memory,
// and disk) per engine. It is separate from app process runtimes
// (host/cli/runtime_profile.go).
//
// Each engine ships small, medium, and large presets. Those numbers are
// independent: Redis small disk is smaller than Postgres small disk.
// Cluster admins persist creates, updates, and removals in a host JSON file
// (see DefaultPath). There is no controller table. Updating a definition
// does not resize instances already created from it; there is no in-place
// resize.
package dbruntime

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/docker/go-units"
)

const (
	EnginePostgres   = "postgres"
	EngineRedis      = "redis"
	EngineMariaDB    = "mariadb"
	EngineMongoDB    = "mongodb"
	EngineKafka      = "kafka"
	EngineClickHouse = "clickhouse"

	DefaultRuntime = "small"
)

const (
	kib int64 = 1024
	mib int64 = 1024 * kib
	gib int64 = 1024 * mib
)

// Engines is the stable engine order used by the catalog and CLI.
var Engines = []string{
	EnginePostgres,
	EngineRedis,
	EngineMariaDB,
	EngineMongoDB,
	EngineKafka,
	EngineClickHouse,
}

// Size is the CPU, memory, and disk captured when an instance is provisioned.
// CPU is milliCPU. Memory and disk are bytes.
type Size struct {
	CPU    int64 `json:"cpu"`
	Memory int64 `json:"memory"`
	Disk   int64 `json:"disk"`
}

// Runtime is one named size for one engine.
type Runtime struct {
	Name    string `json:"name"`
	Engine  string `json:"engine"`
	CPU     int64  `json:"cpu"`
	Memory  int64  `json:"memory"`
	Disk    int64  `json:"disk"`
	Builtin bool   `json:"builtin"`
}

// Size returns the provision-time dimensions of r.
func (r Runtime) Size() Size {
	return Size{CPU: r.CPU, Memory: r.Memory, Disk: r.Disk}
}

// Catalog is the published database runtimes plus whether raw sizes are allowed.
type Catalog struct {
	AllowCustomSizes bool      `json:"allow_custom_sizes"`
	Runtimes         []Runtime `json:"runtimes"`
}

// ErrCustomSizesDisabled is returned when a tenant sets raw CPU, memory, or
// disk and an admin has not allowed custom sizes.
var ErrCustomSizesDisabled = errors.New("custom CPU, memory, and disk are disabled; choose a published database runtime with --runtime (default small) or ask a cluster admin to run flynn-host db-runtime:allow-custom")

// ErrBuiltinRemove is returned when removing small, medium, or large.
var ErrBuiltinRemove = errors.New("builtin database runtimes cannot be removed")

// UnpublishedError means the runtime name is not in the catalog for that engine.
// A tenant cannot provision an unpublished size.
type UnpublishedError struct {
	Engine      string
	Name        string
	AllowCustom bool
}

func (e *UnpublishedError) Error() string {
	if e.AllowCustom {
		return fmt.Sprintf("database runtime %q is not published for %s; pass cpu, memory, and disk to request a custom size", e.Name, e.Engine)
	}
	return fmt.Sprintf("database runtime %q is not published for %s; custom sizes are disabled", e.Name, e.Engine)
}

var nameRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

var engineOrder = map[string]int{}

func init() {
	for i, e := range Engines {
		engineOrder[e] = i
	}
}

// BuiltinCatalog is the in-memory default list. Disk differs per engine.
func BuiltinCatalog() Catalog {
	return Catalog{Runtimes: builtinRuntimes()}
}

func builtinRuntimes() []Runtime {
	type row struct {
		engine              string
		cpuS, cpuM, cpuL    int64
		memS, memM, memL    int64
		diskS, diskM, diskL int64
	}
	rows := []row{
		{EnginePostgres, 500, 1000, 2000, 512 * mib, 1 * gib, 2 * gib, 10 * gib, 50 * gib, 100 * gib},
		{EngineRedis, 250, 500, 1000, 256 * mib, 512 * mib, 1 * gib, 1 * gib, 5 * gib, 10 * gib},
		{EngineMariaDB, 500, 1000, 2000, 512 * mib, 1 * gib, 2 * gib, 8 * gib, 32 * gib, 80 * gib},
		{EngineMongoDB, 500, 1000, 2000, 1 * gib, 2 * gib, 4 * gib, 16 * gib, 64 * gib, 200 * gib},
		{EngineKafka, 1000, 2000, 4000, 1 * gib, 2 * gib, 4 * gib, 20 * gib, 100 * gib, 500 * gib},
		{EngineClickHouse, 1000, 2000, 4000, 2 * gib, 4 * gib, 8 * gib, 32 * gib, 128 * gib, 500 * gib},
	}
	out := make([]Runtime, 0, len(rows)*3)
	for _, r := range rows {
		out = append(out,
			Runtime{Name: "small", Engine: r.engine, CPU: r.cpuS, Memory: r.memS, Disk: r.diskS, Builtin: true},
			Runtime{Name: "medium", Engine: r.engine, CPU: r.cpuM, Memory: r.memM, Disk: r.diskM, Builtin: true},
			Runtime{Name: "large", Engine: r.engine, CPU: r.cpuL, Memory: r.memL, Disk: r.diskL, Builtin: true},
		)
	}
	return out
}

// ProviderEngine maps a resource:add provider to a catalog engine.
// mysql is the MariaDB provider name.
func ProviderEngine(provider string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "postgres", "postgresql":
		return EnginePostgres, true
	case "mysql", "mariadb":
		return EngineMariaDB, true
	case EngineRedis:
		return EngineRedis, true
	case EngineMongoDB:
		return EngineMongoDB, true
	case EngineKafka:
		return EngineKafka, true
	case EngineClickHouse:
		return EngineClickHouse, true
	default:
		return "", false
	}
}

// NormalizeEngine returns the canonical engine name.
func NormalizeEngine(engine string) (string, error) {
	e, ok := ProviderEngine(engine)
	if !ok {
		return "", fmt.Errorf("engine must be one of postgres, redis, mariadb, mongodb, kafka, clickhouse")
	}
	return e, nil
}

// ValidateName checks a runtime name.
func ValidateName(name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("runtime name is required")
	}
	if !nameRE.MatchString(name) {
		return fmt.Errorf("runtime name %q must match %s", name, nameRE.String())
	}
	return nil
}

// Validate checks name, engine, cpu, memory, and disk.
func Validate(r Runtime) error {
	if _, err := NormalizeEngine(r.Engine); err != nil {
		return err
	}
	if err := ValidateName(r.Name); err != nil {
		return err
	}
	if r.CPU <= 0 {
		return fmt.Errorf("cpu must be greater than zero")
	}
	if r.Memory <= 0 {
		return fmt.Errorf("memory must be greater than zero")
	}
	if r.Disk <= 0 {
		return fmt.Errorf("disk must be greater than zero")
	}
	return nil
}

// ParseCPU parses a milliCPU value ("500" or "500m").
func ParseCPU(s string) (int64, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(strings.ToLower(s), "m")
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("cpu must be a positive milliCPU value")
	}
	return n, nil
}

// ParseBytes parses a memory or disk size ("512MB", "10GB", or a byte count).
func ParseBytes(s string) (int64, error) {
	n, err := units.RAMInBytes(strings.TrimSpace(s))
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("size must be a positive byte value")
	}
	return n, nil
}

// Find returns the runtime for engine and name.
func (c Catalog) Find(engine, name string) (Runtime, bool) {
	eng, err := NormalizeEngine(engine)
	if err != nil {
		return Runtime{}, false
	}
	name = strings.ToLower(strings.TrimSpace(name))
	for _, r := range c.Runtimes {
		if r.Engine == eng && r.Name == name {
			return r, true
		}
	}
	return Runtime{}, false
}

// List returns runtimes ordered by engine, then small/medium/large, then name.
func (c Catalog) List() []Runtime {
	out := append([]Runtime(nil), c.Runtimes...)
	rank := map[string]int{"small": 0, "medium": 1, "large": 2}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Engine != out[j].Engine {
			return engineOrder[out[i].Engine] < engineOrder[out[j].Engine]
		}
		ri, rj := 10, 10
		if v, ok := rank[out[i].Name]; ok {
			ri = v
		}
		if v, ok := rank[out[j].Name]; ok {
			rj = v
		}
		if ri != rj {
			return ri < rj
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Resolve returns the size to use when provisioning engine.
// An empty name selects small. name must already be published in catalog.
// allowCustom does not invent a size for an unknown name; it only changes
// the error so a caller that also accepts raw numbers can tell the tenant
// those numbers are allowed. Raw numbers go through ResolveProvision.
func Resolve(engine, name string, catalog Catalog, allowCustom bool) (Size, error) {
	eng, err := NormalizeEngine(engine)
	if err != nil {
		return Size{}, err
	}
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		name = DefaultRuntime
	}
	if err := ValidateName(name); err != nil {
		return Size{}, err
	}
	rt, ok := catalog.Find(eng, name)
	if !ok {
		return Size{}, &UnpublishedError{Engine: eng, Name: name, AllowCustom: allowCustom}
	}
	if err := Validate(rt); err != nil {
		return Size{}, err
	}
	return rt.Size(), nil
}

// ResolveProvision sizes a new instance. Any non-zero custom field is a raw
// size and is rejected unless allowCustom is set. Otherwise the published
// runtime is used (default small).
func ResolveProvision(engine, name string, custom Size, catalog Catalog, allowCustom bool) (Size, string, error) {
	if custom.CPU != 0 || custom.Memory != 0 || custom.Disk != 0 {
		if !allowCustom {
			return Size{}, "", ErrCustomSizesDisabled
		}
		if custom.CPU <= 0 || custom.Memory <= 0 || custom.Disk <= 0 {
			return Size{}, "", fmt.Errorf("custom size requires cpu, memory, and disk")
		}
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" {
			name = "custom"
		}
		if err := ValidateName(name); err != nil {
			return Size{}, "", err
		}
		return custom, name, nil
	}
	sz, err := Resolve(engine, name, catalog, allowCustom)
	if err != nil {
		return Size{}, "", err
	}
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		name = DefaultRuntime
	}
	return sz, name, nil
}

// SizeAfterDefinitionUpdate returns the size stored on an instance at
// provision time. Updating the runtime definition leaves that size unchanged.
// Flynn has no in-place database resize; provision a new resource to get a
// different size.
func SizeAfterDefinitionUpdate(instance Size, _ Runtime) Size {
	return instance
}

// Create adds a custom runtime. Builtin names on that engine stay reserved.
func (c *Catalog) Create(r Runtime) error {
	eng, err := NormalizeEngine(r.Engine)
	if err != nil {
		return err
	}
	r.Engine = eng
	r.Name = strings.ToLower(strings.TrimSpace(r.Name))
	r.Builtin = false
	if err := Validate(r); err != nil {
		return err
	}
	if _, ok := c.Find(r.Engine, r.Name); ok {
		return fmt.Errorf("database runtime %s/%s already exists", r.Engine, r.Name)
	}
	c.Runtimes = append(c.Runtimes, r)
	return nil
}

// UpdateFields are the optional edits for Update. Nil leaves the field as-is.
type UpdateFields struct {
	Name   *string
	CPU    *int64
	Memory *int64
	Disk   *int64
}

// Update edits a published runtime. Builtin small/medium/large can change
// CPU, memory, and disk, but not their names. The returned runtime is the
// new definition only; existing instances keep the size from provision time
// (see SizeAfterDefinitionUpdate).
func (c *Catalog) Update(engine, name string, f UpdateFields) (Runtime, error) {
	eng, err := NormalizeEngine(engine)
	if err != nil {
		return Runtime{}, err
	}
	name = strings.ToLower(strings.TrimSpace(name))
	idx := -1
	for i, r := range c.Runtimes {
		if r.Engine == eng && r.Name == name {
			idx = i
			break
		}
	}
	if idx < 0 {
		return Runtime{}, &UnpublishedError{Engine: eng, Name: name, AllowCustom: c.AllowCustomSizes}
	}
	r := c.Runtimes[idx]
	if f.Name != nil {
		next := strings.ToLower(strings.TrimSpace(*f.Name))
		if next != r.Name {
			if r.Builtin {
				return Runtime{}, fmt.Errorf("builtin database runtime %s/%s cannot be renamed", r.Engine, r.Name)
			}
			if err := ValidateName(next); err != nil {
				return Runtime{}, err
			}
			if _, ok := c.Find(r.Engine, next); ok {
				return Runtime{}, fmt.Errorf("database runtime %s/%s already exists", r.Engine, next)
			}
			r.Name = next
		}
	}
	if f.CPU != nil {
		r.CPU = *f.CPU
	}
	if f.Memory != nil {
		r.Memory = *f.Memory
	}
	if f.Disk != nil {
		r.Disk = *f.Disk
	}
	if err := Validate(r); err != nil {
		return Runtime{}, err
	}
	c.Runtimes[idx] = r
	return r, nil
}

// Remove deletes a custom runtime. Builtin presets cannot be removed.
func (c *Catalog) Remove(engine, name string) error {
	eng, err := NormalizeEngine(engine)
	if err != nil {
		return err
	}
	name = strings.ToLower(strings.TrimSpace(name))
	idx := -1
	for i, r := range c.Runtimes {
		if r.Engine == eng && r.Name == name {
			idx = i
			break
		}
	}
	if idx < 0 {
		return &UnpublishedError{Engine: eng, Name: name, AllowCustom: c.AllowCustomSizes}
	}
	if c.Runtimes[idx].Builtin {
		return ErrBuiltinRemove
	}
	c.Runtimes = append(c.Runtimes[:idx], c.Runtimes[idx+1:]...)
	return nil
}
