//go:build linux
// +build linux

package term

import (
	"io/ioutil"
	"os"
	"testing"

	"github.com/google/go-cmp/cmp"
	"gotest.tools/assert"
)

// RequiresRoot skips tests that require root, unless the test.root flag has
// been set
func RequiresRoot(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("skipping test that requires root")
		return
	}
}

// newTtyForTest opens the process controlling TTY. CI runs these tests as a
// non-root user so RequiresRoot skips. Headless root (ssh without a pty)
// cannot open /dev/tty — skip instead of failing.
func newTtyForTest(t *testing.T) *os.File {
	t.Helper()
	RequiresRoot(t)
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, os.ModeDevice)
	if err != nil {
		t.Skipf("controlling TTY required: %v", err)
	}
	return tty
}

func newTempFile() (*os.File, error) {
	return ioutil.TempFile(os.TempDir(), "temp")
}

func TestGetWinsize(t *testing.T) {
	tty := newTtyForTest(t)
	defer tty.Close()
	winSize, err := GetWinsize(tty.Fd())
	assert.NilError(t, err)
	assert.Assert(t, winSize != nil)

	newSize := Winsize{Width: 200, Height: 200, x: winSize.x, y: winSize.y}
	err = SetWinsize(tty.Fd(), &newSize)
	assert.NilError(t, err)
	winSize, err = GetWinsize(tty.Fd())
	assert.NilError(t, err)
	assert.DeepEqual(t, *winSize, newSize, cmpWinsize)
}

var cmpWinsize = cmp.AllowUnexported(Winsize{})

func TestSetWinsize(t *testing.T) {
	tty := newTtyForTest(t)
	defer tty.Close()
	winSize, err := GetWinsize(tty.Fd())
	assert.NilError(t, err)
	assert.Assert(t, winSize != nil)
	newSize := Winsize{Width: 200, Height: 200, x: winSize.x, y: winSize.y}
	err = SetWinsize(tty.Fd(), &newSize)
	assert.NilError(t, err)
	winSize, err = GetWinsize(tty.Fd())
	assert.NilError(t, err)
	assert.DeepEqual(t, *winSize, newSize, cmpWinsize)
}

func TestGetFdInfo(t *testing.T) {
	tty := newTtyForTest(t)
	defer tty.Close()
	inFd, isTerminal := GetFdInfo(tty)
	assert.Equal(t, inFd, tty.Fd())
	assert.Equal(t, isTerminal, true)
	tmpFile, err := newTempFile()
	assert.NilError(t, err)
	defer tmpFile.Close()
	inFd, isTerminal = GetFdInfo(tmpFile)
	assert.Equal(t, inFd, tmpFile.Fd())
	assert.Equal(t, isTerminal, false)
}

func TestIsTerminal(t *testing.T) {
	tty := newTtyForTest(t)
	defer tty.Close()
	isTerminal := IsTerminal(tty.Fd())
	assert.Equal(t, isTerminal, true)
	tmpFile, err := newTempFile()
	assert.NilError(t, err)
	defer tmpFile.Close()
	isTerminal = IsTerminal(tmpFile.Fd())
	assert.Equal(t, isTerminal, false)
}

func TestSaveState(t *testing.T) {
	tty := newTtyForTest(t)
	defer tty.Close()
	state, err := SaveState(tty.Fd())
	assert.NilError(t, err)
	assert.Assert(t, state != nil)
	tty2 := newTtyForTest(t)
	defer tty2.Close()
	err = RestoreTerminal(tty2.Fd(), state)
	assert.NilError(t, err)
}

func TestDisableEcho(t *testing.T) {
	tty := newTtyForTest(t)
	defer tty.Close()
	state, err := SetRawTerminal(tty.Fd())
	defer RestoreTerminal(tty.Fd(), state)
	assert.NilError(t, err)
	assert.Assert(t, state != nil)
	err = DisableEcho(tty.Fd(), state)
	assert.NilError(t, err)
}
