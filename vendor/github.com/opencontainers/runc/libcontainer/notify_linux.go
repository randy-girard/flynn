//go:build linux
// +build linux

package libcontainer

import (
	"bufio"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"

	"github.com/opencontainers/runc/libcontainer/cgroups"
	"golang.org/x/sys/unix"
)

const oomCgroupName = "memory"

type PressureLevel uint

const (
	LowPressure PressureLevel = iota
	MediumPressure
	CriticalPressure
)

func registerMemoryEvent(cgDir string, evName string, arg string) (<-chan struct{}, error) {
	evFile, err := os.Open(filepath.Join(cgDir, evName))
	if err != nil {
		return nil, err
	}
	fd, err := unix.Eventfd(0, unix.EFD_CLOEXEC)
	if err != nil {
		evFile.Close()
		return nil, err
	}

	eventfd := os.NewFile(uintptr(fd), "eventfd")

	eventControlPath := filepath.Join(cgDir, "cgroup.event_control")
	data := fmt.Sprintf("%d %d %s", eventfd.Fd(), evFile.Fd(), arg)
	if err := ioutil.WriteFile(eventControlPath, []byte(data), 0700); err != nil {
		eventfd.Close()
		evFile.Close()
		return nil, err
	}
	ch := make(chan struct{})
	go func() {
		defer func() {
			eventfd.Close()
			evFile.Close()
			close(ch)
		}()
		buf := make([]byte, 8)
		for {
			if _, err := eventfd.Read(buf); err != nil {
				return
			}
			// When a cgroup is destroyed, an event is sent to eventfd.
			// So if the control path is gone, return instead of notifying.
			if _, err := os.Lstat(eventControlPath); os.IsNotExist(err) {
				return
			}
			ch <- struct{}{}
		}
	}()
	return ch, nil
}

// notifyOnOOMV2 uses inotify to watch memory.events for OOM kills in cgroups v2
func notifyOnOOMV2(dir string) (<-chan struct{}, error) {
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC)
	if err != nil {
		return nil, fmt.Errorf("failed to create inotify fd: %w", err)
	}

	eventsPath := filepath.Join(dir, "memory.events")
	wd, err := unix.InotifyAddWatch(fd, eventsPath, unix.IN_MODIFY)
	if err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("failed to add inotify watch for %s: %w", eventsPath, err)
	}

	ch := make(chan struct{})

	// Get initial oom_kill count
	getOOMKillCount := func() uint64 {
		f, err := os.Open(eventsPath)
		if err != nil {
			return 0
		}
		defer f.Close()
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "oom_kill ") {
				var count uint64
				fmt.Sscanf(line, "oom_kill %d", &count)
				return count
			}
		}
		return 0
	}

	lastOOMCount := getOOMKillCount()

	go func() {
		defer func() {
			unix.InotifyRmWatch(fd, uint32(wd))
			unix.Close(fd)
			close(ch)
		}()
		buf := make([]byte, 4096)
		for {
			n, err := unix.Read(fd, buf)
			if err != nil {
				if err == unix.EINTR {
					continue
				}
				return
			}
			if n < unix.SizeofInotifyEvent {
				continue
			}
			// Check if the cgroup still exists
			if _, err := os.Lstat(dir); os.IsNotExist(err) {
				return
			}
			// Check if oom_kill count increased
			currentOOMCount := getOOMKillCount()
			if currentOOMCount > lastOOMCount {
				lastOOMCount = currentOOMCount
				ch <- struct{}{}
			}
		}
	}()

	return ch, nil
}

// notifyOnOOM returns channel on which you can expect event about OOM,
// if process died without OOM this channel will be closed.
func notifyOnOOM(paths map[string]string) (<-chan struct{}, error) {
	dir := paths[oomCgroupName]
	if dir == "" {
		return nil, fmt.Errorf("path %q missing", oomCgroupName)
	}

	if cgroups.IsCgroup2UnifiedMode() {
		return notifyOnOOMV2(dir)
	}
	return registerMemoryEvent(dir, "memory.oom_control", "")
}

func notifyMemoryPressure(paths map[string]string, level PressureLevel) (<-chan struct{}, error) {
	dir := paths[oomCgroupName]
	if dir == "" {
		return nil, fmt.Errorf("path %q missing", oomCgroupName)
	}

	if level > CriticalPressure {
		return nil, fmt.Errorf("invalid pressure level %d", level)
	}

	levelStr := []string{"low", "medium", "critical"}[level]
	return registerMemoryEvent(dir, "memory.pressure_level", levelStr)
}
