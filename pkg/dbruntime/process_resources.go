package dbruntime

import (
	"errors"
	"fmt"
	"strconv"

	units "github.com/docker/go-units"
	"github.com/randy-girard/flynn/host/resource"
)

// MinCPU is the smallest milliCPU a database instance may be given.
const MinCPU int64 = 100

// MinMemory is the smallest memory limit in bytes an instance of engine
// may be given. Below it the engine does not start reliably with the
// settings EngineMemoryEnv derives (JVM heap, WiredTiger cache, ClickHouse
// server memory). Zero for an unknown engine.
func MinMemory(engine string) int64 {
	eng, err := NormalizeEngine(engine)
	if err != nil {
		return 0
	}
	switch eng {
	case EngineRedis:
		return 64 * mib
	case EnginePostgres:
		return 128 * mib
	case EngineMySQL:
		return 256 * mib
	case EngineMongoDB, EngineKafka:
		return 512 * mib
	case EngineClickHouse:
		return 1 * gib
	}
	return 0
}

// ValidateSize checks a CPU and memory limit against the engine minimums.
// Zero fields are not checked.
func ValidateSize(engine string, cpu, memory int64) error {
	if cpu < 0 || memory < 0 {
		return errors.New("cpu and memory must be positive values")
	}
	if cpu > 0 && cpu < MinCPU {
		return fmt.Errorf("cpu must be at least %d milliCPU", MinCPU)
	}
	if min := MinMemory(engine); memory > 0 && memory < min {
		eng, _ := NormalizeEngine(engine)
		return fmt.Errorf("%s memory must be at least %s", eng, units.BytesSize(float64(min)))
	}
	return nil
}

// ProcessResourcesFor is the CPU and memory limits for a new instance's
// process type. cpu (milliCPU) and memory (bytes) are the provider config
// values, which addons:create sends for both published runtimes and raw
// --cpu/--memory. It is VolumeSize's rule for CPU and memory:
//
//   - a published runtime gives its own CPU and memory when a requested
//     value is zero or matches it;
//   - any other value is a custom size and needs AllowCustomSizes (checked
//     here as well as in the CLI, because the provider config comes from
//     the tenant). An omitted field comes from the published runtime, or
//     from the engine's default runtime when name is not published;
//   - values below MinCPU or MinMemory are refused.
//
// A dedicated runtime reserves its CPU and memory on the host; a custom size
// shares host capacity. Nil (no error) when there is no catalog or the name
// is not published and nothing was requested: the controller default applies.
func ProcessResourcesFor(cat *Catalog, engine, name string, cpu, memory int64) (resource.Resources, error) {
	if cpu < 0 || memory < 0 {
		return nil, errors.New("cpu and memory must be positive values")
	}
	if cat == nil {
		if cpu > 0 || memory > 0 {
			return nil, errors.New("database runtime catalog unavailable; cannot apply a custom cpu or memory")
		}
		return nil, nil
	}
	base, published := cat.Find(engine, name)
	if published && (cpu == 0 || cpu == base.CPU) && (memory == 0 || memory == base.Memory) {
		if base.CPU <= 0 || base.Memory <= 0 {
			return nil, nil
		}
		r := make(resource.Resources)
		resource.ApplyNamedLimitsWithReserve(r, base.Memory, base.CPU, base.Dedicated)
		return r, nil
	}
	if cpu == 0 && memory == 0 {
		return nil, nil
	}
	if !cat.AllowCustomSizes {
		return nil, ErrCustomSizesDisabled
	}
	if !published {
		base, published = cat.Find(engine, DefaultRuntime)
		if !published {
			if b, err := EngineBuiltins(engine); err == nil {
				base = b[0]
			}
		}
	}
	if cpu == 0 {
		cpu = base.CPU
	}
	if memory == 0 {
		memory = base.Memory
	}
	if cpu <= 0 || memory <= 0 {
		return nil, errors.New("custom size requires cpu and memory")
	}
	if err := ValidateSize(engine, cpu, memory); err != nil {
		return nil, err
	}
	r := make(resource.Resources)
	resource.ApplyNamedLimitsWithReserve(r, memory, cpu, false)
	return r, nil
}

// MemoryLimitOf is the memory limit in bytes in r, or zero.
func MemoryLimitOf(r resource.Resources) int64 {
	if r == nil {
		return 0
	}
	if s, ok := r[resource.TypeMemory]; ok && s.Limit != nil {
		return *s.Limit
	}
	return 0
}

// Release env keys EngineMemoryEnv sets on an instance release. The engine
// side reads them when it writes its config, so a larger limit is used and
// a smaller one does not start the engine with settings that do not fit.
const (
	// EnvMemoryLimit is the configured memory limit in bytes (every engine).
	// The container's own cgroup memory.max is twice that (flynn-host's
	// hard limit), so engines must not size themselves from the cgroup.
	EnvMemoryLimit = "FLYNN_DB_MEMORY_LIMIT"
	// EnvSharedBuffers and EnvEffectiveCacheSize are postgres settings
	// ("128MB").
	EnvSharedBuffers      = "FLYNN_DB_SHARED_BUFFERS"
	EnvEffectiveCacheSize = "FLYNN_DB_EFFECTIVE_CACHE_SIZE"
	// EnvInnoDBBufferPoolSize is the MariaDB innodb_buffer_pool_size in bytes.
	EnvInnoDBBufferPoolSize = "FLYNN_DB_INNODB_BUFFER_POOL_SIZE"
	// EnvRedisMaxMemory is the redis maxmemory in bytes.
	EnvRedisMaxMemory = "FLYNN_DB_MAXMEMORY"
	// EnvWiredTigerCacheGB is the mongod storage.wiredTiger.engineConfig.cacheSizeGB.
	EnvWiredTigerCacheGB = "FLYNN_DB_WIREDTIGER_CACHE_GB"
	// EnvKafkaHeapOpts is read by kafka-server-start.sh.
	EnvKafkaHeapOpts = "KAFKA_HEAP_OPTS"
	// EnvMaxServerMemoryUsage is the ClickHouse max_server_memory_usage in bytes.
	EnvMaxServerMemoryUsage = "FLYNN_DB_MAX_SERVER_MEMORY_USAGE"
)

var engineMemoryEnvKeys = []string{
	EnvMemoryLimit, EnvSharedBuffers, EnvEffectiveCacheSize, EnvInnoDBBufferPoolSize,
	EnvRedisMaxMemory, EnvWiredTigerCacheGB, EnvKafkaHeapOpts, EnvMaxServerMemoryUsage,
}

// EngineMemoryEnv is the release env that sizes engine from a memory limit
// in bytes. Empty when memory is not positive (the engine keeps its own
// defaults).
//
//   - postgres: shared_buffers 25%, effective_cache_size 75%
//   - mysql: innodb_buffer_pool_size 50%
//   - redis: maxmemory 75% (the rest is replication and fork headroom)
//   - mongodb: WiredTiger cache 50% of (memory - 1 GiB), at least 0.25 GB
//     (mongod's own formula, applied to the limit instead of host RAM)
//   - kafka: JVM heap 50% (-Xms = -Xmx); page cache uses the rest
//   - clickhouse: max_server_memory_usage 80%
func EngineMemoryEnv(engine string, memory int64) map[string]string {
	if memory <= 0 {
		return map[string]string{}
	}
	eng, err := NormalizeEngine(engine)
	if err != nil {
		return map[string]string{}
	}
	env := map[string]string{EnvMemoryLimit: strconv.FormatInt(memory, 10)}
	switch eng {
	case EnginePostgres:
		env[EnvSharedBuffers] = fmt.Sprintf("%dMB", max64(memory/4/mib, 16))
		env[EnvEffectiveCacheSize] = fmt.Sprintf("%dMB", max64(memory*3/4/mib, 32))
	case EngineMySQL:
		env[EnvInnoDBBufferPoolSize] = strconv.FormatInt(max64(memory/2/mib, 32)*mib, 10)
	case EngineRedis:
		env[EnvRedisMaxMemory] = strconv.FormatInt(memory*3/4, 10)
	case EngineMongoDB:
		gb := (float64(memory) - float64(gib)) / 2 / float64(gib)
		if gb < 0.25 {
			gb = 0.25
		}
		env[EnvWiredTigerCacheGB] = strconv.FormatFloat(gb, 'f', 2, 64)
	case EngineKafka:
		heap := max64(memory/2/mib, 128)
		env[EnvKafkaHeapOpts] = fmt.Sprintf("-Xms%dm -Xmx%dm", heap, heap)
	case EngineClickHouse:
		env[EnvMaxServerMemoryUsage] = strconv.FormatInt(memory*8/10, 10)
	}
	return env
}

// ApplyEngineMemoryEnv sets the EngineMemoryEnv keys for the memory limit in
// r on env, and removes stale ones first, so a release rebuilt from an older
// one does not keep settings for a different limit.
func ApplyEngineMemoryEnv(env map[string]string, engine string, r resource.Resources) {
	if env == nil {
		return
	}
	for _, k := range engineMemoryEnvKeys {
		delete(env, k)
	}
	for k, v := range EngineMemoryEnv(engine, MemoryLimitOf(r)) {
		env[k] = v
	}
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
