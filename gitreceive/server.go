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
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/randy-girard/flynn/controller/authorizer"
	"github.com/randy-girard/flynn/controller/authz"
	"github.com/randy-girard/flynn/controller/buildtoken"
	controller "github.com/randy-girard/flynn/controller/client"
	"github.com/randy-girard/flynn/controller/tokensigner"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/controller/utils"
	"github.com/randy-girard/flynn/pkg/archiver"
	"github.com/randy-girard/flynn/pkg/blobstoreauth"
	"github.com/randy-girard/flynn/pkg/buildlock"
	"github.com/randy-girard/flynn/pkg/ctxhelper"
	"github.com/randy-girard/flynn/pkg/httphelper"
	"github.com/randy-girard/flynn/pkg/status"
	"github.com/randy-girard/flynn/pkg/uploadlimit"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == submoduleCheckFlag {
		if err := runCheckSubmoduleURL(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == submoduleListFlag {
		if err := runListSubmodules(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
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

	h := newGitHandler(cc, auth)
	h.locks = cc
	h.signer = receiveSigner()
	if err := h.configure(); err != nil {
		log.Fatalln(err)
	}
	log.Printf("push limits: MAX_PUSH_SIZE=%d (0 is unlimited) MAX_PUSH_DRAIN=%d PUSH_IDLE_TIMEOUT=%s (0s is off)", h.maxPushSize, h.maxPushDrain, h.idleTimeout)
	srv := &http.Server{Addr: ":" + os.Getenv("PORT"), Handler: wrapHandler(h)}
	uploadlimit.ApplyServerTimeouts(srv)
	log.Fatal(srv.ListenAndServe())
}

// configure reads the push settings from the environment.
func (h *gitHandler) configure() error {
	var err error
	if h.maxPushSize, err = uploadlimit.FromEnv("MAX_PUSH_SIZE", defaultMaxPushSize); err != nil {
		return fmt.Errorf("error parsing MAX_PUSH_SIZE: %s", err)
	}
	if h.idleTimeout, err = uploadlimit.DurationFromEnv("PUSH_IDLE_TIMEOUT", defaultPushIdleTimeout); err != nil {
		return err
	}
	if h.maxPushDrain, err = uploadlimit.FromEnv("MAX_PUSH_DRAIN", defaultMaxPushDrain); err != nil {
		return fmt.Errorf("error parsing MAX_PUSH_DRAIN: %s", err)
	}
	return nil
}

// wrapHandler is the middleware every gitreceive request goes through.
func wrapHandler(h http.Handler) http.Handler {
	return httphelper.ContextInjector("gitreceive", httphelper.NewRequestLogger(h))
}

// receiveSigner loads the key that mints per-app receive tokens. A missing
// or unreadable key leaves the receiver on CONTROLLER_KEY so an old release
// still accepts pushes during a rolling update.
func receiveSigner() *tokensigner.Signer {
	raw := strings.TrimSpace(os.Getenv("ACCESS_TOKEN_SIGNING_KEY"))
	if raw == "" {
		return nil
	}
	signer, err := tokensigner.ParseSigningKey(raw)
	if err != nil {
		log.Printf("gitreceive: ACCESS_TOKEN_SIGNING_KEY: %v", err)
		return nil
	}
	return signer
}

// appLookup is the GetApp subset gitreceive needs. The cluster-key controller
// client is used for the lookup; grants are checked against the returned app.
type appLookup interface {
	GetApp(id string) (*ct.App, error)
}

type gitHandler struct {
	controller appLookup
	callers    callerLookup
	auth       *authorizer.Authorizer
	signer     *tokensigner.Signer
	preview    previewLookup
	webhookURL string
	httpClient *http.Client
	prepare    func(cacheKey string) (string, error)
	upload     func(path, cacheKey string) error
	// uploadRetryDelay is the first wait before storeRepo retries an
	// upload (EDGE-10); 0 means one second.
	uploadRetryDelay time.Duration
	// maxPushSize caps the request body of a git RPC; 0 is no limit.
	maxPushSize int64
	// maxPushDrain is how much of a refused chunked pack is still read
	// so the refusal reaches git (EDGE-37, MAX_PUSH_DRAIN).
	maxPushDrain int64
	// idleTimeout cuts off a git RPC body that sends nothing for this long
	// (EDGE-36); 0 is off.
	idleTimeout time.Duration
	// locks holds the app's build lease for a push (EDGE-09); nil pushes
	// unlocked. lockTTL is the lease length, renewed while the push runs.
	locks   buildlock.Store
	lockTTL time.Duration
}

// defaultMaxPushSize caps one push (the pack git sends) unless MAX_PUSH_SIZE
// says otherwise (EDGE-08). GitHub refuses pushes over 2 GiB; 8 GiB leaves
// room for repositories with large committed assets.
const defaultMaxPushSize = 8 << 30

// defaultPushIdleTimeout is how long a push may send nothing before it is
// cut off (EDGE-36), unless PUSH_IDLE_TIMEOUT says otherwise.
const defaultPushIdleTimeout = uploadlimit.DefaultIdleTimeout

type gitService struct {
	method     string
	suffix     string
	handleFunc func(gitEnv, string, string, http.ResponseWriter, *http.Request) bool
	rpc        string
}

type gitEnv struct {
	App          string
	AppName      string
	Pipeline     string
	ReceiveToken string
	// MaxBody caps the RPC request body; 0 is no limit.
	MaxBody int64
	// MaxDrain is how much of a chunked pack past MaxBody is read and
	// discarded so the refusal reaches git (EDGE-37).
	MaxDrain int64
	// IdleTimeout cuts off an RPC request body that sends nothing for this
	// long (EDGE-36); 0 is off.
	IdleTimeout time.Duration
	// LockHolder is the build lease this push holds (EDGE-09), passed to
	// flynn-receiver so it does not try to take it again.
	LockHolder string
	// Refusal, when set, is printed by the pre-receive hook, which then
	// declines the push before anything is built.
	Refusal string
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
		callers:    newControllerCallers(url),
		auth:       auth,
		preview:    newPipelinePreviewClient(),
		webhookURL: strings.TrimRight(url, "/") + "/github/webhook",
		httpClient: http.DefaultClient,
		prepare:    prepareRepo,
		upload:     uploadRepo,

		maxPushSize:  defaultMaxPushSize,
		maxPushDrain: defaultMaxPushDrain,
		idleTimeout:  defaultPushIdleTimeout,
		lockTTL:      buildlock.DefaultTTL,
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

	tok, err := h.authorizeCaller(r)
	if errors.Is(err, errCallerForbidden) {
		http.Error(w, "Forbidden", 403)
		return
	} else if err != nil {
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

	env := gitEnv{App: app.ID, AppName: app.Name, MaxBody: h.maxPushSize, MaxDrain: h.maxPushDrain, IdleTimeout: h.idleTimeout}
	// EDGE-09: the receive RPC holds the app's build lease from before the
	// repo cache download until after the upload. A second push while it is
	// held still runs receive-pack (git only shows hook output), but the
	// hook declines it and nothing is built or uploaded.
	if g.rpc == "git-receive-pack" {
		lease, refusal := h.lockPush(w, app)
		defer lease.Release()
		env.LockHolder = lease.Holder()
		env.Refusal = refusal
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

	if rpc == "git-receive-pack" {
		env.ReceiveToken = h.receiveToken(app)
	}
	success := g.handleFunc(env, g.rpc, repoPath, w, r)
	if success && g.rpc == "git-receive-pack" && env.Refusal == "" {
		if err := h.storeRepo(repoPath, app.ID); err != nil {
			logError(w, "uploadRepo", err, "app", app.Name)
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

	success := g.handleFunc(gitEnv{Pipeline: pipeline, MaxBody: h.maxPushSize, MaxDrain: h.maxPushDrain, IdleTimeout: h.idleTimeout}, g.rpc, repoPath, w, r)
	if success && g.rpc == "git-receive-pack" {
		if err := h.storeRepo(repoPath, cacheKey); err != nil {
			logError(w, "uploadRepo", err, "pipeline", pipeline)
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

// lockPush takes app's build lease for one receive RPC. It returns the lease
// (nil when the controller has no lease API, see buildlock.ErrUnsupported)
// or the message the hook prints when the push must be refused.
func (h *gitHandler) lockPush(w http.ResponseWriter, app *ct.App) (*buildlock.Lease, string) {
	if h.locks == nil {
		return nil, ""
	}
	lease, err := buildlock.Acquire(h.locks, app.ID, "a git push", h.lockTTL)
	if err == nil {
		return lease, ""
	}
	var held *ct.BuildLockHeldError
	switch {
	case errors.As(err, &held):
		return nil, fmt.Sprintf("-----> Push refused: %s. Wait for it to finish, then pull and push again.", held)
	case errors.Is(err, buildlock.ErrUnsupported):
		logError(w, "buildLock", err)
		return nil, ""
	default:
		logError(w, "buildLock", err)
		return nil, fmt.Sprintf("-----> Push refused: could not take the build lock for %s: %s. Try the push again.", app.Name, err)
	}
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

// handlePostRPC runs one RPC. It returns true when git ran to its end and,
// for a push, the request could have changed the repository.
func handlePostRPC(env gitEnv, rpc string, path string, w http.ResponseWriter, r *http.Request) bool {
	receive := rpc == "git-receive-pack"
	// EDGE-08: refuse a body over the limit before git sees any of it.
	declaredTooLarge := env.MaxBody > 0 && r.ContentLength > env.MaxBody
	if declaredTooLarge && !receive {
		refuse(w, env, nil, tooLargeRefusal(env.MaxBody), true, 0)
		return false
	}

	// EDGE-36: a body that sends nothing for PUSH_IDLE_TIMEOUT is cut off
	idle := uploadlimit.NewIdleBody(w, r.Body, env.IdleTimeout)
	defer idle.Done()

	// The client request body may have been gzipped. The limit counts the
	// bytes git reads, so a small gzip body cannot expand past it.
	var body io.Reader = idle
	if r.Header.Get("Content-Encoding") == "gzip" {
		var err error
		body, err = gzip.NewReader(idle)
		if err != nil {
			if idle.TimedOut() {
				refuse(w, env, nil, idleRefusal(env.IdleTimeout), true, 0)
				return false
			}
			fail500(w, "handlePostRPC", err)
			return false
		}
	}
	limited := uploadlimit.NewReader(body, env.MaxBody)
	body = limited

	// EDGE-37: read the ref updates of a push first, so a refusal can be
	// answered as a receive-pack result git prints (see refusal.go)
	var cmds *pushCommands
	if receive {
		cmds, body = readPushCommands(limited)
		if idle.TimedOut() {
			refuse(w, env, cmds, idleRefusal(env.IdleTimeout), true, limited.N())
			return false
		}
		if cmds.notGit {
			w.Header().Set("Connection", "close")
			http.Error(w, "not a git receive-pack request", http.StatusBadRequest)
			return false
		}
		if declaredTooLarge {
			// the whole request is in curl's buffer already, so git reads
			// this answer without the pack being read
			refuse(w, env, cmds, tooLargeRefusal(env.MaxBody), true, limited.N())
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
	// stop kills and reaps git on a path that does not Wait for it. It runs
	// once: two concurrent Waits on one Cmd race.
	var stopOnce sync.Once
	stop := func() { stopOnce.Do(func() { cleanUpProcessGroup(cmd) }) }
	// a client that goes away kills git (brute force subprocess clean-up);
	// whichever path the handler takes then reaps it
	handlerDone := make(chan struct{})
	defer close(handlerDone)
	go func(closed <-chan bool) {
		select {
		case <-closed:
			killProcessGroup(cmd)
		case <-handlerDone:
		}
	}(w.(http.CloseNotifier).CloseNotify())

	// Write the client request body to Git's standard input
	if _, err := io.Copy(stdin, body); err != nil {
		if idle.TimedOut() {
			// stop git before the pre-receive hook can run; the deferred
			// lease release and temp repo removal follow
			stop()
			refuse(w, env, cmds, idleRefusal(env.IdleTimeout), true, limited.N())
			return false
		}
		if limited.Exceeded {
			// stop git before it finishes a truncated pack or runs the
			// pre-receive hook
			stop()
			tooLarge := tooLargeRefusal(env.MaxBody)
			if !cmds.canReport() {
				refuse(w, env, cmds, tooLarge, true, limited.N())
				return false
			}
			// git reads the answer only once it has sent the whole pack:
			// discard the rest, up to MAX_PUSH_DRAIN, then answer
			drained, ended := drain(limited.R, env.MaxDrain)
			if !ended {
				if idle.TimedOut() {
					refuse(w, env, cmds, tooLarge, true, limited.N()+drained)
				} else {
					refuse(w, env, nil, tooLarge, true, limited.N()+drained)
				}
				return false
			}
			refuse(w, env, cmds, tooLarge, false, limited.N()+drained)
			return false
		}
		stop()
		fail500(w, "handlePostRPC write to subprocess", err)
		return false
	}

	// Start writing the response
	w.Header().Add("Content-Type", fmt.Sprintf("application/x-%s-result", rpc))
	w.Header().Add("Cache-Control", "no-cache")
	w.WriteHeader(200) // Don't bother with HTTP 500 from this point on, just return
	if _, err := io.Copy(newWriteFlusher(w), pipe); err != nil {
		stop()
		logError(w, "handlePostRPC read from subprocess", err)
		return false
	}
	err = cmd.Wait()
	stopOnce.Do(func() {}) // git has exited; nothing left to stop
	if err != nil {
		logError(w, "handlePostRPC wait for subprocess", err)
		return false
	}

	// the probe git sends before a chunked pack changed nothing, so the
	// repo cache is not stored again for it
	return !cmds.isProbe()
}

func fail500(w http.ResponseWriter, context string, err error) {
	http.Error(w, "Internal server error", 500)
	logError(w, context, err)
}

func logError(w http.ResponseWriter, msg string, err error, ctx ...interface{}) {
	logger, _ := ctxhelper.LoggerFromContext(w.(*httphelper.ResponseWriter).Context())
	logger.Error(msg, append([]interface{}{"error", err}, ctx...)...)
}

// Git subprocess helpers
func subCommand(rpc string) string {
	return strings.TrimPrefix(rpc, "git-")
}

func (h *gitHandler) receiveToken(app *ct.App) string {
	if h == nil || h.signer == nil || app == nil {
		return ""
	}
	tok, err := buildtoken.MintReceive(h.signer, app, 30*time.Minute)
	if err != nil {
		log.Printf("gitreceive: receive token: %v", err)
		return ""
	}
	return tok
}

func gitCommand(env gitEnv, name string, args ...string) (*exec.Cmd, io.Reader) {
	cmd := exec.Command(name, args...)
	// Start the command in its own process group (nice for signalling)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Explicitly set the environment for the Git command
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("RECEIVE_APP=%s", env.App),
		fmt.Sprintf("RECEIVE_APP_NAME=%s", env.AppName),
		fmt.Sprintf("RECEIVE_PIPELINE=%s", env.Pipeline),
	)
	if tok := strings.TrimSpace(env.ReceiveToken); tok != "" {
		cmd.Env = append(cmd.Env, "FLYNN_RECEIVE_TOKEN="+tok)
	}
	if env.LockHolder != "" {
		cmd.Env = append(cmd.Env, buildlock.HolderEnv+"="+env.LockHolder)
	}
	if env.Refusal != "" {
		cmd.Env = append(cmd.Env, "FLYNN_PUSH_REFUSED="+env.Refusal)
	}

	r, _ := cmd.StdoutPipe()
	cmd.Stderr = cmd.Stdout

	return cmd, r
}

func cleanUpProcessGroup(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	killProcessGroup(cmd)

	// reap our child process
	go cmd.Wait()
}

// killProcessGroup sends SIGTERM to the process group of cmd.
func killProcessGroup(cmd *exec.Cmd) {
	if process := cmd.Process; process != nil && process.Pid > 0 {
		syscall.Kill(-process.Pid, syscall.SIGTERM)
	}
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
//
// Arguments after rev name the app (ID and/or name) for the submodule SSH
// opt-in. Submodule URLs are tenant input (EDGE-02): each one goes through
// `gitreceived -check-submodule-url` first, and only the commit the gitlink
// records is fetched, with depth 1, a time limit and a file size limit.
// `gitreceived -list-submodules` maps each gitlink to the .gitmodules section
// whose path it is (the section name may differ, EDGE-27) and validates the
// paths; every URL is checked before the first fetch.
const archiveTreeFunc = `git-archive-all() {
	rev="$1"
	shift
	work="$(mktemp -d)"
	cleanup() { rm -rf "$work"; }
	trap cleanup RETURN
	# Nested clone/checkout must not see receive-pack's GIT_DIR or quarantine.
	git_outside() {
		env -u GIT_DIR -u GIT_WORK_TREE -u GIT_QUARANTINE_PATH \
			-u GIT_OBJECT_DIRECTORY -u GIT_ALTERNATE_OBJECT_DIRECTORIES \
			-u GIT_INDEX_FILE -u GIT_PREFIX \
			-u GIT_PROXY_COMMAND -u GIT_CONFIG_PARAMETERS -u GIT_CONFIG_COUNT \
			"$@"
	}
	# submodule_plan URL APP... checks URL and, for ssh, that the client,
	# key and host key are there, and prints the checker's three lines.
	# Nothing is contacted.
	submodule_plan() {
		local url="$1"
		shift
		local plan proto fetch_url pin
		plan="$("${SUBMODULE_URL_CHECK:-/bin/gitreceived}" -check-submodule-url "$url" "$@")" || return 1
		{ read -r proto; read -r fetch_url; read -r pin || true; } <<< "$plan"
		[ -n "$proto" ] && [ -n "$fetch_url" ] || return 1
		local known="$HOME/.ssh/known_hosts"
		case "$proto" in
		https) [ -n "$pin" ] || return 1 ;;
		ssh)
			# EDGE-26: fail closed, naming what is missing, before ssh runs.
			# Host keys are only ever checked strictly against
			# SSH_CLIENT_HOSTS (no accept-new: every job start would trust
			# whatever key answered first).
			[ -n "$pin" ] || return 1
			local who="${2:-$1}"
			if ! command -v ssh >/dev/null 2>&1 || ! command -v ssh-keygen >/dev/null 2>&1; then
				echo "ssh submodule URL for app ${who} requires an OpenSSH client (ssh, ssh-keygen), which is not available in this gitreceive image" >&2
				return 1
			fi
			if [ -z "${SSH_CLIENT_KEY:-}" ]; then
				echo "ssh submodule URL for app ${who} requires the cluster SSH client key (gitreceive SSH_CLIENT_KEY), which is not set" >&2
				return 1
			fi
			if ! ssh-keygen -F "$pin" -f "$known" >/dev/null 2>&1; then
				echo "ssh submodule URL for app ${who} requires a host key for ${pin} in gitreceive SSH_CLIENT_HOSTS, which is not available" >&2
				return 1
			fi
			;;
		esac
		printf '%s\n%s\n%s\n' "$proto" "$fetch_url" "$pin"
	}
	# submodule_fetch PLAN SHA DEST fetches SHA as submodule_plan decided.
	submodule_fetch() {
		local plan="$1" sha="$2" dest="$3"
		local proto fetch_url pin
		{ read -r proto; read -r fetch_url; read -r pin || true; } <<< "$plan"
		[ -n "$proto" ] && [ -n "$fetch_url" ] || return 1
		local -a cfg=(-c http.followRedirects=false -c credential.helper= -c core.askPass=)
		local known="$HOME/.ssh/known_hosts"
		[ "$proto" != https ] || cfg+=(-c "http.curloptResolve=$pin")
		local -a limit=(timeout -k 10 "${SUBMODULE_FETCH_TIMEOUT:-300}")
		(
			ulimit -f "${SUBMODULE_MAX_FILE_KB:-1048576}"
			unset GIT_SSH GIT_SSH_COMMAND GIT_SSH_VARIANT
			export GIT_ALLOW_PROTOCOL="$proto" GIT_TERMINAL_PROMPT=0
			if [ "$proto" = ssh ]; then
				# The key exists on disk only for this fetch: mode 0600 in a
				# private directory on tmpfs (/dev/shm in the gitreceive job),
				# removed when this subshell exits.
				keydir="$(mktemp -d "${SUBMODULE_KEY_DIR:-/dev/shm}/flynn-submodule-key.XXXXXX" 2>/dev/null || mktemp -d)" || exit 1
				trap 'rm -rf "$keydir"' EXIT
				trap 'exit 1' HUP INT TERM
				(umask 077 && printf '%s\n' "$SSH_CLIENT_KEY" > "$keydir/key") || exit 1
				chmod 600 "$keydir/key" || exit 1
				unset SSH_CLIENT_KEY
				export GIT_SSH_COMMAND="ssh -F /dev/null -i $keydir/key -o IdentitiesOnly=yes -o BatchMode=yes -o StrictHostKeyChecking=yes -o UpdateHostKeys=no -o UserKnownHostsFile=$known -o GlobalKnownHostsFile=/dev/null -o HostKeyAlias=$pin"
			fi
			rm -rf "$dest"
			mkdir -p "$(dirname "$dest")"
			git_outside git init -q "$dest" || exit 1
			if ! git_outside "${limit[@]}" git -C "$dest" "${cfg[@]}" fetch -q --no-tags --depth 1 -- "$fetch_url" "$sha"; then
				# Servers that refuse a fetch by commit ID: fetch branches and
				# tags (still limited) and check out the recorded commit.
				git_outside "${limit[@]}" git -C "$dest" "${cfg[@]}" fetch -q -- "$fetch_url" '+refs/heads/*:refs/remotes/origin/*' '+refs/tags/*:refs/tags/*' || exit 1
			fi
			git_outside git -C "$dest" -c advice.detachedHead=false checkout --force --quiet "$sha" || exit 1
		)
	}
	git archive --format=tar "$rev" | tar -x -C "$work"
	local unpacked="${PIPESTATUS[0]}${PIPESTATUS[1]}"
	if [ "$unpacked" != 00 ]; then
		echo "cannot unpack ${rev}" >&2
		return 1
	fi
	# One parse of .gitmodules, keyed by the gitlinks' paths (EDGE-27).
	local mods sha path url plan i
	mods="$(mktemp)"
	if ! "${GITRECEIVED_BIN:-/bin/gitreceived}" -list-submodules "$rev" > "$mods"; then
		rm -f "$mods"
		return 1
	fi
	local -a m_sha=() m_path=() m_url=() m_plan=()
	while IFS= read -r -d '' sha && IFS= read -r -d '' path && IFS= read -r -d '' url; do
		m_sha+=("$sha")
		m_path+=("$path")
		m_url+=("$url")
	done < "$mods"
	rm -f "$mods"
	# Every URL is checked before any submodule is fetched.
	for i in "${!m_sha[@]}"; do
		if ! plan="$(submodule_plan "${m_url[$i]}" "$@" < /dev/null)"; then
			echo "submodule ${m_path[$i]}: cannot fetch ${m_url[$i]} (only https to public hosts, or ssh for apps in SSH_CLIENT_KEY_APPS)" >&2
			return 1
		fi
		m_plan+=("$plan")
	done
	for i in "${!m_sha[@]}"; do
		# receive-pack exports GIT_DIR and GIT_QUARANTINE_PATH. Git 2.45+
		# refuses ref updates in that environment, so the fetch runs
		# outside it (git_outside); otherwise slugbuilder gets an empty stream.
		if ! submodule_fetch "${m_plan[$i]}" "${m_sha[$i]}" "$work/${m_path[$i]}" < /dev/null; then
			echo "submodule ${m_path[$i]}: cannot fetch ${m_url[$i]}" >&2
			return 1
		fi
	done
	tar --create --exclude-vcs -C "$work" .
}
`

// The hook declines a push gitreceive refused (FLYNN_PUSH_REFUSED: another
// push holds the app's build lease) and a push whose old rev is no longer
// the branch tip. Smart HTTP advertises refs and receives the pack in two
// requests, each on a fresh repo cache download, so a push that landed in
// between leaves this one stale; receive-pack would only reject its ref
// update after this hook had built and released it (EDGE-09).
var prereceiveHook = []byte(`#!/bin/bash
set -eo pipefail;

if [[ -n "${FLYNN_PUSH_REFUSED}" ]]; then
	printf '%s\n' "${FLYNN_PUSH_REFUSED}"
	exit 1
fi

` + archiveTreeFunc + `

while read oldrev newrev refname; do
	if [[ $refname = "refs/heads/master" ]] || [[ $refname = "refs/heads/main" ]]; then
		current="$(git rev-parse --verify -q "$refname" || true)"
		if { [[ -z "$current" ]] && [[ ! $oldrev =~ ^0+$ ]]; } || { [[ -n "$current" ]] && [[ "$current" != "$oldrev" ]]; }; then
			echo "-----> Push refused: ${refname#refs/heads/} changed since this push started (another push landed first). Pull, then push again."
			exit 1
		fi
		git-archive-all $newrev "$RECEIVE_APP" "$RECEIVE_APP_NAME" | stdbuf -oL -eL "${FLYNN_RECEIVER:-/bin/flynn-receiver}" "$RECEIVE_APP" "$newrev" --meta git=true --meta "git.commit=$newrev"| sed -u "s/^/"$'\e[1G\e[K'"/"
		deploy_pushed=1
		break
	fi
done

if [[ -z "${deploy_pushed}" ]]; then
  echo "The push must include a change to the master or main branch to be deployed."
  exit 1
fi
`)

// repoCacheBase and repoCacheClient are where the repo cache lives and the
// client that reads and writes it. Tests point them at a fake blobstore.
var (
	repoCacheBase   = "http://blobstore.discoverd"
	repoCacheClient = newRepoCacheClient(repoCacheTimeout, repoCacheHeaderTimeout)
)

// A repo cache transfer may move a repo up to the push limit inside the
// cluster, so the whole request gets a generous bound. The blobstore must
// answer within repoCacheHeaderTimeout once the request is sent.
const (
	repoCacheTimeout       = 30 * time.Minute
	repoCacheHeaderTimeout = time.Minute
)

func newRepoCacheClient(timeout, headerTimeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			ResponseHeaderTimeout: headerTimeout,
			IdleConnTimeout:       90 * time.Second,
			MaxIdleConnsPerHost:   2,
		},
	}
}

func blobstoreCacheURL(cacheKey string) string {
	return fmt.Sprintf("%s/repos/%s.tar", repoCacheBase, cacheKey)
}

// prepareRepo downloads the app's cached repo into a new temporary
// directory, or starts an empty one when there is no cache yet. On error the
// directory is removed; the caller removes it after a success.
func prepareRepo(cacheKey string) (string, error) {
	path, err := ioutil.TempDir("", "repo-"+cacheKey)
	if err != nil {
		return "", err
	}
	if err := fetchRepo(path, cacheKey); err != nil {
		os.RemoveAll(path)
		return "", err
	}
	return path, nil
}

func fetchRepo(path, cacheKey string) error {
	req, err := http.NewRequest("GET", blobstoreCacheURL(cacheKey), nil)
	if err != nil {
		return err
	}
	blobstoreauth.Apply(req)
	res, err := repoCacheClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	if res.StatusCode == 404 {
		return initRepo(path)
	}
	if res.StatusCode != 200 {
		return fmt.Errorf("unexpected status %d retrieving cached repo", res.StatusCode)
	}

	if err := archiver.Untar(path, tar.NewReader(res.Body)); err != nil {
		return fmt.Errorf("extracting cached repo: %w", err)
	}
	if err := setGitConfig(path); err != nil {
		return err
	}
	if err := writeRepoHook(path); err != nil {
		return err
	}
	return repairDanglingHEAD(path)
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
	// autoDetach defaults to true, which runs git gc --auto in a forked process, corrupting uploads.
	// The receive repo is non-bare and HEAD names the default branch, but nothing
	// reads that work tree (the hook archives the pushed SHA). Git 2.50 refuses
	// the push, including the first one, unless denyCurrentBranch is ignore.
	// updateInstead would checkout into this repo and bring back the
	// quarantined-HEAD failure.
	for _, args := range [][]string{
		{"config", "--bool", "gc.autoDetach", "false"},
		{"config", "receive.denyCurrentBranch", "ignore"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = path
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git %s: %s", strings.Join(args, " "), out)
		}
	}
	return nil
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
		if cerr := tw.Close(); err == nil {
			err = cerr
		}
		// A failed archive must fail the request, not end it as a short
		// but valid tar that the blobstore would store.
		w.CloseWithError(err)
		errCh <- err
	}()

	// upload the tarball to the blobstore
	req, err := http.NewRequest("PUT", blobstoreCacheURL(cacheKey), r)
	if err != nil {
		r.CloseWithError(err)
		<-errCh
		return err
	}
	blobstoreauth.Apply(req)
	resp, err := repoCacheClient.Do(req)
	// Do closes the body on error, so the archive goroutine always ends.
	tarErr := <-errCh
	if resp != nil {
		defer resp.Body.Close()
	}
	if tarErr != nil {
		return fmt.Errorf("archiving repo: %w", tarErr)
	}
	if err != nil {
		return fmt.Errorf("uploading cached repo: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return &repoUploadError{Status: resp.StatusCode, Body: strings.TrimSpace(string(msg))}
	}
	return nil
}

// repoUploadError is a blobstore response other than 2xx to a repo upload.
type repoUploadError struct {
	Status int
	Body   string
}

func (e *repoUploadError) Error() string {
	msg := fmt.Sprintf("blobstore refused the cached repo: %d %s", e.Status, http.StatusText(e.Status))
	if e.Body != "" {
		msg += ": " + e.Body
	}
	return msg
}

// repoUploadAttempts bounds storeRepo's retries of one repo upload.
const repoUploadAttempts = 3

// storeRepo uploads the repo after a push, retrying a transport error or a
// 5xx. A 4xx (for example a 401 from a bad key) will not change on retry.
// The git response is already sent by then, so a final failure is only
// logged: the build is deployed, and the next push starts from the older
// cache and sends the missing objects again.
func (h *gitHandler) storeRepo(path, cacheKey string) error {
	upload := h.upload
	if upload == nil {
		upload = uploadRepo
	}
	delay := h.uploadRetryDelay
	if delay == 0 {
		delay = time.Second
	}
	var err error
	for attempt := 1; attempt <= repoUploadAttempts; attempt++ {
		if err = upload(path, cacheKey); err == nil {
			return nil
		}
		var refused *repoUploadError
		if errors.As(err, &refused) && refused.Status < 500 {
			return err
		}
		if attempt < repoUploadAttempts {
			time.Sleep(delay * time.Duration(attempt))
		}
	}
	return fmt.Errorf("after %d attempts: %w", repoUploadAttempts, err)
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
