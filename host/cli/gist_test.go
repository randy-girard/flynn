package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/inconshreveable/log15"
	host "github.com/randy-girard/flynn/host/types"
)

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

func readTarball(t *testing.T, path string) map[string]string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	files := make(map[string]string)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if hdr.Mode&0077 != 0 {
			t.Errorf("tar entry %s mode %o is readable by group/other", hdr.Name, hdr.Mode)
		}
		data, err := ioutil.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		files[hdr.Name] = string(data)
	}
	return files
}

func TestCreateTarballIsOwnerOnlyAndReturnsCloseError(t *testing.T) {
	g := &Gist{Files: make(map[string]File)}
	g.AddFile("0-debug-output.log", "hello")

	dir := t.TempDir()
	path := filepath.Join(dir, "debug.tar.gz")
	// A pre-existing world-readable file at --filename must not keep its mode.
	if err := ioutil.WriteFile(path, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if err := g.CreateTarball(path); err != nil {
		t.Fatalf("CreateTarball: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := fi.Mode().Perm(); mode != 0600 {
		t.Fatalf("tarball mode = %o, want 0600", mode)
	}
	if got := readTarball(t, path)["0-debug-output.log"]; got != "hello" {
		t.Fatalf("tarball content = %q", got)
	}

	// The gzip trailer is only written on Close; a failed flush must be
	// reported rather than ignored.
	if err := g.WriteTarball(failingWriter{}); err == nil {
		t.Fatal("WriteTarball to a failing writer returned nil")
	}

	// A failed write leaves no partial tarball behind at the final path.
	missing := filepath.Join(dir, "no-such-dir", "debug.tar.gz")
	if err := g.CreateTarball(missing); err == nil {
		t.Fatal("CreateTarball into a missing directory returned nil")
	}
}

// Representative secrets as they appear in `ps faux`, job logs, job
// descriptions and JSON state.
const (
	secHostAuth      = "hostauth-5f2b9c1d7e"
	secDiscoverdAuth = "discoverd-a81c33f0"
	secControllerKey = "ctrlkey-9d8e7f6a5b"
	secJoinToken     = "jointoken-77aa88bb"
	secGitHubToken   = "ghp_abcdefghijklmnop123456"
	secPGPassword    = "pgpassw0rd-44d1"
	secDBURLPassword = "dburlpass-e6c2"
	secBearer        = "eyJhbGciOiJIUzI1NiJ9.payload.sig"
	secRedis         = "r3dispass-0b9c"
	secCurlKey       = "curlkey-1f2e3d"
	secMySQL         = "mysqlpw-8a7b"
	secFlag          = "flagsecret-6c5d"
	secAWS           = "awsSecretValue/abc+def"
	secJobEnv        = "jobenvsecret-3344"
	secURLToken      = "urltoken-x9y8z7"
)

func writeHostJSON(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "host.json")
	body := `{"args":["--external-ip","192.0.2.200"],"env":{
		"FLYNN_HOST_AUTH_KEY":"` + secHostAuth + `",
		"DISCOVERD_AUTH_KEY":"` + secDiscoverdAuth + `",
		"CONTROLLER_KEY":"` + secControllerKey + `",
		"FLYNN_HOST_JOIN_TOKEN":"` + secJoinToken + `",
		"DISCOVERD":"http://192.0.2.200:1111"}}`
	if err := ioutil.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDebugInfoRedactsHostSecrets(t *testing.T) {
	r := newDebugRedactor()
	if err := r.AddHostConfig(writeHostJSON(t)); err != nil {
		t.Fatal(err)
	}
	r.AddValue(secGitHubToken)
	r.AddEnv(map[string]string{
		"APP_SECRET":   secJobEnv,
		"DATABASE_URL": "postgres://app:" + secDBURLPassword + "@leader.postgres.discoverd:5432/app",
		"PORT":         "8080",
		"FLYNN_APP_ID": "0123456789abcdef",
	})

	g := &Gist{Files: make(map[string]File)}
	g.AddFile("0-debug-output.log", strings.Join([]string{
		"===> ps faux",
		"root  1234  /usr/local/bin/flynn-host daemon --external-ip 192.0.2.200",
		"root  2345  curl -H \"Authorization: Bearer " + secBearer + "\" -u :" + secCurlKey + " http://controller.discoverd/apps",
		"root  3456  redis-cli -h 10.0.0.5 -a " + secRedis + " ping",
		"root  4567  mysqladmin -uroot -p" + secMySQL + " status",
		"root  5678  /bin/some-daemon --admin-password=" + secFlag + " --log-dir /var/log/flynn",
		"root  6789  psql postgres://flynn:" + secPGPassword + "@leader.postgres.discoverd:5432/controller",
		"root  7890  git fetch https://" + secURLToken + "@github.com/org/repo.git",
		"root  8901  flynn-host collect-debug-info --github-token=" + secGitHubToken,
		"root  9012  app-worker APP_SECRET=" + secJobEnv + " FLYNN_APP_ID=0123456789abcdef PORT=8080",
	}, "\n"))
	g.AddFile("1-jobs.log", "env: AWS_SECRET_ACCESS_KEY="+secAWS+" PGPASSWORD="+secPGPassword+"\n"+
		"discoverd auth "+secDiscoverdAuth+" and host key "+secHostAuth+" and join "+secJoinToken+"\n"+
		"controller key in a log line: "+secControllerKey+"\n")
	g.AddFile("scheduler-state.json", `{"env":{"DATABASE_URL":"postgres://u:`+secDBURLPassword+`@h/d","CONTROLLER_KEY": "`+secControllerKey+`"}}`)
	g.AddFile("controller-web-1.log", "ID  job1\nENV[CONTROLLER_KEY]  "+secControllerKey+"\nENV[PORT]  8080\n")

	n := g.Redact(r)
	if n == 0 {
		t.Fatal("Redact reported no replacements")
	}

	all := ""
	size := 0
	for _, f := range g.Files {
		all += f.Content + "\n"
		size += len(f.Content)
	}
	if g.Size != size {
		t.Fatalf("gist Size = %d, want %d after redaction", g.Size, size)
	}
	for _, s := range []string{
		secHostAuth, secDiscoverdAuth, secControllerKey, secJoinToken, secGitHubToken,
		secPGPassword, secDBURLPassword, secBearer, secRedis, secCurlKey, secMySQL,
		secFlag, secAWS, secJobEnv, secURLToken,
	} {
		if strings.Contains(all, s) {
			t.Errorf("secret %q survived redaction", s)
		}
	}
	// Non-secret context stays useful.
	// The DISCOVERD value lives in host.json but is not a secret.
	if r.isKnownValue("http://192.0.2.200:1111") {
		t.Error("non-secret host.json DISCOVERD value treated as a secret")
	}
	for _, keep := range []string{
		"--external-ip 192.0.2.200", "leader.postgres.discoverd:5432",
		"redis-cli -h 10.0.0.5", "--log-dir /var/log/flynn", "ENV[PORT]  8080",
		"FLYNN_APP_ID=0123456789abcdef PORT=8080", "github.com/org/repo.git", "[REDACTED]",
	} {
		if !strings.Contains(all, keep) {
			t.Errorf("redaction removed non-secret context %q:\n%s", keep, all)
		}
	}
}

func TestDebugRedactorIgnoresShortAndMissingValues(t *testing.T) {
	r := newDebugRedactor()
	r.AddValue("")
	r.AddValue("true")
	r.AddEnv(map[string]string{"SOME_KEY": "1"})
	if err := r.AddHostConfig(filepath.Join(t.TempDir(), "missing.json")); err != nil {
		t.Fatalf("missing host.json: %v", err)
	}
	if got := r.Redact("status true 1 ok"); got != "status true 1 ok" {
		t.Fatalf("short values redacted: %q", got)
	}
	bad := filepath.Join(t.TempDir(), "bad.json")
	ioutil.WriteFile(bad, []byte("{"), 0600)
	if err := r.AddHostConfig(bad); err == nil {
		t.Fatal("malformed host.json returned nil")
	}
}

func TestCaptureJobDescRedactsSecretEnv(t *testing.T) {
	job := &host.ActiveJob{Job: &host.Job{ID: "job1", Config: host.ContainerConfig{Env: map[string]string{
		"CONTROLLER_KEY": secControllerKey,
		"PORT":           "8080",
	}}}}
	r := newDebugRedactor()
	var buf bytes.Buffer
	printJobDesc(job, &buf, true, secretEnvNames(job.Job.Config.Env))
	out := r.Redact(buf.String())
	if strings.Contains(out, secControllerKey) {
		t.Fatalf("job env secret printed: %s", out)
	}
	if !strings.Contains(out, "8080") {
		t.Fatalf("non-secret env removed: %s", out)
	}
}

type gistStub struct {
	mu       sync.Mutex
	requests int
	auth     string
	body     string
	sawOut   string
	out      *bytes.Buffer
}

func (s *gistStub) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests++
	s.auth = req.Header.Get("Authorization")
	b, _ := ioutil.ReadAll(req.Body)
	s.body = string(b)
	s.sawOut = s.out.String()
	w.WriteHeader(http.StatusCreated)
	io.WriteString(w, `{"html_url":"https://gist.example/abc"}`)
}

func newDebugGist() *Gist {
	g := &Gist{Files: make(map[string]File)}
	g.AddFile("0-debug-output.log", "root 1 daemon CONTROLLER_KEY="+secControllerKey+"\n")
	return g
}

func TestDebugInfoDefaultsToLocalTarball(t *testing.T) {
	var out bytes.Buffer
	stub := &gistStub{out: &out}
	srv := httptest.NewServer(stub)
	defer srv.Close()
	defer func(u string) { gistAPIURL = u }(gistAPIURL)
	gistAPIURL = srv.URL

	path := filepath.Join(t.TempDir(), "debug.tar.gz")
	r := newDebugRedactor()
	r.AddValue(secControllerKey)
	if err := finishDebugInfo(log15.New(), newDebugGist(), r, debugInfoOptions{Filename: path, Token: secGitHubToken, Out: &out}); err != nil {
		t.Fatal(err)
	}
	if stub.requests != 0 {
		t.Fatalf("default mode made %d upload requests", stub.requests)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0600 {
		t.Fatalf("tarball mode %o", fi.Mode().Perm())
	}
	if got := readTarball(t, path)["0-debug-output.log"]; strings.Contains(got, secControllerKey) || !strings.Contains(got, "[REDACTED]") {
		t.Fatalf("tarball not redacted: %q", got)
	}
}

func TestDebugInfoUploadIsOptInRedactedAndAnnounced(t *testing.T) {
	var out bytes.Buffer
	stub := &gistStub{out: &out}
	srv := httptest.NewServer(stub)
	defer srv.Close()
	defer func(u string) { gistAPIURL = u }(gistAPIURL)
	gistAPIURL = srv.URL

	r := newDebugRedactor()
	r.AddValue(secControllerKey)
	r.AddValue(secGitHubToken)
	g := newDebugGist()
	if err := finishDebugInfo(log15.New(), g, r, debugInfoOptions{Upload: true, Token: secGitHubToken, Out: &out}); err != nil {
		t.Fatal(err)
	}
	if stub.requests != 1 {
		t.Fatalf("upload requests = %d", stub.requests)
	}
	if stub.auth != "token "+secGitHubToken {
		t.Fatalf("Authorization = %q", stub.auth)
	}
	if strings.Contains(stub.body, secControllerKey) || strings.Contains(stub.body, secGitHubToken) {
		t.Fatalf("upload body not redacted: %s", stub.body)
	}
	if !strings.Contains(stub.body, `"public":false`) {
		t.Fatalf("gist is not secret: %s", stub.body)
	}
	if !strings.Contains(stub.sawOut, "0-debug-output.log") || !strings.Contains(stub.sawOut, srv.URL) {
		t.Fatalf("file list and destination not printed before upload; output was %q", stub.sawOut)
	}
	if g.URL != "https://gist.example/abc" {
		t.Fatalf("gist URL = %q", g.URL)
	}
}

func TestDebugInfoUploadNeedsToken(t *testing.T) {
	var out bytes.Buffer
	stub := &gistStub{out: &out}
	srv := httptest.NewServer(stub)
	defer srv.Close()
	defer func(u string) { gistAPIURL = u }(gistAPIURL)
	gistAPIURL = srv.URL

	err := finishDebugInfo(log15.New(), newDebugGist(), newDebugRedactor(), debugInfoOptions{Upload: true, Out: &out})
	if err == nil || !strings.Contains(err.Error(), "--github-token") {
		t.Fatalf("err = %v, want a --github-token hint", err)
	}
	if stub.requests != 0 {
		t.Fatalf("anonymous upload attempted")
	}
}

func TestDebugInfoUploadReportsServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	defer func(u string) { gistAPIURL = u }(gistAPIURL)
	gistAPIURL = srv.URL
	var out bytes.Buffer
	err := finishDebugInfo(log15.New(), newDebugGist(), newDebugRedactor(), debugInfoOptions{Upload: true, Token: "tok-123456", Out: &out})
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("err = %v", err)
	}
}

func TestCollectDebugInfoFlags(t *testing.T) {
	def := parseHostCLI(t, "collect-debug-info", []string{"collect-debug-info"})
	if opts, err := debugInfoOptionsFromArgs(def); err != nil || opts.Upload {
		t.Fatalf("default: upload=%v err=%v", opts.Upload, err)
	}
	up := parseHostCLI(t, "collect-debug-info", []string{"collect-debug-info", "--upload"})
	if opts, err := debugInfoOptionsFromArgs(up); err != nil || !opts.Upload {
		t.Fatalf("--upload: upload=%v err=%v", opts.Upload, err)
	}
	tb := parseHostCLI(t, "collect-debug-info", []string{"collect-debug-info", "--tarball", "--filename=/tmp/x.tgz"})
	if opts, err := debugInfoOptionsFromArgs(tb); err != nil || opts.Upload || opts.Filename != "/tmp/x.tgz" {
		t.Fatalf("--tarball: %+v err=%v", opts, err)
	}
	both := parseHostCLI(t, "collect-debug-info", []string{"collect-debug-info", "--tarball", "--upload"})
	if _, err := debugInfoOptionsFromArgs(both); err == nil {
		t.Fatal("--tarball --upload accepted")
	}
	upFile := parseHostCLI(t, "collect-debug-info", []string{"collect-debug-info", "--upload", "--filename=/tmp/x.tgz"})
	if _, err := debugInfoOptionsFromArgs(upFile); err == nil {
		t.Fatal("--upload --filename accepted")
	}
}
