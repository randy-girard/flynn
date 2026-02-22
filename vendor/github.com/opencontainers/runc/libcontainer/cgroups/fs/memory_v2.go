// +build linux

package fs

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/opencontainers/runc/libcontainer/cgroups"
	"github.com/opencontainers/runc/libcontainer/configs"
)

type MemoryGroupV2 struct {
}

func (s *MemoryGroupV2) Name() string {
	return "memory"
}

func (s *MemoryGroupV2) Apply(d *cgroupData) (err error) {
	path, err := d.path("memory")
	if err != nil && !cgroups.IsNotFound(err) {
		return err
	} else if path == "" {
		return nil
	}
	if memoryAssigned(d.config) {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			if err := os.MkdirAll(path, 0755); err != nil {
				return err
			}
			// Only enable kernel memory accouting when this cgroup
			// is created by libcontainer, otherwise we might get
			// error when people use `cgroupsPath` to join an existed
			// cgroup whose kernel memory is not initialized.
			if err := EnableKernelMemoryAccounting(path); err != nil {
				return err
			}
		}
	}
	defer func() {
		if err != nil {
			os.RemoveAll(path)
		}
	}()

	// We need to join memory cgroup after set memory limits, because
	// kmem.limit_in_bytes can only be set when the cgroup is empty.
	_, err = d.join("memory")
	if err != nil && !cgroups.IsNotFound(err) {
		return err
	}
	return nil
}

func setMemoryAndSwapCgroups(path string, cgroup *configs.Cgroup) error {
	// In cgroups v2, memory.swap.max is the swap limit (separate from memory.max).
	// To prevent swap usage when a memory limit is set, we need to explicitly set
	// memory.swap.max to 0. If MemorySwap is 0 and Memory is set, set swap to 0.
	// If MemorySwap is explicitly set (non-zero), use that value.
	if cgroup.Resources.Memory != 0 {
		// If memory limit is set, we should also set swap limit
		swapLimit := cgroup.Resources.MemorySwap
		// If MemorySwap is 0 (default/unset) but Memory is set, disable swap by setting it to 0
		// If MemorySwap is explicitly set to a non-zero value, use that
		// If MemorySwap is -1, it means unlimited swap (don't set memory.swap.max)
		if swapLimit == 0 && cgroup.Resources.Memory > 0 {
			// Memory is set but MemorySwap is 0 (default), disable swap
			if err := writeFile(path, "memory.swap.max", "0"); err != nil {
				return err
			}
		} else if swapLimit != 0 && swapLimit != -1 {
			// MemorySwap is explicitly set to a non-zero, non-unlimited value
			if err := writeFile(path, "memory.swap.max", strconv.FormatInt(swapLimit, 10)); err != nil {
				return err
			}
		}
		// Set memory limit
		if err := writeFile(path, "memory.max", strconv.FormatInt(cgroup.Resources.Memory, 10)); err != nil {
			return err
		}
	} else if cgroup.Resources.MemorySwap != 0 && cgroup.Resources.MemorySwap != -1 {
		// Memory is not set but MemorySwap is explicitly set
		if err := writeFile(path, "memory.swap.max", strconv.FormatInt(cgroup.Resources.MemorySwap, 10)); err != nil {
			return err
		}
	}
	return nil
}

func (s *MemoryGroupV2) Set(path string, cgroup *configs.Cgroup) error {

	if err := setMemoryAndSwapCgroups(path, cgroup); err != nil {
		return err
	}

	if cgroup.Resources.KernelMemory != 0 {
		if err := setKernelMemory(path, cgroup.Resources.KernelMemory); err != nil {
			return err
		}
	}

	if cgroup.Resources.MemoryReservation != 0 {
		if err := writeFile(path, "memory.high", strconv.FormatInt(cgroup.Resources.MemoryReservation, 10)); err != nil {
			return err
		}
	}

	return nil
}

func (s *MemoryGroupV2) Remove(d *cgroupData) error {
	return removePath(d.path("memory"))
}

func (s *MemoryGroupV2) GetStats(path string, stats *cgroups.Stats) error {
	// Set stats from memory.stat.
	statsFile, err := os.Open(filepath.Join(path, "memory.stat"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer statsFile.Close()

	sc := bufio.NewScanner(statsFile)
	for sc.Scan() {
		t, v, err := getCgroupParamKeyValue(sc.Text())
		if err != nil {
			return fmt.Errorf("failed to parse memory.stat (%q) - %v", sc.Text(), err)
		}
		stats.MemoryStats.Stats[t] = v
	}
	stats.MemoryStats.Cache = stats.MemoryStats.Stats["cache"]

	memoryUsage, err := getMemoryDataV2(path, "")
	if err != nil {
		return err
	}
	stats.MemoryStats.Usage = memoryUsage
	swapUsage, err := getMemoryDataV2(path, "swap")
	if err != nil {
		return err
	}
	stats.MemoryStats.SwapUsage = swapUsage

	stats.MemoryStats.UseHierarchy = true
	return nil
}

func getMemoryDataV2(path, name string) (cgroups.MemoryData, error) {
	memoryData := cgroups.MemoryData{}

	moduleName := "memory"
	if name != "" {
		moduleName = strings.Join([]string{"memory", name}, ".")
	}
	usage := strings.Join([]string{moduleName, "current"}, ".")
	limit := strings.Join([]string{moduleName, "max"}, ".")

	value, err := getCgroupParamUint(path, usage)
	if err != nil {
		if moduleName != "memory" && os.IsNotExist(err) {
			return cgroups.MemoryData{}, nil
		}
		return cgroups.MemoryData{}, fmt.Errorf("failed to parse %s - %v", usage, err)
	}
	memoryData.Usage = value

	value, err = getCgroupParamUint(path, limit)
	if err != nil {
		if moduleName != "memory" && os.IsNotExist(err) {
			return cgroups.MemoryData{}, nil
		}
		return cgroups.MemoryData{}, fmt.Errorf("failed to parse %s - %v", limit, err)
	}
	memoryData.Limit = value

	return memoryData, nil
}
