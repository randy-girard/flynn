package keepalive

import (
	"bufio"
	"errors"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

var backlog int
var backlogOnce = &sync.Once{}

// ReusableListen returns a TCP listener with SO_REUSEPORT and keepalives
// enabled.
func ReusableListen(proto, addr string) (net.Listener, error) {
	b, err := ReusableBind(proto, addr)
	if err != nil {
		return nil, err
	}
	return b.Listen()
}

// BoundSocket is a TCP socket with SO_REUSEPORT and keepalives set that is
// bound to its address but not listening. It holds the port, and the kernel
// refuses connections to it (Linux answers a SYN with RST) until Listen.
type BoundSocket struct {
	fd    int
	proto string
	addr  string
}

// ReusableBind binds a socket like ReusableListen without listening on it.
func ReusableBind(proto, addr string) (*BoundSocket, error) {
	saddr, typ, err := sockaddr(proto, addr)
	if err != nil {
		return nil, err
	}

	syscall.ForkLock.RLock()
	fd, err := syscall.Socket(typ, syscall.SOCK_STREAM, syscall.IPPROTO_TCP)
	if err == nil {
		syscall.CloseOnExec(fd)
	}
	syscall.ForkLock.RUnlock()
	if err != nil {
		return nil, os.NewSyscallError("socket", err)
	}

	if err := setSockopt(fd); err != nil {
		syscall.Close(fd)
		return nil, err
	}

	if err := syscall.Bind(fd, saddr); err != nil {
		syscall.Close(fd)
		return nil, os.NewSyscallError("bind", err)
	}

	return &BoundSocket{fd: fd, proto: proto, addr: addr}, nil
}

// Listen starts listening on the socket and returns it as a net.Listener.
// The BoundSocket is used up either way: on success the listener owns the
// socket, on failure it is closed.
func (b *BoundSocket) Listen() (net.Listener, error) {
	backlogOnce.Do(func() {
		backlog = maxListenerBacklog()
	})

	fd := b.fd
	b.fd = -1
	if fd < 0 {
		return nil, errors.New("reuseport: socket already used")
	}

	if err := syscall.Listen(fd, backlog); err != nil {
		syscall.Close(fd)
		return nil, os.NewSyscallError("listen", err)
	}

	f := os.NewFile(uintptr(fd), b.proto+":"+b.addr)
	l, err := net.FileListener(f)
	f.Close()
	if err != nil {
		return nil, err
	}

	return l, nil
}

// Close releases the port. It does nothing after Listen.
func (b *BoundSocket) Close() error {
	if b.fd < 0 {
		return nil
	}
	err := syscall.Close(b.fd)
	b.fd = -1
	return err
}

func sockaddr(proto, addr string) (syscall.Sockaddr, int, error) {
	a, err := net.ResolveTCPAddr(proto, addr)
	if err != nil {
		return nil, -1, err
	}

	switch proto {
	case "tcp4":
		var ip [4]byte
		copy(ip[:], a.IP.To4())
		return &syscall.SockaddrInet4{Port: a.Port, Addr: ip}, syscall.AF_INET, nil
	case "tcp6":
		var ip [16]byte
		copy(ip[:], a.IP.To16())
		// TODO: this does not set the zone
		return &syscall.SockaddrInet6{Port: a.Port, Addr: ip}, syscall.AF_INET6, nil
	default:
		return nil, -1, errors.New("reuseport: only tcp4 and tcp6 are supported")
	}
}

func maxListenerBacklog() int {
	f, err := os.Open("/proc/sys/net/core/somaxconn")
	if err != nil {
		return syscall.SOMAXCONN
	}
	defer f.Close()
	r := bufio.NewReader(f)
	l, err := r.ReadString('\n')
	if err != nil {
		return syscall.SOMAXCONN
	}
	fs := strings.Fields(l)
	if len(fs) < 1 {
		return syscall.SOMAXCONN
	}
	n, err := strconv.Atoi(fs[0])
	if err != nil || n == 0 {
		return syscall.SOMAXCONN
	}
	// Linux stores the backlog in a uint16.
	// Truncate number to avoid wrapping.
	// See Go issue 5030.
	if n > 1<<16-1 {
		n = 1<<16 - 1
	}
	return n
}
