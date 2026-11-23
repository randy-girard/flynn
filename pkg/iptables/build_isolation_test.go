package iptables

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"testing"

	"github.com/randy-girard/flynn/pkg/netpolicy"
)

// packet is one NEW connection as FORWARD sees it.
type packet struct {
	src, dst []string // ipsets the source and destination IPs are in
	pairs    []string // pair sets (src,dst) the source/destination pair is in
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
	if rule[0] != "FORWARD" && rule[0] != "INPUT" {
		t.Fatalf("not a FORWARD or INPUT rule: %v", rule)
	}
	target := ""
	negate := false
	for i := 1; i < len(rule); i++ {
		switch rule[i] {
		case "!":
			if i+1 >= len(rule) || rule[i+1] != "--match-set" {
				t.Fatalf("matcher only negates --match-set: %v", rule)
			}
			negate = true
		case "-m":
			i++ // set / conntrack / comment: the option that follows does the match
		case "--match-set":
			name, dir := rule[i+1], rule[i+2]
			i += 2
			neg := negate
			negate = false
			switch dir {
			case "src":
				if !in(p.src, name) {
					return "", false
				}
			case "dst":
				if in(p.dst, name) == neg {
					return "", false
				}
			case "src,dst":
				if !in(p.pairs, name) {
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

// installed is the chain after EnableJobIsolation inserts each rule
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
	pairSets := map[string]bool{}
	for _, s := range JobIsolationPairSets() {
		pairSets[s] = true
	}
	for _, rule := range append(JobIsolationForwardRules(testOverlay, testBridge), HostIsolationInputRules()...) {
		for i, a := range rule {
			if a != "--match-set" {
				continue
			}
			name, dir := rule[i+1], rule[i+2]
			if dir == "src,dst" {
				if !pairSets[name] {
					t.Fatalf("rule %v matches pair set %s that EnableJobIsolation does not create as a pair set", rule, name)
				}
				continue
			}
			if !sets[name] {
				t.Fatalf("rule %v references set %s that EnableJobIsolation does not create", rule, name)
			}
		}
	}
	for _, s := range []string{BuildServiceSet, LogaggregatorSet, DataPeerSet, DataSystemSet} {
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

// TestDatastoreJobsOnlyReachPeersAndDiscoverd (EDGE-29): datastore engines
// run tenant-reachable code (SQL functions, ClickHouse url(), Redis
// modules). On the overlay they may open other engines of their own
// datastore app on any port (KRaft, Keeper and ClickHouse interserver have
// no authentication) and any engine on the authenticated data and admin
// ports (cross-app followers replicate from their primary). Every system
// job, user job and unknown overlay IP is dropped, and on the node
// underlay only discoverd is open.
func TestDatastoreJobsOnlyReachPeersAndDiscoverd(t *testing.T) {
	chain := installed(JobIsolationForwardRules(testOverlay, testBridge))
	data := []string{netpolicy.ServiceData}
	sameApp := []string{DataPeerSet}
	for _, tc := range []struct {
		name string
		p    packet
		want string
	}{
		{"controller", packet{src: data, dst: []string{netpolicy.ServiceSys, BuildServiceSet}, dstIP: "100.64.12.5", dport: "80"}, "DROP"},
		{"blobstore", packet{src: data, dst: []string{netpolicy.ServiceSys, BuildServiceSet}, dstIP: "100.64.13.9", dport: "80"}, "DROP"},
		{"logaggregator syslog", packet{src: data, dst: []string{netpolicy.ServiceSys, LogaggregatorSet}, dstIP: "100.64.12.7", dport: "514"}, "DROP"},
		{"gitreceive / status / plugin api", packet{src: data, dst: []string{netpolicy.ServiceSys}, dstIP: "100.64.12.8", dport: "8080"}, "DROP"},
		{"user job", packet{src: data, dst: []string{netpolicy.ServiceUser}, dstIP: "100.64.14.3", dport: "8080"}, "DROP"},
		{"overlay ip not yet in any set", packet{src: data, dstIP: "100.64.40.4", dport: "80"}, "DROP"},
		{"same-app peer postgres", packet{src: data, dst: data, pairs: sameApp, dstIP: "100.64.14.2", dport: "5432"}, "ACCEPT"},
		{"same-app peer sirenia admin", packet{src: data, dst: data, pairs: sameApp, dstIP: "100.64.14.2", dport: "5433"}, "ACCEPT"},
		{"same-app clickhouse interserver", packet{src: data, dst: data, pairs: sameApp, dstIP: "100.64.14.2", dport: "9009"}, "ACCEPT"},
		{"same-app clickhouse keeper raft", packet{src: data, dst: data, pairs: sameApp, dstIP: "100.64.14.2", dport: "9234"}, "ACCEPT"},
		{"same-app kafka kraft", packet{src: data, dst: data, pairs: sameApp, dstIP: "100.64.14.2", dport: "9093"}, "ACCEPT"},
		{"cross-app follower postgres", packet{src: data, dst: data, dstIP: "100.64.14.2", dport: "5432"}, "ACCEPT"},
		{"other app → system postgres", packet{src: data, dst: []string{netpolicy.ServiceData, DataSystemSet}, dstIP: "100.64.14.9", dport: "5432"}, "DROP"},
		{"other app → system postgres sirenia admin", packet{src: data, dst: []string{netpolicy.ServiceData, DataSystemSet}, dstIP: "100.64.14.9", dport: "5433"}, "DROP"},
		{"system postgres peer → peer", packet{src: data, dst: []string{netpolicy.ServiceData, DataSystemSet}, pairs: sameApp, dstIP: "100.64.14.9", dport: "5433"}, "ACCEPT"},
		{"cross-app follower redis tls", packet{src: data, dst: data, dstIP: "100.64.14.2", dport: "16379"}, "ACCEPT"},
		{"cross-app follower mariadb backup", packet{src: data, dst: data, dstIP: "100.64.14.2", dport: "3307"}, "ACCEPT"},
		{"other app clickhouse interserver", packet{src: data, dst: data, dstIP: "100.64.14.2", dport: "9009"}, "DROP"},
		{"other app clickhouse keeper", packet{src: data, dst: data, dstIP: "100.64.14.2", dport: "9181"}, "DROP"},
		{"other app kafka kraft", packet{src: data, dst: data, dstIP: "100.64.14.2", dport: "9093"}, "DROP"},
		{"other app kafka inter-broker", packet{src: data, dst: data, dstIP: "100.64.14.2", dport: "9094"}, "DROP"},
		{"other app metrics", packet{src: data, dst: data, dstIP: "100.64.14.2", dport: "9180"}, "DROP"},
		{"node discoverd", packet{src: data, dst: []string{NodeSet}, dstIP: "192.168.57.21", dport: netpolicy.DiscoverdHTTPPort}, "ACCEPT"},
		{"node router api", packet{src: data, dst: []string{NodeSet}, dstIP: "192.168.57.21", dport: "5000"}, "DROP"},
		{"node router http", packet{src: data, dst: []string{NodeSet}, dstIP: "192.168.57.21", dport: "80"}, "DROP"},
		{"node router https", packet{src: data, dst: []string{NodeSet}, dstIP: "192.168.57.21", dport: "443"}, "DROP"},
		{"node host api", packet{src: data, dst: []string{NodeSet}, dstIP: "192.168.57.21", dport: "1113"}, "DROP"},
		{"node ssh", packet{src: data, dst: []string{NodeSet}, dstIP: "192.168.57.21", dport: "22"}, "DROP"},
		{"node flannel", packet{src: data, dst: []string{NodeSet}, dstIP: "192.168.57.21", dport: "5002"}, "DROP"},
		{"internet", packet{src: data, dstIP: "140.82.112.3", dport: "443"}, ""},
	} {
		if got := verdict(t, chain, tc.p); got != tc.want {
			t.Errorf("datastore → %s: got %q want %q", tc.name, got, tc.want)
		}
	}
	// Replies to connections other jobs opened (status checks, user
	// clients, the plugin API) are not NEW and must pass.
	for _, dst := range [][]string{{netpolicy.ServiceSys}, {netpolicy.ServiceUser}, {NodeSet}} {
		reply := packet{src: data, dst: dst, dstIP: "100.64.12.8", dport: "40000", state: "ESTABLISHED"}
		if got := verdict(t, chain, reply); got == "DROP" {
			t.Errorf("datastore reply to %v dropped: the drops must be NEW-only", dst)
		}
	}
	// Other classes keep their access to datastores.
	for _, tc := range []struct {
		name string
		p    packet
		want string
	}{
		{"user → data port", packet{src: []string{netpolicy.ServiceUser}, dst: data, dstIP: "100.64.14.2", dport: "5432"}, "ACCEPT"},
		{"user → admin port", packet{src: []string{netpolicy.ServiceUser}, dst: data, dstIP: "100.64.14.2", dport: "5433"}, "DROP"},
		{"system → admin port", packet{src: []string{netpolicy.ServiceSys}, dst: data, dstIP: "100.64.14.2", dport: "5433"}, ""},
	} {
		if got := verdict(t, chain, tc.p); got != tc.want {
			t.Errorf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
}

// TestDatastoreJobsOnlyOpenDNSAndDiscoverdOnTheirHost (EDGE-29): on the
// host they run on, datastore engines may open DNS and discoverd. The
// router API (:5000), the public router listeners, SSH, the host API and
// every other host socket are dropped.
func TestDatastoreJobsOnlyOpenDNSAndDiscoverdOnTheirHost(t *testing.T) {
	chain := installed(HostIsolationInputRules())
	data := []string{netpolicy.ServiceData}
	for _, tc := range []struct {
		proto, port, want string
	}{
		{"udp", "53", "ACCEPT"},
		{"tcp", "53", "ACCEPT"},
		{"tcp", netpolicy.DiscoverdHTTPPort, "ACCEPT"},
		{"tcp", "5000", "DROP"},
		{"tcp", "80", "DROP"},
		{"tcp", "443", "DROP"},
		{"tcp", "1113", "DROP"},
		{"tcp", "22", "DROP"},
		{"tcp", "5002", "DROP"},
		{"udp", "8472", "DROP"},
	} {
		p := packet{src: data, dstIP: testBridge, proto: tc.proto, dport: tc.port}
		if got := verdict(t, chain, p); got != tc.want {
			t.Errorf("datastore → host %s/%s: got %q want %q", tc.proto, tc.port, got, tc.want)
		}
	}
	reply := packet{src: data, dstIP: testBridge, dport: "45000", state: "ESTABLISHED"}
	if got := verdict(t, chain, reply); got == "DROP" {
		t.Error("datastore host drop must be NEW-only")
	}
	// User and build jobs are unchanged: DNS only.
	for _, set := range []string{netpolicy.ServiceUser, netpolicy.ServiceBuild} {
		if got := verdict(t, chain, packet{src: []string{set}, dstIP: testBridge, dport: netpolicy.DiscoverdHTTPPort}); got != "DROP" {
			t.Errorf("%s → host discoverd: got %q want DROP", set, got)
		}
		if got := verdict(t, chain, packet{src: []string{set}, dstIP: testBridge, proto: "udp", dport: "53"}); got != "ACCEPT" {
			t.Errorf("%s → host DNS: got %q want ACCEPT", set, got)
		}
	}
}

// fakeChain is one iptables chain for EnsureRuleOrder: -S prints the rules
// it holds (the live ones in iptables' own spelling, inserted ones as
// given), -I chain [n] inserts, -D chain n deletes by number.
type fakeChain struct {
	t     *testing.T
	name  string
	rules [][]string
}

func (c *fakeChain) run(args ...string) ([]byte, error) {
	switch {
	case len(args) == 2 && args[0] == "-S" && args[1] == c.name:
		var b strings.Builder
		fmt.Fprintf(&b, "-P %s DROP\n", c.name)
		for _, r := range c.rules {
			fmt.Fprintf(&b, "-A %s %s\n", c.name, strings.Join(r, " "))
		}
		return []byte(b.String()), nil
	case len(args) > 2 && args[0] == "-I" && args[1] == c.name:
		pos, rest := 1, args[2:]
		if n, err := strconv.Atoi(rest[0]); err == nil {
			pos, rest = n, rest[1:]
		}
		r := append([]string(nil), rest...)
		c.rules = append(c.rules[:pos-1], append([][]string{r}, c.rules[pos-1:]...)...)
		return nil, nil
	case len(args) == 3 && args[0] == "-D" && args[1] == c.name:
		n, err := strconv.Atoi(args[2])
		if err != nil || n < 1 || n > len(c.rules) {
			c.t.Fatalf("bad delete %v", args)
		}
		c.rules = append(c.rules[:n-1], c.rules[n:]...)
		return nil, nil
	}
	c.t.Fatalf("unexpected iptables call %v", args)
	return nil, nil
}

func (c *fakeChain) chain() [][]string {
	out := make([][]string, len(c.rules))
	for i, r := range c.rules {
		out[i] = append([]string{c.name}, r...)
	}
	return out
}

// TestEnsureRuleOrderAfterUpgrade (EDGE-29, seen live): on a host updated
// from the per-port datastore drops, the DNS and discoverd accepts already
// existed, so the new all-port datastore drop was inserted above them and
// engines lost their local discoverd. EnsureRuleOrder must put each listed
// rule above the ones listed before it, using the chain as iptables prints
// it, without leaving any rule absent.
func TestEnsureRuleOrderAfterUpgrade(t *testing.T) {
	// INPUT as `iptables -S` printed it on dev-node1 after the update.
	live := []string{
		"-m set --match-set flynn-net-data src -m conntrack --ctstate NEW -j DROP",
		"-p tcp -m set --match-set flynn-net-data src -m tcp --dport 1111 -j ACCEPT",
		"-p udp -m set --match-set flynn-net-data src -m udp --dport 53 -j ACCEPT",
		"-p udp -m set --match-set flynn-net-build src -m udp --dport 53 -j ACCEPT",
		"-p udp -m set --match-set flynn-net-user src -m udp --dport 53 -j ACCEPT",
		"-p tcp -m set --match-set flynn-net-data src -m tcp --dport 53 -j ACCEPT",
		"-p tcp -m set --match-set flynn-net-build src -m tcp --dport 53 -j ACCEPT",
		"-p tcp -m set --match-set flynn-net-user src -m tcp --dport 53 -j ACCEPT",
		"-m set --match-set flynn-net-build src -m conntrack --ctstate NEW -j DROP",
		"-m set --match-set flynn-net-user src -m conntrack --ctstate NEW -j DROP",
		"-j ufw-before-input",
	}
	c := &fakeChain{t: t, name: "INPUT"}
	for _, l := range live {
		c.rules = append(c.rules, strings.Fields(l))
	}
	data := []string{netpolicy.ServiceData}
	if got := verdict(t, c.chain(), packet{src: data, dstIP: testBridge, dport: netpolicy.DiscoverdHTTPPort}); got != "DROP" {
		t.Fatalf("fixture: discoverd = %q, want the broken DROP", got)
	}
	if err := EnsureRuleOrder(c.run, "INPUT", HostIsolationInputRules()); err != nil {
		t.Fatal(err)
	}
	if len(c.rules) != len(live) {
		t.Fatalf("rule count %d, want %d: %v", len(c.rules), len(live), c.rules)
	}
	for _, tc := range []struct{ proto, port, want string }{
		{"tcp", netpolicy.DiscoverdHTTPPort, "ACCEPT"},
		{"udp", "53", "ACCEPT"},
		{"tcp", "53", "ACCEPT"},
		{"tcp", "5000", "DROP"},
	} {
		if got := verdict(t, c.chain(), packet{src: data, dstIP: testBridge, proto: tc.proto, dport: tc.port}); got != tc.want {
			t.Errorf("after repair, datastore → host %s/%s = %q, want %q", tc.proto, tc.port, got, tc.want)
		}
	}
	if last := strings.Join(c.rules[len(c.rules)-1], " "); last != "-j ufw-before-input" {
		t.Fatalf("rules not in the list moved: last = %q", last)
	}
	// Already in order: no calls besides -S.
	ordered := &fakeChain{t: t, name: "INPUT", rules: c.rules}
	calls := 0
	run := func(args ...string) ([]byte, error) {
		if args[0] != "-S" {
			calls++
		}
		return ordered.run(args...)
	}
	if err := EnsureRuleOrder(run, "INPUT", HostIsolationInputRules()); err != nil || calls != 0 {
		t.Fatalf("ordered chain changed: err=%v calls=%d", err, calls)
	}
}

// TestForwardUpgradeFromEDGE04Chain replays the update on the FORWARD chain
// dev-node1 had before EDGE-29: insert what is missing at the head (as
// insertIfMissing does), repair the order, delete the legacy rules. The
// result must isolate every class as the fresh rule list does.
func TestForwardUpgradeFromEDGE04Chain(t *testing.T) {
	const overlay, bridge = "100.100.0.0/16", "100.100.81.1"
	live := []string{
		"-d 169.254.0.0/16 -m set --match-set flynn-net-build src -m comment --comment flynn-egress-deny -j DROP",
		"-d 169.254.0.0/16 -m set --match-set flynn-net-user src -m comment --comment flynn-egress-deny -j DROP",
	}
	for _, p := range []string{"27017", "9440", "9092", "9000", "8443", "8123", "6379", "5432", "3306"} {
		live = append(live, "-p tcp -m set --match-set flynn-net-user src -m set --match-set flynn-net-data dst -m tcp --dport "+p+" -j ACCEPT")
	}
	live = append(live,
		"-d 100.100.81.1/32 -p udp -m set --match-set flynn-net-user src -m udp --dport 53 -j ACCEPT",
		"-d 100.100.81.1/32 -p tcp -m set --match-set flynn-net-user src -m tcp --dport 53 -j ACCEPT",
		"-m set --match-set flynn-net-build src -m set --match-set flynn-net-build-svc dst -j ACCEPT",
		"-m set --match-set flynn-net-build src -m set --match-set flynn-net-user dst -j DROP",
		"-m set --match-set flynn-net-data src -m set --match-set flynn-net-logagg dst -m conntrack --ctstate NEW -j DROP",
		"-p tcp -m set --match-set flynn-net-data src -m set --match-set flynn-net-nodes dst -m tcp --dport 1113 -m conntrack --ctstate NEW -j DROP",
		"-p tcp -m set --match-set flynn-net-data src -m set --match-set flynn-net-nodes dst -m tcp --dport 22 -m conntrack --ctstate NEW -j DROP",
		"-m set --match-set flynn-net-build src -m set --match-set flynn-net-nodes dst -m conntrack --ctstate NEW -j DROP",
		"-m set --match-set flynn-net-user src -m set --match-set flynn-net-nodes dst -m conntrack --ctstate NEW -j DROP",
		"-d 100.100.0.0/16 -m set --match-set flynn-net-build src -m conntrack --ctstate NEW -j DROP",
		"-d 100.100.0.0/16 -m set --match-set flynn-net-user src -m conntrack --ctstate NEW -j DROP",
	)
	c := &fakeChain{t: t, name: "FORWARD"}
	for _, l := range live {
		c.rules = append(c.rules, strings.Fields(l))
	}
	have := func(args []string) bool {
		for _, r := range c.rules {
			if ruleKey(r) == ruleKey(args[1:]) {
				return true
			}
		}
		return false
	}
	rules := JobIsolationForwardRules(overlay, bridge)
	for _, r := range rules {
		if !have(r) {
			c.run(append([]string{"-I", "FORWARD"}, r[1:]...)...)
		}
	}
	if err := EnsureRuleOrder(c.run, "FORWARD", rules); err != nil {
		t.Fatal(err)
	}
	for _, r := range legacyForwardRules() {
		for i, have := range c.rules {
			if ruleKey(have) == ruleKey(r[1:]) {
				c.rules = append(c.rules[:i], c.rules[i+1:]...)
				break
			}
		}
	}
	chain := c.chain()
	data, user, build := []string{netpolicy.ServiceData}, []string{netpolicy.ServiceUser}, []string{netpolicy.ServiceBuild}
	for _, tc := range []struct {
		name string
		p    packet
		want string
	}{
		{"data → controller", packet{src: data, dst: []string{netpolicy.ServiceSys, BuildServiceSet}, dstIP: "100.100.12.5", dport: "80"}, "DROP"},
		{"data → same-app kraft", packet{src: data, dst: data, pairs: []string{DataPeerSet}, dstIP: "100.100.14.2", dport: "9093"}, "ACCEPT"},
		{"data → other app kraft", packet{src: data, dst: data, dstIP: "100.100.14.2", dport: "9093"}, "DROP"},
		{"data → follower 5432", packet{src: data, dst: data, dstIP: "100.100.14.2", dport: "5432"}, "ACCEPT"},
		{"data → system postgres", packet{src: data, dst: []string{netpolicy.ServiceData, DataSystemSet}, dstIP: "100.100.14.9", dport: "5432"}, "DROP"},
		{"data → node discoverd", packet{src: data, dst: []string{NodeSet}, dstIP: "192.168.57.21", dport: "1111"}, "ACCEPT"},
		{"data → node router api", packet{src: data, dst: []string{NodeSet}, dstIP: "192.168.57.21", dport: "5000"}, "DROP"},
		{"user → datastore 5432", packet{src: user, dst: data, dstIP: "100.100.14.2", dport: "5432"}, "ACCEPT"},
		{"user → datastore admin", packet{src: user, dst: data, dstIP: "100.100.14.2", dport: "5433"}, "DROP"},
		{"user → dns", packet{src: user, dstIP: bridge, proto: "udp", dport: "53"}, "ACCEPT"},
		{"user → metadata", packet{src: user, dstIP: "169.254.169.254", dport: "80"}, "DROP"},
		{"build → blobstore", packet{src: build, dst: []string{netpolicy.ServiceSys, BuildServiceSet}, dstIP: "100.100.13.9", dport: "80"}, "ACCEPT"},
		{"build → user", packet{src: build, dst: user, dstIP: "100.100.14.3", dport: "8080"}, "DROP"},
		{"system → admin port", packet{src: []string{netpolicy.ServiceSys}, dst: data, dstIP: "100.100.14.2", dport: "5433"}, ""},
	} {
		if got := verdict(t, chain, tc.p); got != tc.want {
			t.Errorf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
	for _, r := range legacyForwardRules() {
		if have(r) {
			t.Fatalf("legacy rule left: %v", r)
		}
	}
}

// TestEgressDenyCoversDatastoreEngines (EDGE-67): datastore engines run
// code a tenant can steer to an address (ClickHouse URL/S3/MySQL table
// engines, extensions), so the egress deny list (link-local, which holds
// the cloud metadata service, plus FLYNN_JOB_EGRESS_DENY) covers them as
// it covers user and build jobs. Cluster node IPs stay with the EDGE-29
// rules even when an operator range contains them: engines must keep
// discoverd on every node, and every other node port is dropped there.
func TestEgressDenyCoversDatastoreEngines(t *testing.T) {
	cidrs, err := EgressDenyCIDRs("10.20.0.0/16,192.168.57.0/24")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeForward{}
	for _, r := range installed(JobIsolationForwardRules(testOverlay, testBridge)) {
		f.rules = append(f.rules, strings.Join(r[1:], " "))
	}
	f.rules = append(f.rules, "-i flynnbr0 ! -o flynnbr0 -j ACCEPT")
	if err := ApplyEgressDeny(f.run, cidrs); err != nil {
		t.Fatal(err)
	}
	chain := func() [][]string {
		var out [][]string
		for _, r := range f.rules {
			if r == "-i flynnbr0 ! -o flynnbr0 -j ACCEPT" {
				continue // the fall-through "" below stands for it
			}
			out = append(out, append([]string{"FORWARD"}, strings.Fields(r)...))
		}
		return out
	}
	data := []string{netpolicy.ServiceData}
	node := []string{NodeSet}
	check := func(when string) {
		t.Helper()
		for _, tc := range []struct {
			name string
			p    packet
			want string
		}{
			{"data → metadata service", packet{src: data, dstIP: "169.254.169.254", dport: "80"}, "DROP"},
			{"data → link-local udp", packet{src: data, dstIP: "169.254.10.1", proto: "udp", dport: "53"}, "DROP"},
			{"data → operator CIDR", packet{src: data, dstIP: "10.20.1.5", dport: "443"}, "DROP"},
			{"data → operator /24 off the node set", packet{src: data, dstIP: "192.168.57.99", dport: "9000"}, "DROP"},
			{"data → node discoverd inside an operator CIDR", packet{src: data, dst: node, dstIP: "192.168.57.21", dport: netpolicy.DiscoverdHTTPPort}, "ACCEPT"},
			{"data → node router api inside an operator CIDR", packet{src: data, dst: node, dstIP: "192.168.57.21", dport: "5000"}, "DROP"},
			{"data → same-app peer", packet{src: data, dst: data, pairs: []string{DataPeerSet}, dstIP: "100.64.14.2", dport: "5433"}, "ACCEPT"},
			{"data → cross-app follower", packet{src: data, dst: data, dstIP: "100.64.14.2", dport: "6379"}, "ACCEPT"},
			{"data → internet", packet{src: data, dstIP: "140.82.112.3", dport: "443"}, ""},
			{"user → metadata service", packet{src: []string{netpolicy.ServiceUser}, dstIP: "169.254.169.254", dport: "80"}, "DROP"},
			{"build → operator CIDR", packet{src: []string{netpolicy.ServiceBuild}, dstIP: "10.20.1.5", dport: "443"}, "DROP"},
			{"system → metadata service", packet{src: []string{netpolicy.ServiceSys}, dstIP: "169.254.169.254", dport: "80"}, ""},
			{"system → operator CIDR (backups to an operator S3 endpoint)", packet{src: []string{netpolicy.ServiceSys}, dstIP: "10.20.1.5", dport: "443"}, ""},
		} {
			if got := verdict(t, chain(), tc.p); got != tc.want {
				t.Errorf("%s: %s: got %q want %q\n%s", when, tc.name, got, tc.want, strings.Join(f.rules, "\n"))
			}
		}
	}
	check("fresh")

	// Idempotent.
	n := len(f.rules)
	if err := ApplyEgressDeny(f.run, cidrs); err != nil {
		t.Fatal(err)
	}
	if len(f.rules) != n {
		t.Fatalf("second apply changed the chain:\n%s", strings.Join(f.rules, "\n"))
	}

	// The operator drops a range: its data rule goes with the others.
	if err := ApplyEgressDeny(f.run, []string{"169.254.0.0/16", "192.168.57.0/24"}); err != nil {
		t.Fatal(err)
	}
	if f.index("10.20.0.0/16") >= 0 {
		t.Fatalf("stale deny rule kept:\n%s", strings.Join(f.rules, "\n"))
	}
	if got := verdict(t, chain(), packet{src: data, dstIP: "10.20.1.5", dport: "443"}); got != "" {
		t.Fatalf("data → removed operator CIDR: got %q want fall-through", got)
	}
	if got := verdict(t, chain(), packet{src: data, dstIP: "169.254.169.254", dport: "80"}); got != "DROP" {
		t.Fatalf("data → metadata service after removal: got %q want DROP", got)
	}
}

// TestEgressDenyUpgradeAddsDatastoreRules (EDGE-67): a host updated from a
// deny list that covered only user and build jobs keeps those rules and
// gains the datastore ones; nothing is deleted.
func TestEgressDenyUpgradeAddsDatastoreRules(t *testing.T) {
	f := &fakeForward{rules: []string{
		"-d 169.254.0.0/16 -m set --match-set flynn-net-build src -m comment --comment flynn-egress-deny -j DROP",
		"-d 169.254.0.0/16 -m set --match-set flynn-net-user src -m comment --comment flynn-egress-deny -j DROP",
		"-i flynnbr0 ! -o flynnbr0 -j ACCEPT",
	}}
	old := append([]string(nil), f.rules[:2]...)
	if err := ApplyEgressDeny(f.run, DefaultEgressDeny); err != nil {
		t.Fatal(err)
	}
	for _, r := range old {
		if f.index(r) < 0 {
			t.Fatalf("existing deny rule removed: %s\n%s", r, strings.Join(f.rules, "\n"))
		}
	}
	at := f.index(netpolicy.ServiceData+" src", "-d 169.254.0.0/16", "DROP")
	if at < 0 || at > f.index("-i flynnbr0", "ACCEPT") {
		t.Fatalf("datastore link-local DROP missing or after the bridge egress ACCEPT:\n%s", strings.Join(f.rules, "\n"))
	}
	if len(f.rules) != 4 {
		t.Fatalf("want 3 deny rules and the egress ACCEPT:\n%s", strings.Join(f.rules, "\n"))
	}
	if f.index(netpolicy.ServiceData+" src", "! --match-set "+NodeSet+" dst") != at {
		t.Fatalf("datastore deny rule must leave node IPs to the isolation rules:\n%s", strings.Join(f.rules, "\n"))
	}

	// A datastore deny rule without the node exemption (hand-added, or
	// from a build that lacked it) is replaced, not kept beside the new one.
	f.rules = append([]string{"-d 169.254.0.0/16 -m set --match-set flynn-net-data src -m comment --comment flynn-egress-deny -j DROP"}, old...)
	f.rules = append(f.rules, "-i flynnbr0 ! -o flynnbr0 -j ACCEPT")
	if err := ApplyEgressDeny(f.run, DefaultEgressDeny); err != nil {
		t.Fatal(err)
	}
	if len(f.rules) != 4 || f.index(netpolicy.ServiceData+" src", "! --match-set "+NodeSet+" dst") < 0 {
		t.Fatalf("unexempted datastore rule not replaced:\n%s", strings.Join(f.rules, "\n"))
	}
}
