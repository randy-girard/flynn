package version

import "testing"

func TestStringReleaseDevAndParse(t *testing.T) {
	t.Setenv("FLYNN_VERSION", "")
	if String() != "dev" || !Dev() || Release() != "dev" {
		t.Fatalf("dev defaults: %s %v %s", String(), Dev(), Release())
	}

	t.Setenv("FLYNN_VERSION", "v20160814.0-abc123")
	if String() != "v20160814.0-abc123" {
		t.Fatal(String())
	}
	if Dev() {
		t.Fatal("release must not be Dev")
	}
	if Release() != "v20160814.0" {
		t.Fatalf("Release=%s", Release())
	}

	older := Parse("v20160101.0")
	newer := Parse("v20160814.2")
	if !older.Before(newer) || newer.Before(older) {
		t.Fatal("Before")
	}
	sameDate := Parse("v20160814.1")
	if !sameDate.Before(newer) {
		t.Fatal("iteration")
	}
	if !Parse("nope").Dev {
		t.Fatal("invalid is Dev")
	}
}
