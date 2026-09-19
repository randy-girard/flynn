package plugin

import "testing"

func TestParsePluginCalVer(t *testing.T) {
	got, ok := ParsePluginCalVer("v20260919.2")
	if !ok || got != (PluginCalVer{Date: 20260919, N: 2, P: 0}) {
		t.Fatalf("two-part: %+v ok=%v", got, ok)
	}
	got, ok = ParsePluginCalVer(" v20260919.2.10 ")
	if !ok || got != (PluginCalVer{Date: 20260919, N: 2, P: 10}) {
		t.Fatalf("three-part: %+v ok=%v", got, ok)
	}
	if _, ok := ParsePluginCalVer("v20260919"); ok {
		t.Fatal("date-only must not parse")
	}
	if _, ok := ParsePluginCalVer("v20260919.2.1-smoke"); ok {
		t.Fatal("suffix must not parse")
	}
}

func TestComparePluginCalVer(t *testing.T) {
	eq := [][2]string{
		{"v20260919.2", "v20260919.2.0"},
		{"v20260919.2.0", "v20260919.2"},
	}
	for _, c := range eq {
		if ComparePluginCalVer(c[0], c[1]) != 0 {
			t.Errorf("%s vs %s: want equal", c[0], c[1])
		}
	}
	if ComparePluginCalVer("v20260919.2.1", "v20260919.2") <= 0 {
		t.Fatal("plugin patch must sort above the Flynn-aligned .0 / two-part tag")
	}
	if ComparePluginCalVer("v20260919.2.10", "v20260919.2.9") <= 0 {
		t.Fatal("numeric patch order")
	}
	if ComparePluginCalVer("v20260920.0.0", "v20260919.9.9") <= 0 {
		t.Fatal("newer date wins")
	}
	if ComparePluginCalVer("v20260919.2.0", "v1") <= 0 {
		t.Fatal("calver sorts above non-calver")
	}
}
