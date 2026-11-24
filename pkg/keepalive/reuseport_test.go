package keepalive

import (
	"net"
	"testing"
	"time"
)

func TestReusableBindRefusesUntilListen(t *testing.T) {
	// find a free port with a throwaway listener
	tmp, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := tmp.Addr().String()
	tmp.Close()

	b, err := ReusableBind("tcp4", addr)
	if err != nil {
		t.Fatal(err)
	}
	if conn, err := net.DialTimeout("tcp4", addr, time.Second); err == nil {
		conn.Close()
		t.Fatal("dial to a bound socket that is not listening succeeded")
	}

	// a second SO_REUSEPORT socket can bind next to it
	other, err := ReusableBind("tcp4", addr)
	if err != nil {
		t.Fatalf("second bind: %s", err)
	}
	other.Close()

	l, err := b.Listen()
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if err := b.Close(); err != nil {
		t.Fatalf("close after listen: %s", err)
	}
	if _, err := b.Listen(); err == nil {
		t.Fatal("listen twice succeeded")
	}
	go func() {
		if c, err := l.Accept(); err == nil {
			c.Close()
		}
	}()
	conn, err := net.DialTimeout("tcp4", addr, time.Second)
	if err != nil {
		t.Fatalf("dial after listen: %s", err)
	}
	conn.Close()
}
