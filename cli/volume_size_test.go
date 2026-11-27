package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flynn/go-docopt"
	controller "github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
)

func volumeSizeServer(t *testing.T) controller.Client {
	t.Helper()
	vols := []*ct.Volume{
		{ID: "v-sized", HostID: "h1", State: ct.VolumeStateCreated, VolumeReq: ct.VolumeReq{Size: 20 * 1024 * 1024 * 1024}},
		{ID: "v-legacy", HostID: "h1", State: ct.VolumeStateCreated},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/apps/shop/volumes":
			json.NewEncoder(w).Encode(vols)
		case "/apps/shop/volumes/v-sized":
			json.NewEncoder(w).Encode(vols[0])
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	c, err := controller.NewClientWithToken(srv.URL, "token")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// flynn volume and flynn volume:show print the size the controller stores
// (NEW-28); a volume with no recorded size (from before the controller
// stored it, or a legacy uncapped dataset) shows "-".
func TestVolumeListShowsSize(t *testing.T) {
	c := volumeSizeServer(t)
	prev := flagApp
	flagApp = "shop"
	defer func() { flagApp = prev }()
	out := captureStdout(t, func() {
		if err := runVolumeList(&docopt.Args{}, c); err != nil {
			t.Fatal(err)
		}
	})
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 || !strings.Contains(lines[0], "SIZE") {
		t.Fatalf("output:\n%s\nwant a SIZE column and two rows", out)
	}
	if !strings.Contains(lines[1], "v-sized") || !strings.Contains(lines[1], "20 GiB") {
		t.Fatalf("sized row %q, want 20 GiB", lines[1])
	}
	if !strings.Contains(lines[2], "v-legacy") || !strings.Contains(lines[2], " - ") {
		t.Fatalf("legacy row %q, want -", lines[2])
	}

	show := captureStdout(t, func() {
		args := &docopt.Args{String: map[string]string{"<id>": "v-sized"}, Bool: map[string]bool{}}
		if err := runVolumeShow(args, c); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(show, "Size:") || !strings.Contains(show, "20 GiB") {
		t.Fatalf("volume:show output:\n%s\nwant Size: 20 GiB", show)
	}
}
