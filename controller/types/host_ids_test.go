package types

import "testing"

func TestHostIDsTagRoundTrip(t *testing.T) {
	got := ParseHostIDsTag(EncodeHostIDsTag([]string{"c", "a", "b", "a", " "}))
	if len(got) != 3 || got[0] != "c" || got[1] != "a" || got[2] != "b" {
		t.Fatalf("Parse/Encode = %v", got)
	}
	if ParseHostIDsTag("") != nil {
		t.Fatal("empty tag must parse to nil")
	}
}

func TestHostIDsTagMatches(t *testing.T) {
	if !HostIDsTagMatches(nil, "host1") {
		t.Fatal("nil tags match every host")
	}
	if !HostIDsTagMatches(map[string]string{"disk": "ssd"}, "host1") {
		t.Fatal("tags without flynn-host-ids match every host")
	}
	allowed := map[string]string{FormationHostIDsTag: "host1,host3"}
	if !HostIDsTagMatches(allowed, "host1") || !HostIDsTagMatches(allowed, "host3") {
		t.Fatal("listed hosts must match")
	}
	if HostIDsTagMatches(allowed, "host2") {
		t.Fatal("unlisted host must not match")
	}
	if HostIDsTagMatches(map[string]string{FormationHostIDsTag: ""}, "host1") {
		t.Fatal("empty flynn-host-ids must match no hosts")
	}
}
