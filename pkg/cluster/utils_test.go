package cluster

import "testing"

func TestGenerateAndExtractJobID(t *testing.T) {
	id := GenerateJobID("host1", "deadbeef")
	if id != "host1-deadbeef" {
		t.Fatalf("id=%s", id)
	}
	hostID, err := ExtractHostID(id)
	if err != nil || hostID != "host1" {
		t.Fatalf("host=%s err=%v", hostID, err)
	}
	uuid, err := ExtractUUID(id)
	if err != nil || uuid != "deadbeef" {
		t.Fatalf("uuid=%s err=%v", uuid, err)
	}
	if GenerateJobID("host1", "") == "host1-" {
		t.Fatal("empty uuid must be replaced with a random id")
	}
	for _, bad := range []string{"", "host1", "-uuid", "host1-"} {
		if _, err := ExtractHostID(bad); err == nil {
			t.Fatalf("ExtractHostID(%q) must fail", bad)
		}
		if _, err := ExtractUUID(bad); err == nil {
			t.Fatalf("ExtractUUID(%q) must fail", bad)
		}
	}
}

func TestHostTagsFromMeta(t *testing.T) {
	got := HostTagsFromMeta(map[string]string{
		"tag:disk": "ssd",
		"id":       "host1",
		"tag:zone": "a",
	})
	if got["disk"] != "ssd" || got["zone"] != "a" {
		t.Fatalf("%v", got)
	}
	if _, ok := got["id"]; ok {
		t.Fatal("non-tag metadata must not become a host tag")
	}
}
