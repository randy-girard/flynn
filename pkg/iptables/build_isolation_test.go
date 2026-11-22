package iptables

import (
	"net"
	"strings"
	"testing"

	"github.com/randy-girard/flynn/pkg/netpolicy"
)

// packet is one NEW connection as FORWARD sees it.
type packet struct {
	src, dst []string // ipsets the source and destination IPs are in
	dstIP    string
	proto    string
	dport    string
	state    string
}

// verdict walks chain (in kernel order) and returns the target of the first
// rule that matches, or "" when the packet falls through to the rules
// EnableOutboundNAT installed (bridge egress and overlay ACCEPT).
func verdict(t *testing.T, chain [][]string, p packet) string {
	t.Helper()
	if p.state == "" {
		p.state = "NEW"
	}
	if p.proto == "" {
		p.proto = "tcp"
	}
	for _, rule := range chain {
		if target, ok := matchRule(t, rule, p); ok {
			return target
		}
	}
	return ""
}

func matchRule(t *testing.T, rule []string, p packet) (string, bool) {
	t.Helper()
	in := func(sets []string, name string) bool {
		for _, s := range sets {
			if s == name {
				return true
			}
		}
		return false
	}
	if rule[0] != "FORWARD" {
		t.Fatalf("not a FORWARD rule: %v", rule)
	}
	target := ""
	for i := 1; i < len(rule); i++ {
		switch rule[i] {
		case "-m":
			i++ // set / conntrack / comment: the option that follows does the match
		case "--match-set":
			name, dir := rule[i+1], rule[i+2]
			i += 2
			switch dir {
			case "src":
				if !in(p.src, name) {
					return "", false
				}
			case "dst":
				if !in(p.dst, name) {
					return "", false
				}
			default:
				t.Fatalf("unsupported set direction %q in %v", dir, rule)
			}
		case "-d":
			cidr := rule[i+1]
			i++
			if !strings.Contains(cidr, "/") {
				cidr += "/32"
			}
			_, n, err := net.ParseCIDR(cidr)
			if err != nil {
				t.Fatalf("bad -d %q", rule[i])
			}
			if !n.Contains(net.ParseIP(p.dstIP)) {
				return "", false
			}
		case "-p":
			if rule[i+1] != p.proto {
				return "", false
			}
			i++
		case "--dport":
			if rule[i+1] != p.dport {
				return "", false
			}
			i++
		case "--ctstate":
			if !strings.Contains(rule[i+1], p.state) {
				return "", false
			}
			i++
		case "--comment":
			i++
		case "-j":
			target = rule[i+1]
			i++
		default:
			t.Fatalf("matcher does not understand %q in %v", rule[i], rule)
		}
	}
	return target, true
}

// installed is the FORWARD chain after EnableJobIsolation inserts each rule
// at the head, in list order.
func installed(rules [][]string) [][]string {
	chain := make([][]string, 0, len(rules))
	for _, r := range rules {
		chain = append([][]string{r}, chain...)
	}
	return chain
}

const (
	testOverlay = "100.64.0.0/16"
	testBridge  = "100.64.57.1"
)

// TestBuildJobsOnlyReachBuildServices (EDGE-04): slugbuilder and
// dockerbuilder run tenant code. On the overlay they may open only the
// controller, blobstore and tarreceive; logaggregator, the system Postgres,
// tenant datastores and user jobs are dropped. The internet stays open.
func TestBuildJobsOnlyReachBuildServices(t *testing.T) {
	chain := installed(JobIsolationForwardRules(testOverlay, testBridge))
	build := []string{netpolicy.ServiceBuild}
	for _, tc := range []struct {
		name string
		p    packet
		want string
	}{
		{"controller", packet{src: build, dst: []string{netpolicy.ServiceSys, BuildServiceSet}, dstIP: "100.64.12.5", dport: "80"}, "ACCEPT"},
		{"blobstore", packet{src: build, dst: []string{netpolicy.ServiceSys, BuildServiceSet}, dstIP: "100.64.13.9", dport: "80"}, "ACCEPT"},
		{"logaggregator api", packet{src: build, dst: []string{netpolicy.ServiceSys, LogaggregatorSet}, dstIP: "100.64.12.7", dport: "80"}, "DROP"},
		{"logaggregator syslog", packet{src: build, dst: []string{netpolicy.ServiceSys, LogaggregatorSet}, dstIP: "100.64.12.7", dport: "514"}, "DROP"},
		{"other system job", packet{src: build, dst: []string{netpolicy.ServiceSys}, dstIP: "100.64.12.8", dport: "80"}, "DROP"},
		{"datastore data port", packet{src: build, dst: []string{netpolicy.ServiceData}, dstIP: "100.64.14.2", dport: "5432"}, "DROP"},
		{"datastore admin port", packet{src: build, dst: []string{netpolicy.ServiceData}, dstIP: "100.64.14.2", dport: "5433"}, "DROP"},
		{"user job", packet{src: build, dst: []string{netpolicy.ServiceUser}, dstIP: "100.64.14.3", dport: "8080"}, "DROP"},
		{"overlay ip not yet in any set", packet{src: build, dstIP: "100.64.40.4", dport: "80"}, "DROP"},
		{"node underlay", packet{src: build, dst: []string{NodeSet}, dstIP: "192.168.57.21", dport: "5000"}, "DROP"},
		{"internet", packet{src: build, dstIP: "140.82.112.3", dport: "443"}, ""},
	} {
		if got := verdict(t, chain, tc.p); got != tc.want {
			t.Errorf("build → %s: got %q want %q", tc.name, got, tc.want)
		}
	}
	// Replies to connections a build opened are not NEW and must pass.
	reply := packet{src: build, dst: []string{netpolicy.ServiceSys}, dstIP: "100.64.12.8", dport: "80", state: "ESTABLISHED"}
	if got := verdict(t, chain, reply); got == "DROP" {
		t.Error("build drop must be NEW-only")
	}
}

// TestOnlyHostAndPlatformJobsReachLogaggregator: user and build jobs are
// covered by their overlay drops; datastore engines (tenant-reachable
// processes) may not open the syslog or API port either. gitreceive and
// other platform jobs still ship to it, and flynn-host traffic does not
// traverse FORWARD.
func TestOnlyHostAndPlatformJobsReachLogaggregator(t *testing.T) {
	chain := installed(JobIsolationForwardRules(testOverlay, testBridge))
	logagg := []string{netpolicy.ServiceSys, LogaggregatorSet}
	for _, tc := range []struct {
		name string
		src  string
		port string
		want string
	}{
		{"user syslog", netpolicy.ServiceUser, "514", "DROP"},
		{"user api", netpolicy.ServiceUser, "80", "DROP"},
		{"datastore syslog", netpolicy.ServiceData, "514", "DROP"},
		{"datastore api", netpolicy.ServiceData, "80", "DROP"},
		{"gitreceive syslog", netpolicy.ServiceSys, "514", ""},
		{"controller api", netpolicy.ServiceSys, "80", ""},
	} {
		p := packet{src: []string{tc.src}, dst: logagg, dstIP: "100.64.12.7", dport: tc.port}
		if got := verdict(t, chain, p); got != tc.want {
			t.Errorf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
	// User jobs keep their attached datastore data plane.
	p := packet{src: []string{netpolicy.ServiceUser}, dst: []string{netpolicy.ServiceData}, dstIP: "100.64.14.2", dport: "5432"}
	if got := verdict(t, chain, p); got != "ACCEPT" {
		t.Fatalf("user → datastore 5432: %q", got)
	}
}

// TestEnableJobIsolationInstallsListedRules: EnableJobIsolation inserts
// exactly JobIsolationForwardRules, and creates the service sets the rules
// reference before inserting them.
func TestEnableJobIsolationInstallsListedRules(t *testing.T) {
	sets := map[string]bool{}
	for _, s := range JobIsolationSets() {
		sets[s] = true
	}
	for _, rule := range JobIsolationForwardRules(testOverlay, testBridge) {
		for i, a := range rule {
			if a == "--match-set" && !sets[rule[i+1]] {
				t.Fatalf("rule %v references set %s that EnableJobIsolation does not create", rule, rule[i+1])
			}
		}
	}
	for _, s := range []string{BuildServiceSet, LogaggregatorSet} {
		if len(s) > 31-len("-swap") {
			t.Fatalf("ipset name %q too long for ReplaceSetIPs", s)
		}
	}
}

// TestApplyOverlayRulesWidensBuildDrop: overlay:set to a wider network must
// move the build drop as well as the user drop, or builds reach jobs in the
// added range.
func TestApplyOverlayRulesWidensBuildDrop(t *testing.T) {
	nat := "-P POSTROUTING ACCEPT\n-A POSTROUTING -s 100.100.9.0/24 ! -o flynnbr0 ! -d 100.64.0.0/10 -j MASQUERADE"
	fwd := strings.Join([]string{
		"-P FORWARD ACCEPT",
		"-A FORWARD -m set --match-set flynn-net-build src -m set --match-set " + BuildServiceSet + " dst -j ACCEPT",
		"-A FORWARD -m set --match-set flynn-net-build src -d 100.100.0.0/16 -m conntrack --ctstate NEW -j DROP",
		"-A FORWARD -m set --match-set flynn-net-user src -d 100.64.0.0/10 -m conntrack --ctstate NEW -j DROP",
		"-A FORWARD -i flynnbr0 ! -o flynnbr0 -j ACCEPT",
	}, "\n")
	var calls []string
	run := func(args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		calls = append(calls, joined)
		switch {
		case strings.Contains(joined, "-S POSTROUTING"):
			return []byte(nat), nil
		case strings.Contains(joined, "-S FORWARD"):
			return []byte(fwd), nil
		}
		return nil, nil
	}
	if err := ApplyOverlayRules(run, "flynnbr0", "100.100.9.1/24", "100.64.0.0/10"); err != nil {
		t.Fatal(err)
	}
	body := strings.Join(calls, "\n")
	want := "-I FORWARD 4 " + strings.Join(BuildOverlayDropArgs("100.64.0.0/10")[1:], " ")
	if !strings.Contains(body, want) {
		t.Fatalf("missing %q (wider build drop before bridge egress):\n%s", want, body)
	}
	if !strings.Contains(body, "-D FORWARD -m set --match-set flynn-net-build src -d 100.100.0.0/16") {
		t.Fatalf("stale build drop not deleted:\n%s", body)
	}
	if strings.Contains(body, "-D FORWARD -m set --match-set flynn-net-build src -m set --match-set "+BuildServiceSet) {
		t.Fatalf("must not delete the build allow:\n%s", body)
	}
	if strings.Contains(body, "-I FORWARD 4 -m set --match-set flynn-net-user") {
		t.Fatalf("user drop already matches, must not be re-inserted:\n%s", body)
	}
}
