package main

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"time"

	c "github.com/flynn/go-check"
	"github.com/kr/pty"
	controller "github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/attempt"
	"github.com/randy-girard/flynn/pkg/term"
)

type GitDeploySuite struct {
	Helper
}

var _ = c.ConcurrentSuite(&GitDeploySuite{})

var Attempts = attempt.Strategy{
	Total: 60 * time.Second,
	Delay: 500 * time.Millisecond,
}

func (s *GitDeploySuite) TestEnvDir(t *c.C) {
	r := s.newGitRepo(t, "env-dir")
	t.Assert(r.flynn("create"), Succeeds)
	t.Assert(r.flynn("env", "set", "FOO=bar", "BUILDPACK_URL=https://github.com/kr/heroku-buildpack-inline"), Succeeds)

	push := r.git("push", "flynn", "master")
	t.Assert(push, SuccessfulOutputContains, "bar")
}

func (s *GitDeploySuite) TestEmptyRelease(t *c.C) {
	r := s.newGitRepo(t, "empty-release")
	t.Assert(r.flynn("create"), Succeeds)
	t.Assert(r.flynn("env", "set", "BUILDPACK_URL=https://github.com/kr/heroku-buildpack-inline"), Succeeds)

	push := r.git("push", "flynn", "master")
	t.Assert(push, Succeeds)

	run := r.flynn("run", "echo", "foo")
	t.Assert(run, Succeeds)
	t.Assert(run, Outputs, "foo\n")
}

func (s *GitDeploySuite) TestBuildCaching(t *c.C) {
	s.testBuildCaching(t, nil)
}

func (s *GitDeploySuite) TestAppRecreation(t *c.C) {
	r := s.newGitRepo(t, "empty")
	t.Assert(r.flynn("create", "-y", "app-recreation"), Succeeds)
	r.git("commit", "-m", "bump", "--allow-empty")
	t.Assert(r.git("push", "flynn", "master"), Succeeds)
	t.Assert(r.flynn("delete", "-y"), Succeeds)

	// recreate app and push again, it should work
	t.Assert(r.flynn("create", "-y", "app-recreation"), Succeeds)
	t.Assert(r.git("push", "flynn", "master"), Succeeds)
	t.Assert(r.flynn("delete", "-y"), Succeeds)
}

func (s *GitDeploySuite) TestGoBuildpack(t *c.C) {
	s.runBuildpackTest(t, "go-flynn-example", []string{"postgres"})
}

func (s *GitDeploySuite) TestNodejsBuildpack(t *c.C) {
	s.runBuildpackTest(t, "nodejs-flynn-example", nil)
}

func (s *GitDeploySuite) TestPhpBuildpack(t *c.C) {
	s.runBuildpackTest(t, "php-flynn-example", nil)
}

func (s *GitDeploySuite) TestRubyBuildpack(t *c.C) {
	s.runBuildpackTest(t, "ruby-flynn-example", nil)
}

func (s *GitDeploySuite) TestJavaBuildpack(t *c.C) {
	s.runBuildpackTest(t, "java-flynn-example", nil)
}

func (s *GitDeploySuite) TestClojureBuildpack(t *c.C) {
	s.runBuildpackTest(t, "clojure-flynn-example", nil)
}

func (s *GitDeploySuite) TestPythonBuildpack(t *c.C) {
	s.runBuildpackTest(t, "python-flynn-example", nil)
}

func (s *GitDeploySuite) TestStaticBuildpack(t *c.C) {
	s.runBuildpackTestWithResponsePattern(t, "static-flynn-example", nil, `Hello, Flynn!`)
}

func (s *GitDeploySuite) TestPushTwice(t *c.C) {
	r := s.newGitRepo(t, "https://github.com/flynn-examples/nodejs-flynn-example")
	t.Assert(r.flynn("create"), Succeeds)
	t.Assert(r.git("push", "flynn", "master"), Succeeds)
	t.Assert(r.git("commit", "-m", "second", "--allow-empty"), Succeeds)
	t.Assert(r.git("push", "flynn", "master"), Succeeds)
}

func (s *GitDeploySuite) runBuildpackTest(t *c.C, name string, resources []string) {
	s.runBuildpackTestWithResponsePattern(t, name, resources, `Hello from Flynn on port \d+`)
}

func legacyExampleFixture(name string) (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("legacy example fixture path unavailable")
	}
	dir := filepath.Join(filepath.Dir(file), "fixtures", "legacy-examples", name)
	info, err := os.Stat(dir)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("legacy example fixture %s is not a directory", name)
	}
	return dir, nil
}

func copyLegacyExample(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			return err
		}
		defer out.Close()
		_, err = io.Copy(out, in)
		return err
	})
}

func (s *GitDeploySuite) prepareLegacyExample(t *c.C, r *gitRepo, name string) {
	switch name {
	case "python-flynn-example":
		// The example pins Flask 0.12 and nothing else. The current Python
		// buildpack then installs Jinja2 3, and Flask 0.12 exits on
		// `from jinja2 import Markup`. Pin a Flask that imports on the
		// buildpack's Python 3.12.
		t.Assert(r.sh("printf '%s\n' 'Flask==3.0.3' > requirements.txt"), Succeeds)
		t.Assert(r.sh("printf '%s\n' '3.12' > .python-version"), Succeeds)
		t.Assert(r.git("add", "requirements.txt", ".python-version"), Succeeds)
		t.Assert(r.git("commit", "-m", "pin flask for the current python buildpack"), Succeeds)
	case "php-flynn-example":
		// composer.lock has no plugin-api-version, so the current PHP
		// buildpack rejects it before compile. Replace it with a Slim 4
		// app and a Composer 2 lock.
		s.commitLegacyExample(t, r, name, "refresh php example for the current php buildpack")
	case "ruby-flynn-example":
		// Gemfile.lock pins Bundler 1.12.5. That release calls =~ on the
		// boolean the buildpack writes for disable_shared_gems, and Ruby 3
		// has no TrueClass#=~. Ship Bundler 2.5, current Sinatra, and a
		// bind on every interface so the router can reach the process.
		s.commitLegacyExample(t, r, name, "refresh ruby example for the current ruby buildpack")
	}
}

func (s *GitDeploySuite) commitLegacyExample(t *c.C, r *gitRepo, name, message string) {
	src, err := legacyExampleFixture(name)
	t.Assert(err, c.IsNil)
	t.Assert(copyLegacyExample(src, r.dir), c.IsNil)
	t.Assert(r.git("add", "-A"), Succeeds)
	t.Assert(r.git("commit", "-m", message), Succeeds)
}

func (s *GitDeploySuite) runBuildpackTestWithResponsePattern(t *c.C, name string, resources []string, pat string) {
	r := s.newGitRepo(t, "https://github.com/flynn-examples/"+name)
	s.prepareLegacyExample(t, r, name)

	t.Assert(r.flynn("create", name), SuccessfulOutputContains, fmt.Sprintf("Created %s\n", name))

	for _, resource := range resources {
		s.skipUnlessProvider(t, resource)
		t.Assert(r.flynn("resource", "add", resource), Succeeds)
	}

	watcher, err := s.controllerClient(t).WatchJobEvents(name, "")
	t.Assert(err, c.IsNil)
	defer watcher.Close()

	push := r.git("push", "flynn", "master")
	t.Assert(push, SuccessfulOutputContains, "Creating release")
	t.Assert(push, SuccessfulOutputContains, "Scaling initial release to web=1")
	t.Assert(push, SuccessfulOutputContains, "* [new branch]      master -> master")
	t.Assert(push, SuccessfulOutputContains, "Initial web job started")
	t.Assert(push, SuccessfulOutputContains, "Application deployed")

	watcher.WaitFor(ct.JobEvents{"web": {ct.JobStateUp: 1}}, scaleTimeout, nil)

	route := name + ".dev"
	newRoute := r.flynn("route:add", "http", route)
	t.Assert(newRoute, Succeeds)

	err = Attempts.Run(func() error {
		// Make HTTP requests
		client := &http.Client{}
		req, err := http.NewRequest("GET", "http://"+routerIP, nil)
		if err != nil {
			return err
		}
		req.Host = route
		res, err := client.Do(req)
		if err != nil {
			return err
		}
		defer res.Body.Close()
		contents, err := ioutil.ReadAll(res.Body)
		if err != nil {
			return err
		}
		if res.StatusCode != 200 {
			return fmt.Errorf("Expected status 200, got %v", res.StatusCode)
		}
		m, err := regexp.MatchString(pat, string(contents))
		if err != nil {
			return err
		}
		if !m {
			return fmt.Errorf("Expected `%s`, got `%v`", pat, string(contents))
		}
		return nil
	})
	t.Assert(err, c.IsNil)

	t.Assert(r.flynn("scale", "web=0"), Succeeds)
}

func (s *GitDeploySuite) TestRunQuoting(t *c.C) {
	r := s.newGitRepo(t, "empty")
	t.Assert(r.flynn("create"), Succeeds)
	t.Assert(r.git("push", "flynn", "master"), Succeeds)

	run := r.flynn("run", "bash", "-c", "echo 'foo bar'")
	t.Assert(run, Succeeds)
	t.Assert(run, Outputs, "foo bar\n")
}

// TestConfigDir ensures we don't regress on a bug where uploaded repos were
// being checked out into the bare git repo, which would fail if the repo
// contained a config directory because the bare repo had a config file in it.
func (s *GitDeploySuite) TestConfigDir(t *c.C) {
	r := s.newGitRepo(t, "config-dir")
	t.Assert(r.flynn("create"), Succeeds)
	t.Assert(r.git("push", "flynn", "master"), Succeeds)
}

// TestLargeRepo ensures that there is no regression for https://github.com/randy-girard/flynn/issues/1799
func (s *GitDeploySuite) TestLargeRepo(t *c.C) {
	r := s.newGitRepo(t, "")

	// write 5MiB of random data to a file in 10KiB chunks
	dest, err := os.Create(filepath.Join(r.dir, "random"))
	t.Assert(err, c.IsNil)
	buf := make([]byte, 102400)
	for i := 0; i < 512; i++ {
		io.ReadFull(rand.Reader, buf)
		dest.Write(buf)
	}
	dest.Close()

	// push the repo
	t.Assert(r.git("add", "random"), Succeeds)
	t.Assert(r.git("commit", "-m", "data"), Succeeds)
	t.Assert(r.flynn("create"), Succeeds)
	t.Assert(r.git("push", "flynn", "master"), OutputContains, "Unable to select a buildpack")
}

func (s *GitDeploySuite) TestPrivateSSHKeyClone(t *c.C) {
	// GitHub no longer accepts the deploy key this suite installs.
	t.Skip("fixture GitHub deploy key is not authorized")
	r := s.newGitRepo(t, "private-clone")
	t.Assert(r.flynn("create"), Succeeds)
	t.Assert(r.flynn("env", "set", "BUILDPACK_URL=git@github.com:kr/heroku-buildpack-inline.git"), Succeeds)

	push := r.git("push", "flynn", "master")
	t.Assert(push, Succeeds)
}

func (s *GitDeploySuite) TestGitSubmodules(t *c.C) {
	r := s.newGitRepo(t, "empty")
	t.Assert(r.git("submodule", "add", "https://github.com/flynn-examples/go-flynn-example.git"), Succeeds)

	t.Assert(r.git("commit", "-am", "Add Submodule"), Succeeds)
	t.Assert(r.flynn("create"), Succeeds)
	t.Assert(r.git("push", "flynn", "master"), Succeeds)
	t.Assert(r.flynn("run", "ls", "go-flynn-example"), SuccessfulOutputContains, "main.go")

	// deploy again to test cached repo
	t.Assert(r.git("commit", "-m", "foo", "--allow-empty"), Succeeds)
	t.Assert(r.git("push", "flynn", "master"), Succeeds)
	t.Assert(r.flynn("run", "ls", "go-flynn-example"), SuccessfulOutputContains, "main.go")
}

func (s *GitDeploySuite) TestCancel(t *c.C) {
	r := s.newGitRepo(t, "cancel-hang")
	t.Assert(r.flynn("create", "cancel-hang"), Succeeds)
	t.Assert(r.flynn("env", "set", "FOO=bar", "BUILDPACK_URL=https://github.com/kr/heroku-buildpack-inline"), Succeeds)

	// The logged-in user token hides slugbuilder job events. The cluster
	// key is what gitreceive and flynn-host use, and it receives them.
	watcher, err := s.clusterKeyClient(t).WatchJobEvents("cancel-hang", "")
	t.Assert(err, c.IsNil)

	// start push
	cmd := exec.Command("git", "push", "flynn", "master")
	// put the command in its own process group (to emulate the way shells handle Ctrl-C)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Dir = r.dir
	var stdout io.Reader
	stdout, _ = cmd.StdoutPipe()
	cmd.Stderr = cmd.Stdout
	out := &bytes.Buffer{}
	stdout = io.TeeReader(stdout, out)
	err = cmd.Start()
	t.Assert(err, c.IsNil)

	done := make(chan struct{})
	go func() {
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			cmd.Process.Signal(syscall.SIGTERM)
			cmd.Wait()
			t.Fatal("git push timed out")
		}
	}()

	// wait for sentinel
	sc := bufio.NewScanner(stdout)
	found := false
	for sc.Scan() {
		if strings.Contains(sc.Text(), "hanging...") {
			found = true
			break
		}
	}
	t.Log(out.String())
	t.Assert(found, c.Equals, true)

	// send Ctrl-C to git process group
	syscall.Kill(-cmd.Process.Pid, syscall.SIGINT)
	t.Assert(err, c.IsNil)
	go io.Copy(ioutil.Discard, stdout)
	cmd.Wait()
	close(done)

	// check that slugbuilder exits immediately
	err = watcher.WaitFor(ct.JobEvents{"slugbuilder": {ct.JobStateUp: 1, ct.JobStateDown: 1}}, 10*time.Second, nil)
	t.Assert(err, c.IsNil)
}

func (s *GitDeploySuite) TestCrashingApp(t *c.C) {
	// create a crashing app
	r := s.newGitRepo(t, "crash")
	app := "crashing-app"
	t.Assert(r.flynn("create", app), Succeeds)
	t.Assert(r.flynn("env", "set", "BUILDPACK_URL=https://github.com/kr/heroku-buildpack-inline"), Succeeds)

	// check the push is rejected as the job won't start
	push := r.git("push", "flynn", "master")
	t.Assert(push, c.Not(Succeeds))
	t.Assert(push, OutputContains, "web job failed to start")

	// check the formation was deleted
	client := s.controllerClient(t)
	release, err := client.GetAppRelease(app)
	t.Assert(err, c.IsNil)
	_, err = client.GetFormation(app, release.ID)
	t.Assert(err, c.Equals, controller.ErrNotFound)
}

func (s *GitDeploySuite) TestSlugignore(t *c.C) {
	r := s.newGitRepo(t, "slugignore")
	t.Assert(r.flynn("create", "slugignore"), Succeeds)
	t.Assert(r.git("push", "flynn", "master"), Succeeds)

	run := r.flynn("run", "ls")
	t.Assert(run, Succeeds)
	t.Assert(run, Outputs, "existing\n")
}

func (s *GitDeploySuite) TestNonMasterPush(t *c.C) {
	r := s.newGitRepo(t, "empty")
	t.Assert(r.flynn("create"), Succeeds)
	push := r.git("push", "flynn", "master:foo")
	t.Assert(push, c.Not(Succeeds))
	t.Assert(push, OutputContains, "push must include a change to the master or main branch")
}

func (s *GitDeploySuite) TestProcfileChange(t *c.C) {
	for _, strategy := range []string{"all-at-once", "one-by-one"} {
		r := s.newGitRepo(t, "http")
		name := "procfile-change-" + strategy
		t.Assert(r.flynn("create", name), Succeeds)
		t.Assert(s.controllerClient(t).UpdateApp(&ct.App{ID: name, Strategy: strategy}), c.IsNil)
		t.Assert(r.git("push", "flynn", "master"), Succeeds)

		// add a new web process type
		t.Assert(r.sh("echo new-web: http >> Procfile"), Succeeds)
		t.Assert(r.git("commit", "-a", "-m", "add new-web process"), Succeeds)
		t.Assert(r.git("push", "flynn", "master"), Succeeds)
		t.Assert(r.flynn("scale", "new-web=1"), Succeeds)

		// remove the new web process type
		t.Assert(r.sh("sed -i '/new-web: http/d' Procfile"), Succeeds)
		t.Assert(r.git("commit", "-a", "-m", "remove new-web process"), Succeeds)
		t.Assert(r.git("push", "flynn", "master"), Succeeds)
	}
}

func (s *GitDeploySuite) TestCustomPort(t *c.C) {
	r := s.newGitRepo(t, "http")
	name := "custom-port"
	t.Assert(r.flynn("create", name), Succeeds)
	t.Assert(r.git("push", "flynn", "master"), Succeeds)

	// Update release with a different port
	cc := s.controllerClient(t)
	app, err := cc.GetApp(name)
	t.Assert(err, c.IsNil)
	release, err := cc.GetAppRelease(name)
	t.Assert(err, c.IsNil)
	release.ID = ""
	release.Processes["web"].Ports[0].Port = 9090
	t.Assert(cc.CreateRelease(app.ID, release), c.IsNil)
	t.Assert(cc.DeployAppRelease(app.ID, release.ID, nil), c.IsNil)

	// Deploy again, check that port stays the same
	t.Assert(r.git("commit", "-m", "foo", "--allow-empty"), Succeeds)
	t.Assert(r.git("push", "flynn", "master"), Succeeds)
	release, err = cc.GetAppRelease(app.ID)
	t.Assert(err, c.IsNil)
	t.Assert(release.Processes["web"].Ports[0].Port, c.Equals, 9090)
}

func (s *GitDeploySuite) TestDevStdout(t *c.C) {
	r := s.newGitRepo(t, "empty-release")
	t.Assert(r.flynn("create"), Succeeds)
	t.Assert(r.flynn("env", "set", "BUILDPACK_URL=https://github.com/kr/heroku-buildpack-inline"), Succeeds)
	t.Assert(r.git("push", "flynn", "master"), Succeeds)

	// check slug jobs can write to /dev/stdout and /dev/stderr
	for _, dev := range []string{"/dev/stdout", "/dev/stderr"} {
		// check without a TTY
		echoFoo := fmt.Sprintf("echo foo > %s", dev)
		t.Assert(r.flynn("run", "bash", "-c", echoFoo), SuccessfulOutputContains, "foo")

		// check with a TTY
		cmd := flynnCmd(r.dir, "run", "bash", "-c", echoFoo)
		master, slave, err := pty.Open()
		t.Assert(err, c.IsNil)
		defer master.Close()
		defer slave.Close()
		t.Assert(term.SetWinsize(slave.Fd(), &term.Winsize{Height: 24, Width: 80}), c.IsNil)
		cmd.Stdin = slave
		cmd.Stdout = slave
		cmd.Stderr = slave
		t.Assert(cmd.Run(), c.IsNil)
		out := make([]byte, 3)
		_, err = io.ReadFull(master, out)
		t.Assert(err, c.IsNil)
		t.Assert(string(out), c.Equals, "foo")
	}
}

func (s *GitDeploySuite) TestSourceVersion(t *c.C) {
	r := s.newGitRepoWithoutTrace(t, "source-version")
	t.Assert(r.flynn("create"), Succeeds)
	t.Assert(r.flynn("env", "set", "BUILDPACK_URL=https://github.com/kr/heroku-buildpack-inline"), Succeeds)

	res := r.git("rev-parse", "HEAD")
	t.Assert(res, Succeeds)
	commit := strings.TrimSpace(res.Output)
	t.Assert(commit, c.HasLen, 40)

	push := r.git("push", "flynn", "master")
	t.Assert(push, SuccessfulOutputContains, fmt.Sprintf("SOURCE_VERSION: %s", commit))
}
