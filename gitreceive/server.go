/*
gitreceive handles 'smart' Git HTTP requests for Flynn

This HTTP server can service 'git clone', 'git push' etc. commands
from Git clients that use the 'smart' Git HTTP protocol (git-upload-pack
and git-receive-pack).

Derived from https://gitlab.com/gitlab-org/gitlab-git-http-server
*/
package main

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"github.com/randy-girard/flynn/controller/authorizer"
	"github.com/randy-girard/flynn/controller/authz"
	controller "github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/controller/utils"
	"github.com/randy-girard/flynn/pkg/archiver"
	"github.com/randy-girard/flynn/pkg/blobstoreauth"
	"github.com/randy-girard/flynn/pkg/ctxhelper"
	"github.com/randy-girard/flynn/pkg/httphelper"
	"github.com/randy-girard/flynn/pkg/status"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "-ensure-preview" {
		if err := runEnsurePreview(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	key := os.Getenv("CONTROLLER_KEY")
	if key == "" {
		log.Fatal("missing CONTROLLER_KEY env var")
	}
	cc, err := controller.NewClient("", key)
	if err != nil {
		log.Fatalln("Unable to connect to controller:", err)
	}

	tokenKey, err := authorizer.ParseTokenKey(os.Getenv("ACCESS_TOKEN_KEY"))
	if err != nil {
		log.Fatalln("error decoding ACCESS_TOKEN_KEY:", err)
	}
	tokenMaxValidity, err := authorizer.ParseTokenMaxValidity(os.Getenv("ACCESS_TOKEN_MAX_VALIDITY"))
	if err != nil {
		log.Fatalln("error parsing ACCESS_TOKEN_MAX_VALIDITY:", err)
	}
	auth := authorizer.New([]string{key}, nil, tokenKey, tokenMaxValidity)

	log.Fatal(http.ListenAndServe(":"+os.Getenv("PORT"), httphelper.ContextInjector("gitreceive", httphelper.NewRequestLogger(newGitHandler(cc, auth)))))
}

// appLookup is the GetApp subset gitreceive needs. The cluster-key controller
// client is used for the lookup; grants are checked against the returned app.
type appLookup interface {
	GetApp(id string) (*ct.App, error)
}

type gitHandler struct {
	controller appLookup
	auth       *authorizer.Authorizer
	preview    previewLookup
	webhookURL string
	httpClient *http.Client
	prepare    func(cacheKey string) (string, error)
	upload     func(path, cacheKey string) error
}

type gitService struct {
	method     string
	suffix     string
	handleFunc func(gitEnv, string, string, http.ResponseWriter, *http.Request) bool
	rpc        string
}

type gitEnv struct {
	App      string
	Pipeline string
}

// Routing table
var gitServices = [...]gitService{
	{"GET", "/info/refs", handleGetInfoRefs, ""},
	{"POST", "/git-upload-pack", handlePostRPC, "git-upload-pack"},
	{"POST", "/git-receive-pack", handlePostRPC, "git-receive-pack"},
}

func newGitHandler(controller appLookup, auth *authorizer.Authorizer) *gitHandler {
	url := os.Getenv("CONTROLLER_URL")
	if url == "" {
		url = "http://controller.discoverd"
	}
	return &gitHandler{
		controller: controller,
		auth:       auth,
		preview:    newPipelinePreviewClient(),
		webhookURL: strings.TrimRight(url, "/") + "/github/webhook",
		httpClient: http.DefaultClient,
		prepare:    prepareRepo,
		upload:     uploadRepo,
	}
}

func (h *gitHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var g gitService

	if r.URL.Path == status.Path {
		status.HealthyHandler.ServeHTTP(w, r)
		return
	}

	if r.Method == http.MethodPost && r.URL.Path == "/github/webhook" {
		h.proxyGitHubWebhook(w, r)
		return
	}

	tok, err := h.auth.AuthorizeRequest(r)
	if err != nil {
		w.Header().Set("WWW-Authenticate", "Basic")
		http.Error(w, "Authentication required", 401)
		return
	}

	// Look for a matching Git service
	foundService := false
	for _, g = range gitServices {
		if r.Method == g.method && strings.HasSuffix(r.URL.Path, g.suffix) {
			foundService = true
			break
		}
	}
	kind, name := parseGitRepoPath(r.URL.Path, g.suffix)
	if !foundService || kind == "" || (kind == "app" && !utils.AppNamePattern.MatchString(name)) {
		// The protocol spec in git/Documentation/technical/http-protocol.txt
		// says we must return 403 if no matching service is found.
		http.Error(w, "Forbidden", 403)
		return
	}

	rpc := g.rpc
	if rpc == "" {
		rpc = r.URL.Query().Get("service")
	}

	if kind == "preview" {
		h.servePreview(w, r, tok, g, name, rpc)
		return
	}

	// Lookup app with the cluster-key client, then enforce the caller's grants
	// against app.ID. Git remotes address apps by name; dashboard/build tokens
	// grant by UUID, so GetApp-as-caller would 403 even for a legitimate push.
	app, err := h.controller.GetApp(name)
	if err == controller.ErrNotFound {
		http.Error(w, "unknown app", 404)
		return
	} else if err != nil {
		fail500(w, "getApp", err)
		return
	}

	if rpc == "git-receive-pack" {
		if !authz.GitPushAllowed(tok, app.ID, app.Name) {
			http.Error(w, "Forbidden", 403)
			return
		}
	} else if !authz.GitFetchAllowed(tok, app.ID, app.Name) {
		http.Error(w, "Forbidden", 403)
		return
	}

	prepare := h.prepare
	if prepare == nil {
		prepare = prepareRepo
	}
	repoPath, err := prepare(app.ID)
	if err != nil {
		fail500(w, "prepareRepo", err)
		return
	}
	defer os.RemoveAll(repoPath)

	success := g.handleFunc(gitEnv{App: app.ID}, g.rpc, repoPath, w, r)
	if success && g.rpc == "git-receive-pack" {
		if err := uploadRepo(repoPath, app.ID); err != nil {
			logError(w, "uploadRepo", err)
		}
	}
}

func (h *gitHandler) servePreview(w http.ResponseWriter, r *http.Request, tok *authorizer.Token, g gitService, pipeline, rpc string) {
	if h.preview == nil {
		http.Error(w, "preview remotes require the pipelines plugin", 404)
		return
	}
	info, err := h.preview.PreviewRemote(pipeline)
	if err != nil {
		if strings.Contains(err.Error(), "not available") || strings.Contains(err.Error(), "not enabled") || strings.Contains(err.Error(), "not found") {
			http.Error(w, err.Error(), 404)
			return
		}
		fail500(w, "previewRemote", err)
		return
	}
	if info == nil || !info.Available {
		reason := "preview remote is not available"
		if info != nil && strings.TrimSpace(info.Reason) != "" {
			reason = info.Reason
		}
		http.Error(w, reason, 404)
		return
	}

	if rpc == "git-receive-pack" {
		if !previewGitAllowed(tok, info, authz.GitPushAllowed) {
			http.Error(w, "Forbidden", 403)
			return
		}
	} else if !previewGitAllowed(tok, info, authz.GitFetchAllowed) {
		http.Error(w, "Forbidden", 403)
		return
	}

	cacheKey := "preview-" + pipeline
	prepare := h.prepare
	if prepare == nil {
		prepare = prepareRepo
	}
	repoPath, err := prepare(cacheKey)
	if err != nil {
		fail500(w, "prepareRepo", err)
		return
	}
	defer os.RemoveAll(repoPath)
	if err := writeRepoHookBytes(repoPath, previewPrereceiveHook); err != nil {
		fail500(w, "writePreviewHook", err)
		return
	}
	// A deleted preview app leaves refs/heads/<branch> in this repo. Git then
	// reports the next push of that commit as already up to date and never
	// runs the hook, so the app is not created again.
	if info.Branches != nil {
		pruned, err := dropStalePreviewHeads(repoPath, info.Branches)
		if err != nil {
			fail500(w, "dropPreviewBranch", err)
			return
		}
		if pruned {
			upload := h.upload
			if upload == nil {
				upload = uploadRepo
			}
			if err := upload(repoPath, cacheKey); err != nil {
				fail500(w, "uploadRepo", err)
				return
			}
		}
	}

	success := g.handleFunc(gitEnv{Pipeline: pipeline}, g.rpc, repoPath, w, r)
	if success && g.rpc == "git-receive-pack" {
		if err := uploadRepo(repoPath, cacheKey); err != nil {
			logError(w, "uploadRepo", err)
		}
	}
}

func previewGitAllowed(tok *authorizer.Token, info *previewRemoteInfo, allow func(*authorizer.Token, string, string) bool) bool {
	if tok != nil && tok.HasClusterAdmin() {
		return true
	}
	if info == nil {
		return false
	}
	n := len(info.AppIDs)
	if len(info.AppNames) > n {
		n = len(info.AppNames)
	}
	for i := 0; i < n; i++ {
		id, name := "", ""
		if i < len(info.AppIDs) {
			id = info.AppIDs[i]
		}
		if i < len(info.AppNames) {
			name = info.AppNames[i]
		}
		if allow(tok, id, name) {
			return true
		}
	}
	return false
}

func handleGetInfoRefs(env gitEnv, _ string, path string, w http.ResponseWriter, r *http.Request) bool {
	rpc := r.URL.Query().Get("service")
	if !(rpc == "git-upload-pack" || rpc == "git-receive-pack") {
		// The 'dumb' Git HTTP protocol is not supported
		http.Error(w, "Not Found", 404)
		return false
	}

	// Prepare our Git subprocess
	cmd, pipe := gitCommand(env, "git", subCommand(rpc), "--stateless-rpc", "--advertise-refs", path)
	if err := cmd.Start(); err != nil {
		fail500(w, "handleGetInfoRefs", err)
		return false
	}
	defer cleanUpProcessGroup(cmd) // Ensure brute force subprocess clean-up

	// Start writing the response
	w.Header().Add("Content-Type", fmt.Sprintf("application/x-%s-advertisement", rpc))
	w.Header().Add("Cache-Control", "no-cache")
	w.WriteHeader(200) // Don't bother with HTTP 500 from this point on, just return
	if err := pktLine(w, fmt.Sprintf("# service=%s\n", rpc)); err != nil {
		logError(w, "handleGetInfoRefs response", err)
		return false
	}
	if err := pktFlush(w); err != nil {
		logError(w, "handleGetInfoRefs response", err)
		return false
	}
	if _, err := io.Copy(w, pipe); err != nil {
		logError(w, "handleGetInfoRefs read from subprocess", err)
		return false
	}
	if err := cmd.Wait(); err != nil {
		logError(w, "handleGetInfoRefs wait for subprocess", err)
		return false
	}

	return true
}

func handlePostRPC(env gitEnv, rpc string, path string, w http.ResponseWriter, r *http.Request) bool {

	// The client request body may have been gzipped.
	body := r.Body
	if r.Header.Get("Content-Encoding") == "gzip" {
		var err error
		body, err = gzip.NewReader(r.Body)
		if err != nil {
			fail500(w, "handlePostRPC", err)
			return false
		}
	}

	// Prepare our Git subprocess
	cmd, pipe := gitCommand(env, "git", subCommand(rpc), "--stateless-rpc", path)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		fail500(w, "handlePostRPC", err)
		return false
	}
	defer stdin.Close()
	if err := cmd.Start(); err != nil {
		fail500(w, "handlePostRPC", err)
		return false
	}
	go func(done <-chan bool) {
		<-done
		cleanUpProcessGroup(cmd) // Ensure brute force subprocess clean-up
	}(w.(http.CloseNotifier).CloseNotify())

	// Write the client request body to Git's standard input
	if _, err := io.Copy(stdin, body); err != nil {
		fail500(w, "handlePostRPC write to subprocess", err)
		return false
	}

	// Start writing the response
	w.Header().Add("Content-Type", fmt.Sprintf("application/x-%s-result", rpc))
	w.Header().Add("Cache-Control", "no-cache")
	w.WriteHeader(200) // Don't bother with HTTP 500 from this point on, just return
	if _, err := io.Copy(newWriteFlusher(w), pipe); err != nil {
		logError(w, "handlePostRPC read from subprocess", err)
		return false
	}
	if err := cmd.Wait(); err != nil {
		logError(w, "handlePostRPC wait for subprocess", err)
		return false
	}

	return true
}

func fail500(w http.ResponseWriter, context string, err error) {
	http.Error(w, "Internal server error", 500)
	logError(w, context, err)
}

func logError(w http.ResponseWriter, msg string, err error) {
	logger, _ := ctxhelper.LoggerFromContext(w.(*httphelper.ResponseWriter).Context())
	logger.Error(msg, "error", err)
}

// Git subprocess helpers
func subCommand(rpc string) string {
	return strings.TrimPrefix(rpc, "git-")
}

func gitCommand(env gitEnv, name string, args ...string) (*exec.Cmd, io.Reader) {
	cmd := exec.Command(name, args...)
	// Start the command in its own process group (nice for signalling)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Explicitly set the environment for the Git command
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("RECEIVE_APP=%s", env.App),
		fmt.Sprintf("RECEIVE_PIPELINE=%s", env.Pipeline),
	)

	r, _ := cmd.StdoutPipe()
	cmd.Stderr = cmd.Stdout

	return cmd, r
}

func cleanUpProcessGroup(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}

	process := cmd.Process
	if process != nil && process.Pid > 0 {
		// Send SIGTERM to the process group of cmd
		syscall.Kill(-process.Pid, syscall.SIGTERM)
	}

	// reap our child process
	go cmd.Wait()
}

// Git HTTP line protocol functions
func pktLine(w io.Writer, s string) error {
	_, err := fmt.Fprintf(w, "%04x%s", len(s)+4, s)
	return err
}

func pktFlush(w io.Writer) error {
	_, err := fmt.Fprint(w, "0000")
	return err
}

func newWriteFlusher(w http.ResponseWriter) io.Writer {
	return writeFlusher{w.(interface {
		io.Writer
		http.Flusher
	})}
}

type writeFlusher struct {
	wf interface {
		io.Writer
		http.Flusher
	}
}

func (w writeFlusher) Write(p []byte) (int, error) {
	defer w.wf.Flush()
	return w.wf.Write(p)
}

// archiveTreeFunc prints a tar of rev on stdout. It must not checkout into
// this repository. receive-pack keeps the pushed objects in a quarantine
// directory until the hook exits 0, and checkout rewrites HEAD to that SHA.
// A failed deploy then discards the object, and the next push dies with
// "bad object HEAD" before this hook can run.
const archiveTreeFunc = `git-archive-all() {
	rev="$1"
	work="$(mktemp -d)"
	cleanup() { rm -rf "$work"; }
	trap cleanup RETURN
	git archive --format=tar "$rev" | tar -x -C "$work"
	if [ -f "$work/.gitmodules" ]; then
		links="$(mktemp)"
		git ls-tree -r "$rev" > "$links"
		while read -r mode type sha path; do
			[ "$mode" = "160000" ] || continue
			url="$(git config -f "$work/.gitmodules" --get "submodule.${path}.url" || true)"
			[ -n "$url" ] || continue
			dest="$work/$path"
			rm -rf "$dest"
			mkdir -p "$(dirname "$dest")"
			GIT_QUARANTINE_PATH= git clone --quiet "$url" "$dest"
			GIT_QUARANTINE_PATH= git -C "$dest" checkout --force --quiet "$sha"
		done < "$links"
		rm -f "$links"
	fi
	tar --create --exclude-vcs -C "$work" .
}
`

var prereceiveHook = []byte(`#!/bin/bash
set -eo pipefail;

` + archiveTreeFunc + `

while read oldrev newrev refname; do
	if [[ $refname = "refs/heads/master" ]] || [[ $refname = "refs/heads/main" ]]; then
		git-archive-all $newrev | stdbuf -oL -eL /bin/flynn-receiver "$RECEIVE_APP" "$newrev" --meta git=true --meta "git.commit=$newrev"| sed -u "s/^/"$'\e[1G\e[K'"/"
		deploy_pushed=1
		break
	fi
done

if [[ -z "${deploy_pushed}" ]]; then
  echo "The push must include a change to the master or main branch to be deployed."
  exit 1
fi
`)

func blobstoreCacheURL(cacheKey string) string {
	return fmt.Sprintf("http://blobstore.discoverd/repos/%s.tar", cacheKey)
}

func prepareRepo(cacheKey string) (string, error) {
	path, err := ioutil.TempDir("", "repo-"+cacheKey)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequest("GET", blobstoreCacheURL(cacheKey), nil)
	if err != nil {
		return "", err
	}
	blobstoreauth.Apply(req)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()

	if res.StatusCode == 404 {
		return path, initRepo(path)
	}
	if res.StatusCode != 200 {
		return "", fmt.Errorf("unexpected error %d retrieving cached repo", res.StatusCode)
	}

	if err := archiver.Untar(path, tar.NewReader(res.Body)); err != nil {
		return "", err
	}
	if err := setGitConfig(path); err != nil {
		return "", err
	}
	if err := writeRepoHook(path); err != nil {
		return "", err
	}
	if err := repairDanglingHEAD(path); err != nil {
		return "", err
	}

	return path, nil
}

func initRepo(path string) error {
	cmd := exec.Command("git", "init")
	cmd.Dir = path
	if err := cmd.Run(); err != nil {
		return err
	}
	if err := setGitConfig(path); err != nil {
		return err
	}
	return writeRepoHook(path)
}

func writeRepoHook(path string) error {
	return writeRepoHookBytes(path, prereceiveHook)
}

func writeRepoHookBytes(path string, hook []byte) error {
	return ioutil.WriteFile(filepath.Join(path, ".git", "hooks", "pre-receive"), hook, 0755)
}

func setGitConfig(path string) error {
	// autoDetach defaults to true, which runs git gc --auto in a forked process, corrupting uploads
	cmd := exec.Command("git", "config", "--bool", "gc.autoDetach", "false")
	cmd.Dir = path
	return cmd.Run()
}

var detachedObject = regexp.MustCompile(`^[0-9a-fA-F]{40}([0-9a-fA-F]{24})?$`)

// repairDanglingHEAD rewrites a detached HEAD whose object is missing back to
// an unborn symbolic ref. receive-pack refuses to start when HEAD is a SHA
// that is not in the object store ("bad object HEAD"). A symbolic ref is fine
// even when that branch does not exist yet.
func repairDanglingHEAD(path string) error {
	headPath := filepath.Join(path, ".git", "HEAD")
	raw, err := ioutil.ReadFile(headPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	head := strings.TrimSpace(string(raw))
	if strings.HasPrefix(head, "ref:") || !detachedObject.MatchString(head) {
		return nil
	}
	cmd := exec.Command("git", "cat-file", "-e", head)
	cmd.Dir = path
	if err := cmd.Run(); err == nil {
		return nil
	}
	return ioutil.WriteFile(headPath, []byte("ref: refs/heads/main\n"), 0644)
}

// dropStalePreviewHeads deletes branch refs that no longer have a preview
// app. live == nil means the caller does not know, and nothing is deleted.
func dropStalePreviewHeads(repo string, live []string) (bool, error) {
	if live == nil {
		return false, nil
	}
	keep := make(map[string]struct{}, len(live))
	for _, branch := range live {
		branch = strings.TrimSpace(branch)
		if branch != "" {
			keep[branch] = struct{}{}
		}
	}
	list := exec.Command("git", "for-each-ref", "--format=%(refname)", "refs/heads")
	list.Dir = repo
	out, err := list.Output()
	if err != nil {
		return false, err
	}
	changed := false
	for _, line := range strings.Split(string(out), "\n") {
		ref := strings.TrimSpace(line)
		if !strings.HasPrefix(ref, "refs/heads/") {
			continue
		}
		branch := strings.TrimPrefix(ref, "refs/heads/")
		if _, ok := keep[branch]; ok {
			continue
		}
		del := exec.Command("git", "update-ref", "-d", ref)
		del.Dir = repo
		if err := del.Run(); err != nil {
			return changed, err
		}
		changed = true
	}
	if !changed {
		return false, nil
	}
	return true, retargetDeletedHEAD(repo)
}

// retargetDeletedHEAD points HEAD at main when it still names a branch ref
// that was just deleted. A symbolic ref is valid even if that branch is absent.
func retargetDeletedHEAD(repo string) error {
	cmd := exec.Command("git", "symbolic-ref", "HEAD")
	cmd.Dir = repo
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	ref := strings.TrimSpace(string(out))
	if ref == "" || !strings.HasPrefix(ref, "refs/heads/") {
		return nil
	}
	check := exec.Command("git", "show-ref", "--verify", "--quiet", ref)
	check.Dir = repo
	if check.Run() == nil {
		return nil
	}
	set := exec.Command("git", "symbolic-ref", "HEAD", "refs/heads/main")
	set.Dir = repo
	return set.Run()
}

func uploadRepo(path, cacheKey string) error {
	// A failed hook can leave HEAD pointing at a quarantined SHA. Never
	// store that; the next push would die before the hook runs.
	if err := repairDanglingHEAD(path); err != nil {
		return err
	}
	r, w := io.Pipe()
	tw := tar.NewWriter(w)

	errCh := make(chan error)
	go func() {
		err := archiver.Tar(path, tw, func(n string) bool { return strings.HasSuffix(n, ".git") || strings.Contains(n, ".git/") })
		tw.Close()
		w.Close()
		errCh <- err
	}()

	// upload the tarball to the blobstore
	req, _ := http.NewRequest("PUT", blobstoreCacheURL(cacheKey), r)
	blobstoreauth.Apply(req)
	resp, err := http.DefaultClient.Do(req)
	if err := <-errCh; err != nil {
		return err
	}
	resp.Body.Close()
	return err
}

func (h *gitHandler) proxyGitHubWebhook(w http.ResponseWriter, r *http.Request) {
	if h.httpClient == nil {
		h.httpClient = http.DefaultClient
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	req, err := http.NewRequest(http.MethodPost, h.webhookURL, strings.NewReader(string(body)))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	for _, key := range []string{"X-GitHub-Event", "X-GitHub-Delivery", "X-GitHub-Hook-ID", "X-Hub-Signature", "X-Hub-Signature-256", "Content-Type", "User-Agent"} {
		if v := r.Header.Get(key); v != "" {
			req.Header.Set(key, v)
		}
	}
	if req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := h.httpClient.Do(req)
	if err != nil {
		fail500(w, "github webhook", err)
		return
	}
	defer res.Body.Close()
	for k, vs := range res.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(res.StatusCode)
	_, _ = io.Copy(w, res.Body)
}
