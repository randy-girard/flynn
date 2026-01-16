//go:build linux
// +build linux

package fs

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"

	"github.com/opencontainers/runc/libcontainer/cgroups"
	"github.com/opencontainers/runc/libcontainer/configs"
	libcontainerUtils "github.com/opencontainers/runc/libcontainer/utils"
)

type CpusetGroupV2 struct {
}

func (s *CpusetGroupV2) Name() string {
	return "cpuset"
}

func (s *CpusetGroupV2) Apply(d *cgroupData) error {
	dir, err := d.path("cpuset")
	if err != nil && !cgroups.IsNotFound(err) {
		return err
	}
	return s.ApplyDir(dir, d.config, d.pid)
}

func (s *CpusetGroupV2) Set(path string, cgroup *configs.Cgroup) error {
	if cgroup.Resources.CpusetCpus != "" {
		if err := writeFile(path, "cpuset.cpus", cgroup.Resources.CpusetCpus); err != nil {
			return err
		}
	}
	if cgroup.Resources.CpusetMems != "" {
		if err := writeFile(path, "cpuset.mems", cgroup.Resources.CpusetMems); err != nil {
			return err
		}
	}
	return nil
}

func (s *CpusetGroupV2) Remove(d *cgroupData) error {
	return removePath(d.path("cpuset"))
}

func (s *CpusetGroupV2) GetStats(path string, stats *cgroups.Stats) error {
	return nil
}

func (s *CpusetGroupV2) ApplyDir(dir string, cgroup *configs.Cgroup, pid int) error {
	// This might happen if we have no cpuset cgroup mounted.
	// Just do nothing and don't fail.
	if dir == "" {
		return nil
	}
	mountInfo, err := ioutil.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return err
	}
	root := filepath.Dir(cgroups.GetClosestMountpointAncestor(dir, string(mountInfo)))
	// 'ensureParent' start with parent because we don't want to
	// explicitly inherit from parent, it could conflict with
	// 'cpuset.cpu_exclusive'.
	if err := s.ensureParent(filepath.Dir(dir), root); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	// We didn't inherit cpuset configs from parent, but we have
	// to ensure cpuset configs are set before moving task into the
	// cgroup.
	// The logic is, if user specified cpuset configs, use these
	// specified configs, otherwise, inherit from parent. This makes
	// cpuset configs work correctly with 'cpuset.cpu_exclusive', and
	// keep backward compatibility.
	if err := s.ensureCpusAndMems(dir, cgroup); err != nil {
		return err
	}

	// because we are not using d.join we need to place the pid into the procs file
	// unlike the other subsystems
	return cgroups.WriteCgroupProc(dir, pid)
}

func (s *CpusetGroupV2) getSubsystemSettings(parent string) (cpus []byte, mems []byte, err error) {
	// Try to read cpuset.cpus.effective first (preferred in cgroups v2)
	// If that doesn't exist (e.g., newly created cgroup), fall back to cpuset.cpus
	cpus, err = ioutil.ReadFile(filepath.Join(parent, "cpuset.cpus.effective"))
	if err != nil {
		if os.IsNotExist(err) {
			// Fall back to cpuset.cpus for newly created cgroups
			cpus, err = ioutil.ReadFile(filepath.Join(parent, "cpuset.cpus"))
			if err != nil {
				return nil, nil, err
			}
		} else {
			return nil, nil, err
		}
	}
	// Same for mems
	mems, err = ioutil.ReadFile(filepath.Join(parent, "cpuset.mems.effective"))
	if err != nil {
		if os.IsNotExist(err) {
			// Fall back to cpuset.mems for newly created cgroups
			mems, err = ioutil.ReadFile(filepath.Join(parent, "cpuset.mems"))
			if err != nil {
				return nil, nil, err
			}
		} else {
			return nil, nil, err
		}
	}
	return cpus, mems, nil
}

// ensureParent makes sure that the parent directory of current is created
// and populated with the proper cpus and mems files copied from
// it's parent.
func (s *CpusetGroupV2) ensureParent(current, root string) error {
	parent := filepath.Dir(current)
	if libcontainerUtils.CleanPath(parent) == root {
		return nil
	}
	// Avoid infinite recursion.
	if parent == current {
		return fmt.Errorf("cpuset: cgroup parent path outside cgroup root")
	}
	if err := s.ensureParent(parent, root); err != nil {
		return err
	}
	if err := os.MkdirAll(current, 0755); err != nil {
		return err
	}
	return s.copyIfNeeded(current, parent)
}

// copyIfNeeded copies the cpuset.cpus and cpuset.mems from the parent
// directory to the current directory if the file's contents are 0
func (s *CpusetGroupV2) copyIfNeeded(current, parent string) error {
	var (
		err                      error
		currentCpus, currentMems []byte
		parentCpus, parentMems   []byte
	)

	if currentCpus, currentMems, err = s.getSubsystemSettings(current); err != nil {
		return err
	}
	if parentCpus, parentMems, err = s.getSubsystemSettings(parent); err != nil {
		return err
	}

	if s.isEmpty(currentCpus) {
		if err := writeFile(current, "cpuset.cpus", string(parentCpus)); err != nil {
			return err
		}
	}
	if s.isEmpty(currentMems) {
		if err := writeFile(current, "cpuset.mems", string(parentMems)); err != nil {
			return err
		}
	}
	return nil
}

func (s *CpusetGroupV2) isEmpty(b []byte) bool {
	return len(bytes.Trim(b, "\n")) == 0
}

func (s *CpusetGroupV2) ensureCpusAndMems(path string, cgroup *configs.Cgroup) error {
	if err := s.Set(path, cgroup); err != nil {
		return err
	}
	return s.copyIfNeeded(path, filepath.Dir(path))
}
