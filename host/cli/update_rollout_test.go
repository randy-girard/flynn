package cli

import "testing"

func TestDecideUpdateRollout(t *testing.T) {
	tests := []struct {
		name           string
		allNodes       bool
		skipImages     bool
		imagesOnly     bool
		hostCount      int
		hostCountKnown bool
		wantRemotes    bool
		wantImages     bool
	}{
		{
			name:      "1-node default rolls out images without --all-nodes",
			hostCount: 1, hostCountKnown: true,
			wantRemotes: false, wantImages: true,
		},
		{
			name:      "3-node default skips remotes and images",
			hostCount: 3, hostCountKnown: true,
			wantRemotes: false, wantImages: false,
		},
		{
			name:     "3-node --all-nodes updates remotes and images",
			allNodes: true, hostCount: 3, hostCountKnown: true,
			wantRemotes: true, wantImages: true,
		},
		{
			name:     "3-node --all-nodes --skip-images updates remotes only",
			allNodes: true, skipImages: true, hostCount: 3, hostCountKnown: true,
			wantRemotes: true, wantImages: false,
		},
		{
			name:     "3-node --all-nodes --images-only rolls out images only",
			allNodes: true, imagesOnly: true, hostCount: 3, hostCountKnown: true,
			wantRemotes: false, wantImages: true,
		},
		{
			name:           "unknown host count without --all-nodes skips images",
			hostCountKnown: false,
			wantRemotes:    false, wantImages: false,
		},
		{
			name:     "unknown host count with --all-nodes rolls out images",
			allNodes: true, hostCountKnown: false,
			wantRemotes: true, wantImages: true,
		},
		{
			name:      "0 hosts treated as single-node for image rollout",
			hostCount: 0, hostCountKnown: true,
			wantRemotes: false, wantImages: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := decideUpdateRollout(tt.allNodes, tt.skipImages, tt.imagesOnly, tt.hostCount, tt.hostCountKnown)
			if got.UpdateRemotes != tt.wantRemotes {
				t.Fatalf("UpdateRemotes=%v, want %v", got.UpdateRemotes, tt.wantRemotes)
			}
			if got.RolloutImages != tt.wantImages {
				t.Fatalf("RolloutImages=%v, want %v", got.RolloutImages, tt.wantImages)
			}
		})
	}
}

func TestValidateImagesOnlyFlags(t *testing.T) {
	if err := validateImagesOnlyFlags(false, false, 3, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := validateImagesOnlyFlags(true, true, 3, nil); err != nil {
		t.Fatalf("unexpected error with allNodes: %v", err)
	}
	if err := validateImagesOnlyFlags(true, false, 1, nil); err != nil {
		t.Fatalf("single-host images-only should be allowed: %v", err)
	}
	if err := validateImagesOnlyFlags(true, false, 3, nil); err == nil {
		t.Fatal("expected error for multi-host images-only without --all-nodes")
	}
	if err := validateImagesOnlyFlags(true, false, 0, fmtTestError("discoverd down")); err == nil {
		t.Fatal("expected error when host count cannot be discovered")
	}
}

type fmtTestError string

func (e fmtTestError) Error() string { return string(e) }
