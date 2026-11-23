package main

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/pprof"
	"syscall"
	"time"

	"github.com/inconshreveable/log15"
)

// handleGoroutineDumps writes every goroutine's stack to
// <dir>/flynn-host-goroutines-<unix nanos>.txt (mode 0600) each time the
// daemon gets SIGUSR2, without stopping it. containerinit does the same for
// a job. A hung job start or cleanup is otherwise undiagnosable once
// flynn-host is restarted. It returns a func that stops the handler.
func handleGoroutineDumps(dir string, log log15.Logger) func() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGUSR2)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-ch:
				path, err := writeGoroutineDump(dir, time.Now())
				if err != nil {
					log.Error("error writing goroutine dump", "err", err)
					continue
				}
				log.Info("wrote goroutine dump", "path", path)
			case <-done:
				return
			}
		}
	}()
	return func() {
		signal.Stop(ch)
		close(done)
	}
}

func writeGoroutineDump(dir string, now time.Time) (string, error) {
	path := filepath.Join(dir, fmt.Sprintf("flynn-host-goroutines-%d.txt", now.UnixNano()))
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", err
	}
	// debug=2 is the panic format: every goroutine with its wait reason and
	// how long it has been blocked, which is what shows a held mutex.
	if err := pprof.Lookup("goroutine").WriteTo(f, 2); err != nil {
		f.Close()
		return "", err
	}
	return path, f.Close()
}
