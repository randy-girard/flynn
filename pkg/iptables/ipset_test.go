package iptables

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIPSetBinMissingFromPATH(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := ipsetBin()
	if err == nil {
		t.Fatal("expected error when ipset is not on PATH")
	}
	if !strings.Contains(err.Error(), "ipset not found") {
		t.Fatalf("got %v", err)
	}
}

func TestUnionIPsDedupesAndSkipsNil(t *testing.T) {
	a := net.ParseIP("100.64.0.1")
	b := net.ParseIP("100.64.0.2")
	got := UnionIPs([]net.IP{a, a, nil}, []net.IP{b, a})
	if len(got) != 2 {
		t.Fatalf("%v", got)
	}
	if AddSetIP("flynn-net-user", nil) != nil {
		t.Fatal("nil IP add is a no-op")
	}
	if DelSetIP("flynn-net-user", nil) != nil {
		t.Fatal("nil IP del is a no-op")
	}
	if bytesPreview(nil) != "" {
		t.Fatal("empty preview")
	}
	long := strings.Repeat("x", 250)
	if got := bytesPreview([]byte("  " + long)); len(got) != 200 {
		t.Fatalf("preview len=%d", len(got))
	}
}

func TestEnsureSetsFailsClosedWithoutIPSet(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	err := EnsureSets("flynn-net-user")
	if err == nil {
		t.Fatal("EnsureSets must fail when ipset is missing (isolation cannot fail open)")
	}
	if !strings.Contains(err.Error(), "ipset not found") {
		t.Fatalf("got %v", err)
	}
}

func TestEnableJobIsolationPropagatesEnsureSetsError(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("iptables.go"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	iso := body
	if i := strings.Index(body, "func EnableJobIsolation"); i >= 0 {
		iso = body[i:]
		if j := strings.Index(iso[1:], "\nfunc "); j >= 0 {
			iso = iso[:j+1]
		}
	}
	if !strings.Contains(iso, "if err := EnsureSets(") {
		t.Fatal("EnableJobIsolation must call EnsureSets")
	}
	if !strings.Contains(iso, "return err") {
		t.Fatal("EnableJobIsolation must return EnsureSets errors (fail closed)")
	}
}

// fakeIPSetKernel models the kernel's named sets so a test can check what the
// live set holds after every command a ReplaceSetIPs call issues.
type fakeIPSetKernel struct {
	t       *testing.T
	sets    map[string]map[string]bool
	live    string
	mustHas []string // IPs that are in both the old and new contents
	calls   []string
}

func (k *fakeIPSetKernel) checkLive(after string) {
	set, ok := k.sets[k.live]
	if !ok {
		k.t.Fatalf("live set %s missing after %q", k.live, after)
	}
	for _, ip := range k.mustHas {
		if !set[ip] {
			k.t.Fatalf("live set %s lost %s after %q: isolation is off for that IP", k.live, ip, after)
		}
	}
}

func (k *fakeIPSetKernel) apply(fields []string) error {
	if len(fields) == 0 {
		return nil
	}
	switch fields[0] {
	case "-exist":
		return k.apply(fields[1:])
	case "create":
		if _, ok := k.sets[fields[1]]; !ok {
			k.sets[fields[1]] = map[string]bool{}
		}
	case "flush":
		k.sets[fields[1]] = map[string]bool{}
	case "add":
		s, ok := k.sets[fields[1]]
		if !ok {
			return fmt.Errorf("set %s does not exist", fields[1])
		}
		s[fields[2]] = true
	case "del":
		delete(k.sets[fields[1]], fields[2])
	case "swap":
		a, aok := k.sets[fields[1]]
		b, bok := k.sets[fields[2]]
		if !aok || !bok {
			return fmt.Errorf("swap needs both sets")
		}
		k.sets[fields[1]], k.sets[fields[2]] = b, a
	case "destroy":
		delete(k.sets, fields[1])
	default:
		return fmt.Errorf("unknown ipset command %q", fields[0])
	}
	k.checkLive(strings.Join(fields, " "))
	return nil
}

func (k *fakeIPSetKernel) run(stdin []byte, args ...string) ([]byte, error) {
	k.calls = append(k.calls, strings.Join(args, " "))
	rest := args
	for len(rest) > 0 && rest[0] == "-exist" {
		rest = rest[1:]
	}
	if len(rest) > 0 && rest[0] == "restore" {
		for _, line := range strings.Split(string(stdin), "\n") {
			if f := strings.Fields(line); len(f) > 0 {
				if err := k.apply(f); err != nil {
					return nil, err
				}
			}
		}
		return nil, nil
	}
	return nil, k.apply(args)
}

func TestReplaceSetIPsIsAtomic(t *testing.T) {
	k := &fakeIPSetKernel{
		t:       t,
		live:    "flynn-net-user",
		sets:    map[string]map[string]bool{"flynn-net-user": {"100.64.0.1": true, "100.64.0.2": true, "100.64.0.9": true}},
		mustHas: []string{"100.64.0.1", "100.64.0.2"},
	}
	restore := stubIPSetExec(t, k.run)
	defer restore()

	next := []net.IP{net.ParseIP("100.64.0.1"), net.ParseIP("100.64.0.2"), net.ParseIP("100.64.0.3"), nil}
	if err := ReplaceSetIPs("flynn-net-user", next); err != nil {
		t.Fatal(err)
	}
	for _, c := range k.calls {
		if strings.Contains(c, "flush flynn-net-user") && !strings.Contains(c, "restore") {
			t.Fatalf("ReplaceSetIPs flushed the live set: %v", k.calls)
		}
		if strings.HasPrefix(c, "add flynn-net-user ") {
			t.Fatalf("ReplaceSetIPs added to the live set one IP at a time: %v", k.calls)
		}
	}
	restores := 0
	for _, c := range k.calls {
		if strings.Contains(c, "restore") {
			restores++
		}
	}
	if restores != 1 {
		t.Fatalf("want one ipset restore, got %d: %v", restores, k.calls)
	}
	got := k.sets["flynn-net-user"]
	if len(got) != 3 || !got["100.64.0.3"] || got["100.64.0.9"] {
		t.Fatalf("live set = %v, want exactly .1 .2 .3", got)
	}
	if len(k.sets) != 1 {
		t.Fatalf("temporary set left behind: %v", k.sets)
	}
}

func TestReplaceSetIPsLeavesLiveSetOnRestoreError(t *testing.T) {
	k := &fakeIPSetKernel{
		t:       t,
		live:    "flynn-net-data",
		sets:    map[string]map[string]bool{"flynn-net-data": {"100.64.0.5": true}},
		mustHas: []string{"100.64.0.5"},
	}
	restore := stubIPSetExec(t, func(stdin []byte, args ...string) ([]byte, error) {
		if strings.Contains(strings.Join(args, " "), "restore") {
			return []byte("ipset v7.19: Error in line 3"), fmt.Errorf("exit status 1")
		}
		return k.run(stdin, args...)
	})
	defer restore()
	if err := ReplaceSetIPs("flynn-net-data", []net.IP{net.ParseIP("100.64.0.6")}); err == nil {
		t.Fatal("expected restore error")
	}
	if !k.sets["flynn-net-data"]["100.64.0.5"] {
		t.Fatalf("live set changed on a failed restore: %v", k.sets)
	}
}

func TestReplaceSetIPsTempNameFitsIPSetLimit(t *testing.T) {
	for _, name := range append(IsolationSets(), NodeSet) {
		if n := len(tempSetName(name)); n > 31 {
			t.Fatalf("temp set for %s is %d chars; ipset names are at most 31", name, n)
		}
		if tempSetName(name) == name {
			t.Fatalf("temp set must differ from %s", name)
		}
	}
}

func stubIPSetExec(t *testing.T, fn func(stdin []byte, args ...string) ([]byte, error)) func() {
	t.Helper()
	old := ipsetExec
	ipsetExec = fn
	return func() { ipsetExec = old }
}
