//go:build linux
// +build linux

package libcontainer

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"
)

const oomCgroupName = "memory"

type PressureLevel uint

const (
	LowPressure PressureLevel = iota
	MediumPressure
	CriticalPressure
)

// parseOOMKillCount reads the memory.events file and returns the current
// oom_kill counter value.
func parseOOMKillCount(path string) (uint64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "oom_kill ") {
			parts := strings.SplitN(line, " ", 2)
			if len(parts) == 2 {
				return strconv.ParseUint(strings.TrimSpace(parts[1]), 10, 64)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	// If oom_kill line is not found, return 0 (no OOM kills yet)
	return 0, nil
}

// notifyOnOOM returns channel on which you can expect event about OOM,
// if process died without OOM this channel will be closed.
// This implementation uses cgroups v2 and monitors the memory.events file
// via inotify for changes to the oom_kill counter.
func notifyOnOOM(paths map[string]string) (<-chan struct{}, error) {
	dir := paths[oomCgroupName]
	if dir == "" {
		return nil, fmt.Errorf("path %q missing", oomCgroupName)
	}

	eventsPath := filepath.Join(dir, "memory.events")

	// Read the initial oom_kill count
	initialCount, err := parseOOMKillCount(eventsPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read memory.events: %w", err)
	}

	// Set up inotify to watch for modifications to memory.events
	fd, err := unix.InotifyInit()
	if err != nil {
		return nil, fmt.Errorf("failed to initialize inotify: %w", err)
	}

	wd, err := unix.InotifyAddWatch(fd, eventsPath, unix.IN_MODIFY)
	if err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("failed to add inotify watch: %w", err)
	}

	ch := make(chan struct{})
	go func() {
		defer func() {
			unix.InotifyRmWatch(fd, uint32(wd))
			unix.Close(fd)
			close(ch)
		}()

		lastCount := initialCount
		buf := make([]byte, unix.SizeofInotifyEvent+unix.NAME_MAX+1)
		for {
			n, err := unix.Read(fd, buf)
			if err != nil {
				return
			}
			if n < unix.SizeofInotifyEvent {
				continue
			}

			// Process all inotify events in the buffer
			offset := 0
			for offset+unix.SizeofInotifyEvent <= n {
				event := (*unix.InotifyEvent)(unsafe.Pointer(&buf[offset]))
				offset += unix.SizeofInotifyEvent + int(event.Len)

				// Check if the memory.events file still exists
				// (container may have been destroyed)
				if _, err := os.Lstat(eventsPath); os.IsNotExist(err) {
					return
				}

				// Re-read the oom_kill count
				currentCount, err := parseOOMKillCount(eventsPath)
				if err != nil {
					// File may have been removed (container destroyed)
					return
				}

				if currentCount > lastCount {
					lastCount = currentCount
					ch <- struct{}{}
				}
			}
		}
	}()
	return ch, nil
}

// notifyMemoryPressure monitors memory pressure using cgroups v2 PSI triggers.
// It opens memory.pressure, writes a PSI trigger configuration, then uses
// poll(POLLPRI) to receive notifications when the pressure threshold is exceeded.
func notifyMemoryPressure(paths map[string]string, level PressureLevel) (<-chan struct{}, error) {
	dir := paths[oomCgroupName]
	if dir == "" {
		return nil, fmt.Errorf("path %q missing", oomCgroupName)
	}

	if level > CriticalPressure {
		return nil, fmt.Errorf("invalid pressure level %d", level)
	}

	pressurePath := filepath.Join(dir, "memory.pressure")

	// Map PressureLevel to a PSI trigger string.
	// Format: "<stall_type> <threshold_us> <window_us>"
	// - LowPressure:      "some" stall > 50ms in a 1s window
	// - MediumPressure:   "some" stall > 100ms in a 1s window
	// - CriticalPressure: "full" stall > 100ms in a 1s window
	triggerStr := []string{
		"some 50000 1000000",
		"some 100000 1000000",
		"full 100000 1000000",
	}[level]

	// Open memory.pressure for writing to register the PSI trigger
	fd, err := unix.Open(pressurePath, unix.O_RDWR|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to open memory.pressure: %w", err)
	}

	// Write the PSI trigger configuration
	if _, err := unix.Write(fd, []byte(triggerStr)); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("failed to write PSI trigger: %w", err)
	}

	ch := make(chan struct{})
	go func() {
		defer func() {
			unix.Close(fd)
			close(ch)
		}()

		pollFds := []unix.PollFd{
			{
				Fd:     int32(fd),
				Events: unix.POLLPRI,
			},
		}

		for {
			// Wait indefinitely for a pressure event (-1 timeout)
			n, err := unix.Poll(pollFds, -1)
			if err != nil {
				// EINTR is expected if we get a signal, retry
				if err == unix.EINTR {
					continue
				}
				return
			}
			if n <= 0 {
				continue
			}

			// Check for error conditions that indicate the cgroup was destroyed
			if pollFds[0].Revents&(unix.POLLERR|unix.POLLHUP|unix.POLLNVAL) != 0 {
				return
			}

			if pollFds[0].Revents&unix.POLLPRI != 0 {
				// Check if the pressure file still exists (container may have been destroyed)
				if _, err := os.Lstat(pressurePath); os.IsNotExist(err) {
					return
				}
				ch <- struct{}{}
			}
		}
	}()
	return ch, nil
}
