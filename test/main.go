package main

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/flynn/go-check"
	"github.com/randy-girard/flynn/pkg/shutdown"
	"github.com/randy-girard/flynn/test/arg"
	testcluster "github.com/randy-girard/flynn/test/cluster/client"
)

var args *arg.Args
var routerIP string
var testCluster *testcluster.Client

func init() {
	log.SetFlags(log.Lshortfile)
	// go test passes -test.* flags. arg.Parse rejects those, and this package
	// is still built by `go test ./...`.
	if underGoTest() {
		return
	}
	args = arg.Parse()
	if args.Stream {
		args.Debug = true
	}
}

func underGoTest() bool {
	for _, a := range os.Args {
		if strings.HasPrefix(a, "-test.") {
			return true
		}
	}
	return false
}

func main() {
	defer shutdown.Exit()

	var err error
	var res *check.Result
	// defer exiting here so it runs after all other defers
	defer func() {
		if err != nil || res != nil && !res.Passed() {
			os.Exit(1)
		}
	}()

	routerIP = args.RouterIP

	if args.ClusterAPI != "" {
		testCluster, err = testcluster.NewClient(args.ClusterAPI)
		if err != nil {
			log.Println(err)
			return
		}
	}

	if err = setupGitreceive(); err != nil {
		log.Println(err)
		return
	}

	res = check.RunAll(&check.RunConf{
		Filter:           args.Run,
		Stream:           args.Stream,
		Verbose:          args.Debug,
		KeepWorkDir:      args.Debug,
		ConcurrencyLevel: args.Concurrency,
	})
	fmt.Println(res)
}

func setupGitreceive() error {
	// Unencrypted SSH private key for the flynn-test GitHub account.
	// Omits header/footer to avoid any GitHub auto-revoke key crawlers
	sshKey := `MIIEpAIBAAKCAQEA2UnQ/17TfzQRt4HInuP1SYz/tSNaCGO3NDIPLydVu8mmxuKT
zlJtH3pz3uWpMEKdZtSjV+QngJL8OFzanQVZtRBJjF2m+cywHJoZA5KsplMon+R+
QmVqu92WlcRdkcft1F1CLoTXTmHHfvuhOkG6GgJONNLP9Z14EsQ7MbBh5guafWOX
kdGFajyd+T2aj27yIkK44WjWqiLjxRIAtgOJrmd/3H0w3E+O1cgNrA2gkFEUhvR1
OHz8SmugYva0VZWKvxZ6muZvn26L1tajYsCntCRR3/a74cAnVFAXjqSatL6YTbSH
sdtE91kEC73/U4SL3OFdDiCrAvXpJ480C2/GQQIDAQABAoIBAHNQNVYRIPS00WIt
wiZwm8/4wAuFQ1aIdMWCe4Ruv5T1I0kRHZe1Lqwx9CQqhWtTLu1Pk5AlSMF3P9s5
i9sg58arahzP5rlS43OKZBP9Vxq9ryWLwWXDJK2mny/EElQ3YgP9qg29+fVi9thw
+dNM5lK/PnnSFwMmGn77HN712D6Yl3CCJJjsAunTfPzR9hyEqX5YvUB5eq/TNhXe
sqrKcGORIoNfv7WohlFSkTAXIvoMxmFWXg8piZ9/b1W4NwvO4wup3ZSErIk0AQ97
HtyXJIXgtj6pLkPqvPXPGvS3quYAddNxvGIdvge7w5LHnrxOzdqbeDAVmJLVwVlv
oo+7aQECgYEA8ZliUuA8q86SWE0N+JZUqbTvE6VzyWG0/u0BJYDkH7yHkbpFOIEy
KTw048WOZLQ6/wPwL8Hb090Cas/6pmRFMgCedarzXc9fvGEwW95em7jA4AyOVBMC
KIAmaYkm6LcUFeyR6ektZeCkT0MNoi4irjBC3/hMRyZu+6RL4jXxHLkCgYEA5j13
2nkbV99GtRRjyGB7uMkrhMere2MekANXEm4dW+LZFZUda4YCqdzfjDfBTxsuyGqi
DnvI7bZFzIQPiiEzvL2Mpiy7JqxmPLGmwzxDp3z75T5vOrGs4g9IQ7yDjp5WPzjz
KCJJHn8Qt9tNZb5h0hBM+NWLT0c1XxtTIVFfgckCgYAfNpTYZjYQcFDB7bqXWjy3
7DNTE3YhF2l94fra8IsIep/9ONaGlVJ4t1mR780Uv6A7oDOgx+fxuET+rb4RTzUN
X70ZMKvee9M/kELiK5mHftgUWirtO8N0nhHYYqrPOA/1QSoc0U5XMi2oO96ADHvY
i02oh/i63IFMK47OO+/ZqQKBgQCY8bY/Y/nc+o4O1hee0TD+xGvrTXRFh8eSpRVf
QdSw6FWKt76OYbw9OGMr0xHPyd/e9K7obiRAfLeLLyLfgETNGSFodghwnU9g/CYq
RUsv5J+0XjAnTkXo+Xvouz6tK9NhNiSYwYXPA1uItt6IOtriXz+ygLCFHml+3zju
xg5quQKBgQCEL95Di6WD+155gEG2NtqeAOWhgxqAbGjFjfpV+pVBksBCrWOHcBJp
QAvAdwDIZpqRWWMcLS7zSDrzn3ZscuHCMxSOe40HbrVdDUee24/I4YQ+R8EcuzcA
3IV9ai+Bxs6PvklhXmarYxJl62LzPLyv0XFscGRes/2yIIxNfNzFug==`

	out, err := flynnCmd("/", "-a", "gitreceive", "env", "set",
		"SSH_CLIENT_HOSTS=github.com ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQCj7ndNxQowgcQnjshcLrqPEiiphnt+VTTvDP6mHBL9j1aNUkY4Ue1gvwnGLVlOhGeYrnZaMgRK6+PKCUXaDbC7qtbW8gIkhL7aGCsOr/C56SJMy/BCZfxd1nWzAOxSDPgVsmerOBYfNqltV9/hWCqBywINIR+5dIg6JTJ72pcEpEjcYgXkE2YEFXV1JHnsKgbLWNlhScqb2UmyRkQyytRLtL+38TGxkxCflmO+5Z8CSSNY7GidjMIZ7Q4zMjA2n1nGrlTDkzwDCsw+wqFPGQA179cnfGWOWRVruj16z6XyvxvjJwbz0wQZ75XK5tKSb7FNyeIEs4TT4jk+S4dhPeAUC5y+bDYirYgM4GC7uEnztnZyaVWQ7B381AK4Qdrwt51ZqExKbQpTUNn+EjqoTwvqNj4kqx5QUCI0ThS/YkOxJCXmPUWZbhjpCg56i+2aB6CmK2JGhn57K5mj0MNdBXA4/WnwH6XoPWJzK5Nyu2zB3nAZp+S5hpQs+p1vN1/wsjk=",
		fmt.Sprintf("SSH_CLIENT_KEY=-----BEGIN RSA PRIVATE KEY-----\n%s\n-----END RSA PRIVATE KEY-----\n", sshKey)).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %q", err, string(out))
	}
	return nil
}

type CmdResult struct {
	Cmd       []string
	Output    string
	OutputBuf io.Reader
	Err       error
}

func flynnEnv(path string) []string {
	env := os.Environ()
	res := make([]string, 0, len(env)+1)
	for _, v := range env {
		if !strings.HasPrefix(v, "FLYNNRC=") {
			res = append(res, v)
		}
	}
	res = append(res, "FLYNNRC="+path)
	return res
}

func flynnCmd(dir string, cmdArgs ...string) *exec.Cmd {
	cmd := exec.Command(args.CLI, cmdArgs...)
	cmd.Dir = dir
	return cmd
}

func flynn(t *check.C, dir string, args ...string) *CmdResult {
	return run(t, flynnCmd(dir, args...))
}

func debug(t *check.C, v ...interface{}) {
	t.Log(append([]interface{}{"++ ", time.Now().Format("15:04:05.000"), " "}, v...)...)
}

func debugf(t *check.C, format string, v ...interface{}) {
	t.Logf(strings.Join([]string{"++", time.Now().Format("15:04:05.000"), format}, " "), v...)
}

func run(t *check.C, cmd *exec.Cmd) *CmdResult {
	var out bytes.Buffer
	debug(t, strings.Join(append([]string{cmd.Path}, cmd.Args[1:]...), " "))
	if args.Stream {
		cmd.Stdout = io.MultiWriter(os.Stdout, &out)
		cmd.Stderr = io.MultiWriter(os.Stderr, &out)
	} else {
		cmd.Stdout = &out
		cmd.Stderr = &out
	}
	err := cmd.Run()
	res := &CmdResult{
		Cmd:       cmd.Args,
		Err:       err,
		Output:    out.String(),
		OutputBuf: &out,
	}
	if !args.Stream {
		t.Log(res.Output)
	}
	return res
}

var Outputs check.Checker = outputChecker{
	&check.CheckerInfo{
		Name:   "Outputs",
		Params: []string{"result", "output"},
	},
}

type outputChecker struct {
	*check.CheckerInfo
}

func (outputChecker) Check(params []interface{}, names []string) (bool, string) {
	ok, msg, s, res := checkCmdResult(params, names)
	if !ok {
		return ok, msg
	}
	return s == commandOutput(res.Output), ""
}

// commandOutput drops the one-line rename notice the CLI writes when an
// old command name is used. Exact output checks are about the command result.
func commandOutput(s string) string {
	trailingNL := strings.HasSuffix(s, "\n")
	lines := strings.Split(s, "\n")
	if trailingNL && len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	kept := lines[:0]
	for _, line := range lines {
		if strings.HasPrefix(line, "flynn ") && strings.Contains(line, " is now flynn ") {
			continue
		}
		kept = append(kept, line)
	}
	out := strings.Join(kept, "\n")
	if trailingNL {
		out += "\n"
	}
	return out
}

func firstOutputLine(s string) string {
	s = strings.TrimSpace(commandOutput(s))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

// tcpRoutePort is the port on the first line of `flynn route:add tcp`.
// A later line tells the operator the cluster firewall opens that port.
func tcpRoutePort(output string) string {
	fields := strings.Fields(firstOutputLine(output))
	if len(fields) == 0 {
		return ""
	}
	return fields[len(fields)-1]
}

func checkCmdResult(params []interface{}, names []string) (ok bool, msg, s string, res *CmdResult) {
	res, ok = params[0].(*CmdResult)
	if !ok {
		msg = "result must be a *CmdResult"
		return
	}
	switch v := params[1].(type) {
	case []byte:
		s = string(v)
	case string:
		s = v
	default:
		msg = "output must be a []byte or string"
		return
	}
	ok = true
	return
}

var OutputContains check.Checker = outputContainsChecker{
	&check.CheckerInfo{
		Name:   "OutputContains",
		Params: []string{"result", "contains"},
	},
}

type outputContainsChecker struct {
	*check.CheckerInfo
}

func (outputContainsChecker) Check(params []interface{}, names []string) (bool, string) {
	ok, msg, s, res := checkCmdResult(params, names)
	if !ok {
		return ok, msg
	}
	return strings.Contains(res.Output, s), ""
}

var Succeeds check.Checker = succeedsChecker{
	&check.CheckerInfo{
		Name:   "Succeeds",
		Params: []string{"result"},
	},
}

type succeedsChecker struct {
	*check.CheckerInfo
}

func (succeedsChecker) Check(params []interface{}, names []string) (bool, string) {
	res, ok := params[0].(*CmdResult)
	if !ok {
		return false, "result must be a *CmdResult"
	}
	return res.Err == nil, ""
}

var SuccessfulOutputContains check.Checker = successfulOutputContainsChecker{
	&check.CheckerInfo{
		Name:   "SuccessfulOutputContains",
		Params: []string{"result", "contains"},
	},
	Succeeds,
	OutputContains,
}

type successfulOutputContainsChecker struct {
	*check.CheckerInfo
	succeeds       check.Checker
	outputContains check.Checker
}

func (s successfulOutputContainsChecker) Check(params []interface{}, names []string) (bool, string) {
	ok, res := s.succeeds.Check(params, names)
	if ok {
		return s.outputContains.Check(params, names)
	}
	return ok, res
}

type matchesChecker struct {
	*check.CheckerInfo
}

var Matches check.Checker = &matchesChecker{
	&check.CheckerInfo{Name: "Matches", Params: []string{"value", "regex"}},
}

func (checker *matchesChecker) Check(params []interface{}, names []string) (result bool, error string) {
	return matches(params[0], params[1])
}

func matches(value, regex interface{}) (result bool, error string) {
	reStr, ok := regex.(string)
	if !ok {
		return false, "Regex must be a string"
	}
	valueStr, valueIsStr := value.(string)
	if !valueIsStr {
		if valueWithStr, valueHasStr := value.(fmt.Stringer); valueHasStr {
			valueStr, valueIsStr = valueWithStr.String(), true
		}
	}
	if valueIsStr {
		matches, err := regexp.MatchString(reStr, valueStr)
		if err != nil {
			return false, "Can't compile regex: " + err.Error()
		}
		return matches, ""
	}
	return false, "Obtained value is not a string and has no .String()"
}
