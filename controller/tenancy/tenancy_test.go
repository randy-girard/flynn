package tenancy

import "testing"

func TestHandleRules(t *testing.T) {
	for _, ok := range []string{"ab", "a1", "user-name", "a01234567890123456789012345678901234567"} {
		if err := ValidateHandle(ok); err != nil {
			t.Fatalf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "A", "a", "-ab", "HasCaps", "a_b", "a.b"} {
		if err := ValidateHandle(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	// 40 chars is over the 39 limit (1 + 38).
	if err := ValidateHandle("a012345678901234567890123456789012345678"); err == nil {
		t.Fatal("40 char handle")
	}
}

func TestQuotas(t *testing.T) {
	hosted, err := EffectiveLimits(ModeHosted, nil)
	if err != nil || *hosted.MaxApps != 5 || *hosted.MaxMemoryMB != 512 || *hosted.MaxCollaborators != 2 {
		t.Fatalf("%+v %v", hosted, err)
	}
	self, err := EffectiveLimits(ModeSelfHosted, nil)
	if err != nil || self.MaxApps != nil || Exceeds(self.MaxApps, 1000) {
		t.Fatalf("self-hosted unlimited: %+v %v", self, err)
	}
	zero := 0
	if _, err := EffectiveLimits(ModeSelfHosted, &Limits{MaxApps: &zero}); err == nil {
		t.Fatal("zero must be rejected")
	}
	neg := -1
	if _, err := EffectiveLimits(ModeHosted, &Limits{MaxResources: &neg}); err == nil {
		t.Fatal("negative must be rejected")
	}
	n := 3
	got, err := EffectiveLimits(ModeHosted, &Limits{MaxApps: &n})
	if err != nil || Exceeds(got.MaxApps, 3) || !Exceeds(got.MaxApps, 4) {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestPasswordRoundTrip(t *testing.T) {
	hash, err := HashPassword("s3cret-pass")
	if err != nil {
		t.Fatal(err)
	}
	if !CheckPassword(hash, "s3cret-pass") || CheckPassword(hash, "nope") {
		t.Fatal("password check")
	}
	if _, err := HashPassword(""); err == nil {
		t.Fatal("empty password")
	}
}

func TestHostnamesAndTXT(t *testing.T) {
	verified := []string{"example.com"}
	if err := HostnameAllowed(ModeSelfHosted, "dashboard.example.com", "app", "cluster.test", nil); err != nil {
		t.Fatal(err)
	}
	if err := HostnameAllowed(ModeHosted, "myapp.cluster.test", "myapp", "cluster.test", nil); err != nil {
		t.Fatal(err)
	}
	for _, reserved := range []string{"dashboard.cluster.test", "controller.cluster.test", "status.cluster.test", "blobstore.cluster.test", "git.cluster.test", "www.cluster.test", "cluster.test"} {
		if err := HostnameAllowed(ModeHosted, reserved, "myapp", "cluster.test", verified); err == nil {
			t.Fatalf("reserved %s", reserved)
		}
	}
	if err := HostnameAllowed(ModeHosted, "api.example.com", "myapp", "cluster.test", verified); err != nil {
		t.Fatal(err)
	}
	if err := HostnameAllowed(ModeHosted, "other.test", "myapp", "cluster.test", verified); err == nil {
		t.Fatal("unverified custom domain")
	}
	if !VerifyTXT([]string{"a", "tok"}, "tok") || VerifyTXT([]string{"a"}, "tok") {
		t.Fatal("txt")
	}
	if TXTName("Example.COM.") != "_flynn-verify.example.com" {
		t.Fatal(TXTName("Example.COM."))
	}
}

func TestScaleIncreases(t *testing.T) {
	old := map[string]int{"web": 1}
	if ScaleIncreases(old, map[string]int{"web": 1}) || !ScaleIncreases(old, map[string]int{"web": 2}) {
		t.Fatal("scale direction")
	}
	if ScaleIncreases(old, map[string]int{"web": 0}) {
		t.Fatal("scale down is not an increase")
	}
}
