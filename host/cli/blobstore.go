package cli

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/flynn/go-docopt"
	"github.com/randy-girard/flynn/blobstore/data"
	controller "github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/cluster"
	"github.com/randy-girard/flynn/pkg/httpclient"
)

var blobstoreBackendNameRe = regexp.MustCompile(`^[a-z0-9]+$`)

func init() {
	Register("blobstore", runBlobstoreStatusCmd, `
usage: flynn-host blobstore

Show the blobstore default backend and configured extra backends.
`)
	Register("blobstore:status", runBlobstoreStatusCmd, `
usage: flynn-host blobstore:status

Show the blobstore default backend and configured extra backends.

Secrets in BACKEND_* env are redacted. This reads the same DEFAULT_BACKEND
and BACKEND_<name> variables blobstore itself uses.
`)
	Register("blobstore:set", runBlobstoreSetCmd, `
usage: flynn-host blobstore:set --backend=<s3|minio> [--name=<name>] --bucket=<bucket> [--region=<region>] [--endpoint=<endpoint>] [--insecure] [--access-key-id=<id>] [--secret-access-key=<key>] [--ec2-role] [--migrate] [--delete]

Configure an S3-compatible blobstore backend and make it the default.

Writes BACKEND_<name> and DEFAULT_BACKEND on the blobstore app (the same env
path as flynn -a blobstore env:set). Default --name is s3main for s3 and
minio for minio. --delete implies --migrate (copy existing objects then
remove them from the previous backend).

Options:
    --backend=<s3|minio>       Backend type (s3 or minio)
    --name=<name>              BACKEND_<name> key (default s3main or minio)
    --bucket=<bucket>          Bucket name
    --region=<region>          S3 region, or MinIO location [default: us-east-1]
    --endpoint=<endpoint>      MinIO / S3-compatible endpoint host:port
    --insecure                 Use HTTP instead of HTTPS (MinIO)
    --access-key-id=<id>       Access key (not required with --ec2-role)
    --secret-access-key=<key>  Secret key (not required with --ec2-role)
    --ec2-role                 Use the EC2 instance role for S3
    --migrate                  Run flynn-blobstore migrate after switching
    --delete                   Delete objects from the old backend after migrate

Examples:
    $ flynn-host blobstore:set --backend=s3 --bucket=flynnblobstore --region=us-east-1 --access-key-id=$AWS_ACCESS_KEY_ID --secret-access-key=$AWS_SECRET_ACCESS_KEY --migrate --delete
    $ flynn-host blobstore:set --backend=minio --bucket=flynnblobstore --endpoint=192.168.56.20:9000 --insecure --access-key-id=minio --secret-access-key=minio123 --migrate --delete
`)
	Register("blobstore:credentials", runBlobstoreCredentialsCmd, `
usage: flynn-host blobstore:credentials --access-key-id=<id> --secret-access-key=<key> [--name=<name>]

Rotate access keys on an existing S3-compatible blobstore backend.

Updates access_key_id and secret_access_key on BACKEND_<name>. If --name is
omitted, the current DEFAULT_BACKEND is used (must not be postgres).

Options:
    --name=<name>              Backend name (default: DEFAULT_BACKEND)
    --access-key-id=<id>       New access key
    --secret-access-key=<key>  New secret key
`)
	Register("blobstore:migrate", runBlobstoreMigrateCmd, `
usage: flynn-host blobstore:migrate [--delete]

Move objects from other backends onto the current DEFAULT_BACKEND.

Runs /bin/flynn-blobstore migrate in a blobstore one-off (same as
flynn -a blobstore run /bin/flynn-blobstore migrate).

Options:
    --delete    Delete objects from the source backend after copying
`)
}

func runBlobstoreStatusCmd(_ *docopt.Args) error {
	client, err := getControllerClient()
	if err != nil {
		return fmt.Errorf("error connecting to controller: %s", err)
	}
	return runBlobstoreStatus(client, os.Stdout)
}

func runBlobstoreSetCmd(args *docopt.Args) error {
	client, err := getControllerClient()
	if err != nil {
		return fmt.Errorf("error connecting to controller: %s", err)
	}
	return runBlobstoreSet(args, client, os.Stdout)
}

func runBlobstoreCredentialsCmd(args *docopt.Args) error {
	client, err := getControllerClient()
	if err != nil {
		return fmt.Errorf("error connecting to controller: %s", err)
	}
	return runBlobstoreCredentials(args, client, os.Stdout)
}

func runBlobstoreMigrateCmd(args *docopt.Args) error {
	client, err := getControllerClient()
	if err != nil {
		return fmt.Errorf("error connecting to controller: %s", err)
	}
	return runBlobstoreMigrateJob(client, args.Bool["--delete"], os.Stdout, os.Stderr)
}

type blobstoreEnvClient interface {
	GetApp(string) (*ct.App, error)
	GetAppRelease(string) (*ct.Release, error)
	CreateRelease(string, *ct.Release) error
	DeployAppRelease(string, string, <-chan struct{}) error
}

type blobstoreJobClient interface {
	blobstoreEnvClient
	RunJobAttached(string, *ct.NewJob) (httpclient.ReadWriteCloser, error)
}

func runBlobstoreStatus(client blobstoreEnvClient, w io.Writer) error {
	release, err := client.GetAppRelease("blobstore")
	if err != nil {
		return fmt.Errorf("error getting blobstore release: %s", err)
	}
	defaultName, backends, err := data.BackendsFromEnv(data.EnvMapToSlice(release.Env))
	if err != nil {
		return err
	}
	_, _ = fmt.Fprint(w, formatBlobstoreStatus(defaultName, backends))
	return nil
}

func formatBlobstoreStatus(defaultName string, backends map[string]map[string]string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Default backend: %s\n", emptyDash(defaultName))
	names := make([]string, 0, len(backends))
	for name := range backends {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		info := data.RedactBackendInfo(backends[name])
		kind := info["backend"]
		if kind == "" {
			kind = name
		}
		marker := ""
		if name == defaultName {
			marker = " (default)"
		}
		fmt.Fprintf(&b, "\n%s%s (%s)\n", name, marker, kind)
		keys := make([]string, 0, len(info))
		for k := range info {
			if k == "backend" {
				continue
			}
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if len(keys) == 0 {
			fmt.Fprintf(&b, "  (built-in)\n")
			continue
		}
		for _, k := range keys {
			fmt.Fprintf(&b, "  %s=%s\n", k, info[k])
		}
	}
	return b.String()
}

func runBlobstoreSet(args *docopt.Args, client blobstoreJobClient, w io.Writer) error {
	kind := strings.ToLower(strings.TrimSpace(args.String["--backend"]))
	name := strings.ToLower(strings.TrimSpace(args.String["--name"]))
	info, err := s3CompatibleBackendInfo(kind, name, args)
	if err != nil {
		return err
	}
	name = info["_name"]
	delete(info, "_name")
	if _, err := client.GetAppRelease("blobstore"); err != nil {
		return fmt.Errorf("error getting blobstore release: %s", err)
	}
	env := blobstoreBackendEnv(name, info)
	id, err := setBlobstoreEnv(client, env)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "Configured blobstore backend %s (%s), default=%s (release %s).\n", name, kind, name, id)
	fmt.Fprintf(w, "If the credentials are invalid, check flynn -a blobstore log.\n")
	if args.Bool["--migrate"] || args.Bool["--delete"] {
		return runBlobstoreMigrateJob(client, args.Bool["--delete"], w, os.Stderr)
	}
	fmt.Fprintf(w, "Migrate existing objects with: flynn-host blobstore:migrate --delete\n")
	return nil
}

func s3CompatibleBackendInfo(kind, name string, args *docopt.Args) (map[string]string, error) {
	if kind != "s3" && kind != "minio" {
		return nil, fmt.Errorf("--backend must be s3 or minio")
	}
	if name == "" {
		if kind == "s3" {
			name = "s3main"
		} else {
			name = "minio"
		}
	}
	if !blobstoreBackendNameRe.MatchString(name) {
		return nil, fmt.Errorf("--name must be lowercase alphanumeric (got %q)", name)
	}
	bucket := strings.TrimSpace(args.String["--bucket"])
	if bucket == "" {
		return nil, fmt.Errorf("--bucket is required")
	}
	info := map[string]string{
		"backend": kind,
		"bucket":  bucket,
		"_name":   name,
	}
	region := strings.TrimSpace(args.String["--region"])
	if region == "" {
		region = "us-east-1"
	}
	ec2Role := args.Bool["--ec2-role"]
	id := strings.TrimSpace(args.String["--access-key-id"])
	secret := strings.TrimSpace(args.String["--secret-access-key"])
	endpoint := strings.TrimSpace(args.String["--endpoint"])
	insecure := args.Bool["--insecure"]

	switch kind {
	case "s3":
		info["region"] = region
		if ec2Role {
			info["ec2_role"] = "true"
		} else if id == "" || secret == "" {
			return nil, fmt.Errorf("--access-key-id and --secret-access-key are required (or pass --ec2-role)")
		} else {
			info["access_key_id"] = id
			info["secret_access_key"] = secret
		}
	case "minio":
		if endpoint == "" {
			return nil, fmt.Errorf("--endpoint is required for minio")
		}
		if id == "" || secret == "" {
			return nil, fmt.Errorf("--access-key-id and --secret-access-key are required for minio")
		}
		info["endpoint"] = endpoint
		info["access_key_id"] = id
		info["secret_access_key"] = secret
		info["location"] = region
		if insecure {
			info["insecure"] = "true"
		}
	}
	return info, nil
}

func blobstoreBackendEnv(name string, info map[string]string) map[string]*string {
	params := data.EncodeBackendParams(info)
	key := data.BackendEnvKey(name)
	env := map[string]*string{
		key:               &params,
		"DEFAULT_BACKEND": &name,
	}
	// Overlay env wins over the params string; drop credential overlays so
	// the BACKEND_<name> value is the source of truth after set/rotate.
	for _, param := range []string{"access_key_id", "secret_access_key", "account_key", "key"} {
		k := data.BackendOverlayEnvKey(name, param)
		env[k] = nil
	}
	return env
}

func runBlobstoreCredentials(args *docopt.Args, client blobstoreEnvClient, w io.Writer) error {
	id := strings.TrimSpace(args.String["--access-key-id"])
	secret := strings.TrimSpace(args.String["--secret-access-key"])
	if id == "" || secret == "" {
		return fmt.Errorf("--access-key-id and --secret-access-key are required")
	}
	release, err := client.GetAppRelease("blobstore")
	if err != nil {
		return fmt.Errorf("error getting blobstore release: %s", err)
	}
	defaultName, backends, err := data.BackendsFromEnv(data.EnvMapToSlice(release.Env))
	if err != nil {
		return err
	}
	name := strings.ToLower(strings.TrimSpace(args.String["--name"]))
	if name == "" {
		name = defaultName
	}
	if name == "" || name == "postgres" {
		return fmt.Errorf("DEFAULT_BACKEND is postgres; pass --name for an S3-compatible backend")
	}
	info := backends[name]
	if info == nil {
		return fmt.Errorf("unknown blobstore backend %q (flynn-host blobstore:status)", name)
	}
	kind := info["backend"]
	if kind != "s3" && kind != "minio" {
		return fmt.Errorf("backend %q is %s, not S3-compatible", name, kind)
	}
	info["access_key_id"] = id
	info["secret_access_key"] = secret
	delete(info, "ec2_role")
	env := blobstoreBackendEnv(name, info)
	// Keep the current default unless this named backend is already default.
	if name != defaultName {
		env["DEFAULT_BACKEND"] = &defaultName
	}
	relID, err := setBlobstoreEnv(client, env)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "Rotated credentials for blobstore backend %s (release %s).\n", name, relID)
	return nil
}

func setBlobstoreEnv(client blobstoreEnvClient, env map[string]*string) (string, error) {
	app, err := client.GetApp("blobstore")
	if err != nil {
		return "", err
	}
	release, err := client.GetAppRelease(app.ID)
	if err == controller.ErrNotFound {
		return "", fmt.Errorf("blobstore has no release")
	}
	if err != nil {
		return "", err
	}
	if release.Env == nil {
		release.Env = make(map[string]string, len(env))
	}
	for k, v := range env {
		if v == nil {
			delete(release.Env, k)
		} else {
			release.Env[k] = *v
		}
	}
	release.ID = ""
	if err := client.CreateRelease(app.ID, release); err != nil {
		return "", err
	}
	if err := client.DeployAppRelease(app.ID, release.ID, nil); err != nil {
		return "", err
	}
	return release.ID, nil
}

func runBlobstoreMigrateJob(client blobstoreJobClient, deleteObjects bool, stdout, stderr io.Writer) error {
	release, err := client.GetAppRelease("blobstore")
	if err != nil {
		return fmt.Errorf("error getting blobstore release: %s", err)
	}
	args := []string{"/bin/flynn-blobstore", "migrate"}
	if deleteObjects {
		args = append(args, "--delete")
	}
	fmt.Fprintf(stdout, "Migrating blobstore objects onto %s...\n", emptyDash(release.Env["DEFAULT_BACKEND"]))
	req := &ct.NewJob{
		Args:                 args,
		ReleaseID:            release.ID,
		ReleaseEnv:           true,
		DisableLog:           true,
		DeprecatedEntrypoint: []string{args[0]},
		DeprecatedCmd:        args[1:],
	}
	rwc, err := client.RunJobAttached("blobstore", req)
	if err != nil {
		return fmt.Errorf("error running blobstore migrate: %s", err)
	}
	defer rwc.Close()
	attachClient := cluster.NewAttachClient(rwc)
	exitStatus, err := attachClient.Receive(stdout, stderr)
	if err != nil {
		return err
	}
	if exitStatus != 0 {
		return fmt.Errorf("blobstore migrate exited with status %d", exitStatus)
	}
	return nil
}
