package cli

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/httpclient"
)

type stubBlobstoreClient struct {
	app      *ct.App
	release  *ct.Release
	created  *ct.Release
	deployed string
}

func (s *stubBlobstoreClient) GetApp(id string) (*ct.App, error) {
	if s.app == nil {
		return nil, errors.New("no app")
	}
	return s.app, nil
}

func (s *stubBlobstoreClient) GetAppRelease(id string) (*ct.Release, error) {
	if s.release == nil {
		return nil, errors.New("no release")
	}
	cp := *s.release
	env := make(map[string]string, len(s.release.Env))
	for k, v := range s.release.Env {
		env[k] = v
	}
	cp.Env = env
	return &cp, nil
}

func (s *stubBlobstoreClient) CreateRelease(appID string, release *ct.Release) error {
	release.ID = "rel-new"
	env := make(map[string]string, len(release.Env))
	for k, v := range release.Env {
		env[k] = v
	}
	cp := *release
	cp.Env = env
	s.created = &cp
	s.release = &cp
	return nil
}

func (s *stubBlobstoreClient) DeployAppRelease(appID, releaseID string, _ <-chan struct{}) error {
	s.deployed = releaseID
	return nil
}

func (s *stubBlobstoreClient) RunJobAttached(string, *ct.NewJob) (httpclient.ReadWriteCloser, error) {
	return nil, errors.New("RunJobAttached not expected")
}

func TestS3CompatibleBackendInfoDefaults(t *testing.T) {
	args := parseHostCLI(t, "blobstore:set", []string{
		"blobstore:set",
		"--backend=s3",
		"--bucket=flynnblobstore",
		"--access-key-id=AKIA",
		"--secret-access-key=s3cret",
	})
	info, err := s3CompatibleBackendInfo("s3", "", args)
	if err != nil {
		t.Fatal(err)
	}
	if info["_name"] != "s3main" || info["backend"] != "s3" || info["region"] != "us-east-1" || info["bucket"] != "flynnblobstore" {
		t.Fatalf("%v", info)
	}
}

func TestS3CompatibleBackendInfoMinio(t *testing.T) {
	args := parseHostCLI(t, "blobstore:set", []string{
		"blobstore:set",
		"--backend=minio",
		"--bucket=flynnblobstore",
		"--endpoint=192.168.56.20:19000",
		"--insecure",
		"--access-key-id=minio",
		"--secret-access-key=minio123",
		"--migrate",
		"--delete",
	})
	info, err := s3CompatibleBackendInfo("minio", "", args)
	if err != nil {
		t.Fatal(err)
	}
	if info["_name"] != "minio" || info["endpoint"] != "192.168.56.20:19000" || info["insecure"] != "true" {
		t.Fatalf("%v", info)
	}
	if !args.Bool["--migrate"] || !args.Bool["--delete"] {
		t.Fatal("migrate/delete flags")
	}
}

func TestS3CompatibleBackendInfoRequiresKeys(t *testing.T) {
	args := parseHostCLI(t, "blobstore:set", []string{
		"blobstore:set", "--backend=s3", "--bucket=b",
	})
	if _, err := s3CompatibleBackendInfo("s3", "", args); err == nil {
		t.Fatal("expected missing keys")
	}
}

func TestFormatBlobstoreStatusRedactsSecrets(t *testing.T) {
	got := formatBlobstoreStatus("minio", map[string]map[string]string{
		"postgres": {"backend": "postgres"},
		"minio": {
			"backend":           "minio",
			"endpoint":          "127.0.0.1:9000",
			"bucket":            "flynnblobstore",
			"access_key_id":     "minioadmin",
			"secret_access_key": "supersecret",
		},
	})
	if !strings.Contains(got, "Default backend: minio") {
		t.Fatalf("default:\n%s", got)
	}
	if !strings.Contains(got, "minio (default) (minio)") && !strings.Contains(got, "minio (default)") {
		t.Fatalf("named default:\n%s", got)
	}
	if strings.Contains(got, "supersecret") {
		t.Fatalf("secret leaked:\n%s", got)
	}
	if !strings.Contains(got, "secret_access_key=***") {
		t.Fatalf("redact:\n%s", got)
	}
	if !strings.Contains(got, "access_key_id=minioadmin") {
		t.Fatalf("access key:\n%s", got)
	}
}

func TestRunBlobstoreSetWritesBackendEnv(t *testing.T) {
	stub := &stubBlobstoreClient{
		app:     &ct.App{ID: "blob-app", Name: "blobstore"},
		release: &ct.Release{ID: "rel-old", Env: map[string]string{"AUTH_KEY": "k"}},
	}
	args := parseHostCLI(t, "blobstore:set", []string{
		"blobstore:set",
		"--backend=minio",
		"--bucket=flynnblobstore",
		"--endpoint=192.168.56.20:19000",
		"--insecure",
		"--access-key-id=minio",
		"--secret-access-key=minio123",
	})
	var buf bytes.Buffer
	if err := runBlobstoreSet(args, stub, &buf); err != nil {
		t.Fatal(err)
	}
	env := stub.created.Env
	if env["DEFAULT_BACKEND"] != "minio" {
		t.Fatalf("DEFAULT_BACKEND=%q", env["DEFAULT_BACKEND"])
	}
	got := env["BACKEND_MINIO"]
	for _, want := range []string{"backend=minio", "endpoint=192.168.56.20:19000", "insecure=true", "bucket=flynnblobstore", "access_key_id=minio", "secret_access_key=minio123"} {
		if !strings.Contains(got, want) {
			t.Fatalf("BACKEND_MINIO missing %q: %q", want, got)
		}
	}
	if env["AUTH_KEY"] != "k" {
		t.Fatal("AUTH_KEY must be preserved")
	}
	if stub.deployed != "rel-new" {
		t.Fatalf("deployed %q", stub.deployed)
	}
	if !strings.Contains(buf.String(), "blobstore:migrate --delete") {
		t.Fatalf("hint:\n%s", buf.String())
	}
}

func TestRunBlobstoreCredentialsRotatesExisting(t *testing.T) {
	stub := &stubBlobstoreClient{
		app: &ct.App{ID: "blob-app", Name: "blobstore"},
		release: &ct.Release{ID: "rel-old", Env: map[string]string{
			"DEFAULT_BACKEND": "minio",
			"BACKEND_MINIO":   "backend=minio endpoint=127.0.0.1:9000 bucket=b access_key_id=old secret_access_key=oldsecret",
		}},
	}
	args := parseHostCLI(t, "blobstore:credentials", []string{
		"blobstore:credentials",
		"--access-key-id=newid",
		"--secret-access-key=newsecret",
	})
	var buf bytes.Buffer
	if err := runBlobstoreCredentials(args, stub, &buf); err != nil {
		t.Fatal(err)
	}
	got := stub.created.Env["BACKEND_MINIO"]
	if !strings.Contains(got, "access_key_id=newid") || !strings.Contains(got, "secret_access_key=newsecret") {
		t.Fatalf("rotated: %q", got)
	}
	if !strings.Contains(got, "endpoint=127.0.0.1:9000") {
		t.Fatalf("kept params: %q", got)
	}
	if stub.created.Env["DEFAULT_BACKEND"] != "minio" {
		t.Fatal("default changed")
	}
}

func TestRunBlobstoreStatus(t *testing.T) {
	stub := &stubBlobstoreClient{
		app: &ct.App{ID: "blob-app"},
		release: &ct.Release{Env: map[string]string{
			"DEFAULT_BACKEND": "postgres",
		}},
	}
	var buf bytes.Buffer
	if err := runBlobstoreStatus(stub, &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "Default backend: postgres") {
		t.Fatalf("%s", buf.String())
	}
}

var _ blobstoreJobClient = (*stubBlobstoreClient)(nil)
var _ io.Writer = (*bytes.Buffer)(nil)
