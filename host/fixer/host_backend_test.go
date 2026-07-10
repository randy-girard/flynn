package fixer

import "testing"

func TestDNSFromSubnet(t *testing.T) {
	tests := []struct {
		subnet string
		want   string
	}{
		{"100.100.44.1/24", "100.100.44.1:53"},
		{"100.100.83.1/24", "100.100.83.1:53"},
		{"100.100.60.1/24", "100.100.60.1:53"},
	}
	for _, tc := range tests {
		got, err := dnsFromSubnet(tc.subnet)
		if err != nil {
			t.Fatalf("subnet %q: %v", tc.subnet, err)
		}
		if got != tc.want {
			t.Fatalf("subnet %q: got %q want %q", tc.subnet, got, tc.want)
		}
	}
}

func TestNormalizeDiscoverdURL(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{
			"192.168.56.20:1111,192.168.56.21:1111",
			"http://192.168.56.20:1111,http://192.168.56.21:1111",
		},
		{
			"http://192.168.56.20:1111,http://192.168.56.21:1111",
			"http://192.168.56.20:1111,http://192.168.56.21:1111",
		},
		{"", ""},
	}
	for _, tc := range tests {
		got := normalizeDiscoverdURL(tc.in)
		if got != tc.want {
			t.Fatalf("in %q: got %q want %q", tc.in, got, tc.want)
		}
	}
}
