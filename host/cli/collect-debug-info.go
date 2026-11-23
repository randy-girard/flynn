package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/flynn/go-docopt"
	"github.com/inconshreveable/log15"
	"github.com/randy-girard/flynn/discoverd/client"
	"github.com/randy-girard/flynn/host/config"
	"github.com/randy-girard/flynn/pkg/cluster"
	"github.com/randy-girard/flynn/pkg/plugin"
	"github.com/randy-girard/flynn/pkg/sirenia/state"
)

var flynnHostLogs = map[string]string{
	"tmp-flynn-host.log": "/tmp/flynn-host.log",
}

var debugCmds = [][]string{
	{"ps", "faux"},
	{"uname", "-a"},
	{"lsb_release", "-a"},
	{"date"},
	{"free", "-m"},
	{"df", "-h"},
	{os.Args[0], "version"},
	{"iptables", "-L", "-v", "-n", "--line-numbers"},
}

// networkDebugAlternatives prefers iproute2 (common on minimal cloud images) over net-tools.
var networkDebugAlternatives = []struct {
	desc string
	cmds [][]string
}{
	{"network interfaces", [][]string{{"ip", "addr"}, {"ifconfig"}}},
	{"routing table", [][]string{{"ip", "route"}, {"route", "-n"}}},
}

const githubTokenFile = ".flynn/github_token"

func init() {
	Register("collect-debug-info", runCollectDebugInfo, `
usage: flynn-host collect-debug-info [options]

Options:
  --tarball              Write a local tarball (the default; kept for compatibility)
  --upload               Upload to a secret GitHub gist instead of writing a tarball
  --include-env          Include job environment variables (secret values stay redacted)
  --log-dir=DIR          Path to the log directory [default: /var/log/flynn]
  --filename=PATH        Path to write the tarball (not valid with --upload)
  --github-token=TOKEN   GitHub personal access token for --upload (stored for future use)

Collect logs and host state for a bug report. By default this writes an
owner-only (0600) tarball on this host and sends nothing over the network.

Known cluster secrets (from /etc/flynn/host.json, this process's environment
and every job's environment) and secret-looking values (KEY=value pairs with
secret names, URL passwords, Authorization headers, password flags) are
replaced with [REDACTED] before anything is written or uploaded. Review the
output before sharing it: redaction cannot find every secret.

--upload sends the redacted files to a secret gist on GitHub. It lists the
files and the destination before sending. GitHub requires a personal access
token with the gist scope: pass --github-token on first use; the token is saved
to ~/.flynn/github_token for later runs.`)
}

// debugInfoOptions says where collect-debug-info output goes.
type debugInfoOptions struct {
	Upload   bool
	Filename string
	Token    string
	Out      io.Writer
}

func debugInfoOptionsFromArgs(args *docopt.Args) (debugInfoOptions, error) {
	opts := debugInfoOptions{
		Upload:   args.Bool["--upload"],
		Filename: args.String["--filename"],
		Out:      os.Stderr,
	}
	if opts.Upload && args.Bool["--tarball"] {
		return opts, errors.New("--tarball and --upload cannot be used together")
	}
	if opts.Upload && opts.Filename != "" {
		return opts, errors.New("--filename is only valid when writing a tarball (without --upload)")
	}
	return opts, nil
}

func githubTokenPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, githubTokenFile)
}

func saveGitHubToken(token string) error {
	path := githubTokenPath()
	if path == "" {
		return fmt.Errorf("unable to determine home directory")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	return ioutil.WriteFile(path, []byte(token), 0600)
}

func loadGitHubToken() string {
	path := githubTokenPath()
	if path == "" {
		return ""
	}
	data, err := ioutil.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func runCollectDebugInfo(args *docopt.Args) error {
	log := log15.New()
	opts, err := debugInfoOptionsFromArgs(args)
	if err != nil {
		return err
	}
	if opts.Upload {
		log.Info("collecting logs and debug information for upload to a secret gist")
	} else {
		log.Info("creating a local tarball containing logs and debug information (use --upload to send it to a gist)")
	}
	log.Info("this may take a while depending on the size of your logs")

	// Resolve GitHub token: flag takes precedence, then stored token. It is
	// only needed (and only loaded) for --upload, but a token passed on the
	// command line is always redacted from the ps output.
	token := args.String["--github-token"]
	if token != "" {
		if err := saveGitHubToken(token); err != nil {
			log.Error("failed to save GitHub token", "err", err)
		} else {
			log.Info(fmt.Sprintf("GitHub token saved to %s", githubTokenPath()))
		}
	} else if opts.Upload {
		token = loadGitHubToken()
		if token != "" {
			log.Info("using stored GitHub token")
		}
	}
	opts.Token = token

	redactor := newDebugRedactor()
	redactor.AddValue(token)
	redactor.AddEnviron(os.Environ())
	if err := redactor.AddHostConfig(hostConfigPath); err != nil {
		log.Error(fmt.Sprintf("error reading secrets from %s", hostConfigPath), "err", err)
	}

	gist := &Gist{
		Description: "Flynn debug information",
		Public:      false,
		Files:       make(map[string]File),
	}

	log.Info("getting flynn-host logs")
	logDir := args.String["--log-dir"]
	flynnHostLogs["flynn-host.log"] = filepath.Join(logDir, "flynn-host.log")
	for name, filepath := range flynnHostLogs {
		if err := gist.AddLocalFile(name, filepath); err != nil && !os.IsNotExist(err) {
			log.Error(fmt.Sprintf("error getting flynn-host log %q", filepath), "err", err)
		}
	}

	log.Info("getting sirenia metadata")
	if err := captureSireniaMetadata(gist); err != nil {
		log.Error("error getting sirenia metadata", "err", err)
	}

	log.Info("getting scheduler state")
	if err := captureSchedulerState(gist); err != nil {
		log.Error("error getting scheduler state", "err", err)
	}

	log.Info("getting job logs")
	if err := captureJobs(gist, args.Bool["--include-env"], redactor); err != nil {
		log.Error("error getting job logs, falling back to on-disk logs", "err", err)
		debugCmds = append(debugCmds, []string{"bash", "-c", fmt.Sprintf("tail -n+1 %s/*.log", logDir)})
	}

	log.Info("getting system information")
	var debugOutput string
	for _, cmd := range debugCmds {
		output, err := captureCmd(cmd[0], cmd[1:]...)
		if err != nil {
			log.Error(fmt.Sprintf("error capturing output of %q", strings.Join(cmd, " ")), "err", err)
			continue
		}
		debugOutput += fmt.Sprintln("===>", strings.Replace(strings.Join(cmd, " "), "\n", `\n`, -1), "\n", output)
	}
	for _, alt := range networkDebugAlternatives {
		debugOutput += captureDebugCmdFirstSuccess(log, alt.desc, alt.cmds)
	}
	gist.AddFile("0-debug-output.log", debugOutput)

	return finishDebugInfo(log, gist, redactor, opts)
}

// hostConfigPath is the host config whose env secrets are redacted.
var hostConfigPath = config.DefaultPath

// finishDebugInfo redacts the collected files, then writes them to a local
// tarball, or uploads them to a gist when opts.Upload is set (falling back to
// a tarball when they exceed the gist size limit).
func finishDebugInfo(log log15.Logger, gist *Gist, redactor *debugRedactor, opts debugInfoOptions) error {
	if opts.Out == nil {
		opts.Out = os.Stderr
	}
	changed := gist.Redact(redactor)
	log.Info(fmt.Sprintf("redacted secrets in %d of %d files", changed, len(gist.Files)))

	tarball := !opts.Upload
	if opts.Upload && gist.Size > GistMaxSize {
		log.Info(fmt.Sprintf("Total size of %d bytes exceeds gist limit, falling back to tarball.", gist.Size))
		tarball = true
	}

	if tarball {
		path := opts.Filename
		if path == "" {
			tmpDir, err := ioutil.TempDir("", "flynn-host-debug")
			if err != nil {
				return err
			}
			path = filepath.Join(tmpDir, "flynn-host-debug.tar.gz")
		}
		if err := gist.CreateTarball(path); err != nil {
			log.Error("error creating tarball", "err", err)
			return err
		}
		log.Info(fmt.Sprintf("created tarball containing debug information at %s (mode 0600); review it before sharing", path))
		return nil
	}

	if opts.Token == "" {
		return errors.New("GitHub requires a token to create a gist: pass --github-token=TOKEN (gist scope), or omit --upload to write a local tarball")
	}

	fmt.Fprintf(opts.Out, "Uploading %d redacted files (%d bytes) to a secret gist at %s:\n", len(gist.Files), gist.Size, gistAPIURL)
	for _, name := range gist.Names() {
		fmt.Fprintf(opts.Out, "  %s (%d bytes)\n", name, len(gist.Files[name].Content))
	}
	if err := gist.Upload(log, opts.Token); err != nil {
		return err
	}

	log.Info(fmt.Sprintf("debug information uploaded to: %s", gist.URL))
	return nil
}

func captureCmd(name string, arg ...string) (string, error) {
	var buf bytes.Buffer
	c := exec.Command(name, arg...)
	c.Stdout = &buf
	if err := c.Run(); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// captureDebugCmdFirstSuccess runs alternatives in order until one succeeds (e.g. ip vs ifconfig).
func captureDebugCmdFirstSuccess(log log15.Logger, desc string, alternatives [][]string) string {
	var lastErr error
	for _, cmd := range alternatives {
		output, err := captureCmd(cmd[0], cmd[1:]...)
		if err == nil {
			return fmt.Sprintln("===>", strings.Replace(strings.Join(cmd, " "), "\n", `\n`, -1), "\n", output)
		}
		lastErr = err
	}
	log.Error(fmt.Sprintf("error capturing %s (tried: %s)", desc, joinAltCmds(alternatives)), "err", lastErr)
	return ""
}

func joinAltCmds(alternatives [][]string) string {
	parts := make([]string, len(alternatives))
	for i, cmd := range alternatives {
		parts[i] = strings.Join(cmd, " ")
	}
	return strings.Join(parts, ", ")
}

func captureJobs(gist *Gist, env bool, redactor *debugRedactor) error {
	client := cluster.NewClient()

	jobs, err := jobList(client, true)
	if err != nil {
		return err
	}

	// Every job's secret env values are redacted wherever they show up (ps
	// command lines, logs, scheduler state), not only for system jobs.
	for _, job := range jobs {
		if job.Job != nil {
			redactor.AddEnv(job.Job.Config.Env)
		}
	}

	var buf bytes.Buffer
	printJobs(jobs, &buf)
	gist.AddFile("1-jobs.log", buf.String())

	for _, job := range jobs {
		var name string
		if system, ok := job.Job.Metadata["flynn-system-app"]; !ok || system != "true" {
			continue // Skip non-system applications
		}
		if app, ok := job.Job.Metadata["flynn-controller.app_name"]; ok {
			name += app + "-"
		}
		if typ, ok := job.Job.Metadata["flynn-controller.type"]; ok {
			name += typ + "-"
		}
		name += job.Job.ID + ".log"

		var content bytes.Buffer
		printJobDesc(&job, &content, env, secretEnvNames(job.Job.Config.Env))
		fmt.Fprint(&content, "\n\n***** ***** ***** ***** ***** ***** ***** ***** ***** *****\n\n")
		getLog(job.HostID, job.Job.ID, client, false, true, &content, &content)
		gist.AddFile(name, content.String())
	}

	return nil
}

func captureSchedulerState(gist *Gist) error {
	leader, err := discoverd.NewService("controller-scheduler").Leader()
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 10 * time.Second}
	res, err := client.Get(fmt.Sprintf("http://%s/debug/state", leader.Addr))
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected HTTP status: %s", res.Status)
	}
	body, err := ioutil.ReadAll(res.Body)
	if err != nil {
		return err
	}
	gist.AddFile("scheduler-state.json", string(body))
	return nil
}

func captureSireniaMetadata(gist *Gist) error {
	appliances := plugin.SireniaServiceNames()
	for _, appliance := range appliances {
		meta, err := discoverd.NewService(appliance).GetMeta()
		if err != nil {
			continue // skip if no service, appliance may not be enabled
		}
		dstate := &state.DiscoverdState{
			Index: meta.Index,
			State: &state.State{},
		}
		if len(meta.Data) > 0 {
			err := json.Unmarshal(meta.Data, dstate.State)
			if err != nil {
				continue
			}
			body, err := json.MarshalIndent(dstate, "", "    ")
			if err != nil {
				continue
			}
			gist.AddFile(fmt.Sprintf("%s-sirenia-state.json", appliance), string(body))
		}
	}
	return nil
}
