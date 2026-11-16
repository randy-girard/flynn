package cli

import (
	"os"
	"strings"
	"testing"

	"github.com/randy-girard/flynn/controller/region"
	ct "github.com/randy-girard/flynn/controller/types"
)

func TestRegionCommandsRegistered(t *testing.T) {
	for _, name := range []string{"region", "region:list", "region:link", "region:unlink"} {
		if commands[name] == nil {
			t.Fatalf("missing flynn-host %s", name)
		}
	}
	usage := commands["region:link"].usage
	for _, flag := range []string{"--region", "--domain", "--tls-pin"} {
		if !strings.Contains(usage, flag) {
			t.Fatalf("region:link missing %s", flag)
		}
	}
	if !strings.Contains(commands["bootstrap"].usage, "--region") {
		t.Fatal("bootstrap must accept --region")
	}
}

func TestRegionLinkBodyHasNoClusterKey(t *testing.T) {
	body := regionLinkBody("EU", "eu.example.com", "pin")
	if body["region"] != "EU" || body["domain"] != "eu.example.com" || body["tls_pin"] != "pin" {
		t.Fatalf("%v", body)
	}
	if _, ok := body["controller_key"]; ok {
		t.Fatal("region:link must not send CONTROLLER_KEY")
	}
	if len(body) != 3 {
		t.Fatalf("link body has extra fields: %v", body)
	}
}

func TestFormatRegionListDoesNotDial(t *testing.T) {
	text := formatRegionList(ct.ClusterRegionList{
		Region: "US",
		Links: []ct.RegionLink{{
			Region:        "EU",
			Domain:        "eu.example.com",
			ControllerURL: "https://controller.eu.example.com",
			DashboardURL:  "https://dashboard.eu.example.com",
			TLSPin:        "pin",
		}},
	})
	if !strings.Contains(text, "US") || !strings.Contains(text, "EU") || !strings.Contains(text, "eu.example.com") {
		t.Fatalf("%s", text)
	}
	if strings.Contains(strings.ToLower(text), "controller_key") {
		t.Fatal(text)
	}
}

func TestBootstrapRegionFlag(t *testing.T) {
	apply, name, err := region.BootstrapApply(false, false, "")
	if err != nil || !apply || name != "US" {
		t.Fatalf("omitted: %v %q %v", apply, name, err)
	}
	apply, _, err = region.BootstrapApply(true, false, "EU")
	if err != nil || apply {
		t.Fatal("--from-backup must keep the region stored in the backup")
	}
}

func TestInstallBootstrapsOmitRegion(t *testing.T) {
	for _, path := range []string{
		"../../script/vagrant/guest/bootstrap-node.sh",
		"../../test/cluster/cluster.go",
		"../../test/cluster2/cluster.go",
		"../../test/README.md",
	} {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "--region") {
			t.Fatalf("%s passes --region; omitted bootstraps must store US", path)
		}
	}
}

func TestBlobstoreSetDoesNotWriteClusterRegion(t *testing.T) {
	b, err := os.ReadFile("blobstore.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "cluster_settings") || strings.Contains(string(b), "cluster_region") {
		t.Fatal("blobstore:set --region must stay the object-store location")
	}
	args := parseHostCLI(t, "blobstore:set", []string{
		"blobstore:set", "--backend=s3", "--bucket=flynnblobstore",
		"--region=us-east-1", "--access-key-id=AKIA", "--secret-access-key=s3cret",
	})
	info, err := s3CompatibleBackendInfo("s3", "", args)
	if err != nil {
		t.Fatal(err)
	}
	if info["region"] != "us-east-1" {
		t.Fatalf("blobstore region = %q", info["region"])
	}
}
