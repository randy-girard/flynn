package volume

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/randy-girard/flynn/pkg/random"
)

func TestValidVolumeID(t *testing.T) {
	valid := random.UUID()
	if !ValidVolumeID(valid) {
		t.Fatalf("random.UUID() %q should be valid", valid)
	}

	cases := []struct {
		id    string
		valid bool
	}{
		{"", false},
		{valid, true},
		{"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", true},
		{"AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE", false}, // production IDs are lowercase
		{"vol-1", false},
		{"../", false},
		{"../../x", false},
		{"foo/../bar", false},
		{"..", false},
		{".", false},
		{"data/../../../etc", false},
		{"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeee", false},   // 35 chars
		{"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeeee", false}, // 37 chars
		{"------------------------------------", false},
		{"abcdefabcdefabcdefabcdefabcdefabcdef", false}, // 36 hex, no hyphens
		{"aaaaaaaa_bbbb_cccc_dddd_eeeeeeeeeeee", false},
	}
	for _, tc := range cases {
		if got := ValidVolumeID(tc.id); got != tc.valid {
			t.Errorf("ValidVolumeID(%q)=%v, want %v", tc.id, got, tc.valid)
		}
	}
}

func TestValidateCreateVolumeID(t *testing.T) {
	if err := ValidateCreateVolumeID(""); err != nil {
		t.Fatalf("empty id should be allowed for create: %v", err)
	}
	if err := ValidateCreateVolumeID(random.UUID()); err != nil {
		t.Fatalf("UUID should be allowed: %v", err)
	}
	if err := ValidateCreateVolumeID("../../escape"); err != ErrInvalidVolumeID {
		t.Fatalf("path-escape id: err=%v, want ErrInvalidVolumeID", err)
	}
	if err := ValidateCreateVolumeID("short"); err != ErrInvalidVolumeID {
		t.Fatalf("short id: err=%v, want ErrInvalidVolumeID", err)
	}
}

func TestVolumeIDCannotEscapeJoin(t *testing.T) {
	// filepath.Join cleans ".." so a caller-supplied id would leave the volume root.
	root := "/var/lib/flynn/volumes/mnt/data"
	escaped := filepath.Join(root, "../../x")
	if strings.HasPrefix(escaped, root+string(filepath.Separator)) || escaped == root {
		t.Fatalf("Join did not escape root: %s", escaped)
	}
	if err := ValidateCreateVolumeID("../../x"); err != ErrInvalidVolumeID {
		t.Fatalf("escape id must be rejected before Join: %v", err)
	}
	safe := filepath.Join(root, random.UUID())
	if !strings.HasPrefix(safe, root+string(filepath.Separator)) {
		t.Fatalf("UUID id left the volume root: %s", safe)
	}
}
