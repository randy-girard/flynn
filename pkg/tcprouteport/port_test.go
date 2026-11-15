package tcprouteport

import "testing"

func TestWindow(t *testing.T) {
	if Count() != 20000 {
		t.Fatalf("count = %d, want 20000", Count())
	}
	for _, port := range []int{MinPort, 15000, MaxPort} {
		if !Allowed(port) {
			t.Fatalf("%d should be allowed", port)
		}
		if err := Check(port); err != nil {
			t.Fatalf("check %d: %v", port, err)
		}
	}
	if err := Check(0); err != nil {
		t.Fatalf("port 0 assigns: %v", err)
	}
	blocked := []int{0, 22, 53, 80, 443, 1111, 1113, 3000, 3500, 5000, 5002, 9999, 30000, 32768, 65534}
	for _, port := range blocked {
		if port == 0 {
			continue
		}
		if Allowed(port) {
			t.Fatalf("%d must stay outside the route window", port)
		}
		err := Check(port)
		if err == nil || err.Error() != ErrWindow.Error() {
			t.Fatalf("check %d = %v", port, err)
		}
	}
	if ErrExhausted.Error() != "tcp route ports 10000-29999 are exhausted" {
		t.Fatal(ErrExhausted)
	}
}
