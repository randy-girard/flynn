package overlay

import (
	"errors"
	"strings"
	"testing"

	hh "github.com/randy-girard/flynn/pkg/httphelper"
)

func defaultMeta() []byte {
	return []byte(`{"config":{"Network":"100.100.0.0/16","Backend":{"Type":"vxlan"}},"subnets":{"100.100.1.0-24":{"PublicIP":"192.168.57.20","BackendType":"vxlan"},"100.100.3.0-24":{"PublicIP":"192.168.57.22"},"100.100.2.0-24":{"PublicIP":"192.168.57.21"}}}`)
}

func hosts3() []Host {
	return []Host{
		{ID: "node-1", Subnet: "100.100.1.1/24", Underlay: "192.168.57.20:1113"},
		{ID: "node-2", Subnet: "100.100.2.1/24", Underlay: "192.168.57.21:1113"},
		{ID: "node-3", Subnet: "100.100.3.1/24", Underlay: "192.168.57.22:1113"},
	}
}

func TestPlanKeepsDefaultCapacityAndLeases(t *testing.T) {
	change, err := Plan(defaultMeta(), "100.100.0.0/16", 0, []string{"192.168.57.20"})
	if err != nil {
		t.Fatal(err)
	}
	if change.Changed || change.Phase != PhaseNone {
		t.Fatalf("repeat must be a no-op: changed=%v phase=%s", change.Changed, change.Phase)
	}
	if change.Next.CIDR != "100.100.0.0/16" || change.Next.SubnetLen != 24 {
		t.Fatalf("next = %+v", change.Next)
	}
	if change.Capacity.HostSubnets != 255 || change.Capacity.Allocated != 3 || change.Capacity.Free != 252 {
		t.Fatalf("capacity = %+v", change.Capacity)
	}
	if len(change.Leases) != 3 || change.Leases[0].CIDR != "100.100.1.0/24" {
		t.Fatalf("leases = %+v", change.Leases)
	}
}

func TestPlanExpandsSupernetAndPreservesLeases(t *testing.T) {
	change, err := Plan(defaultMeta(), "100.64.0.0/10", 0, []string{"192.168.57.20:1113"})
	if err != nil {
		t.Fatal(err)
	}
	if !change.Changed || change.Phase != PhaseHostsThenMeta {
		t.Fatalf("expand phase = %s changed=%v", change.Phase, change.Changed)
	}
	if change.Next.CIDR != "100.64.0.0/10" || change.Next.SubnetLen != 24 {
		t.Fatalf("next = %+v", change.Next)
	}
	if change.Capacity.HostSubnets != 16383 || change.Capacity.Allocated != 3 || change.Capacity.Free != 16380 {
		t.Fatalf("capacity = %+v", change.Capacity)
	}
	out, err := ApplyMeta(defaultMeta(), change.Next)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"Network":"100.64.0.0/10"`,
		`"SubnetLen":24`,
		`"Type":"vxlan"`,
		`"100.100.1.0-24"`,
		`"100.100.2.0-24"`,
		`"100.100.3.0-24"`,
		`"PublicIP":"192.168.57.20"`,
	} {
		if !strings.Contains(string(out), want) {
			t.Fatalf("merged meta missing %s:\n%s", want, out)
		}
	}
}

func TestPlanRejectsOrphanShrinkUnderlayAndMove(t *testing.T) {
	// 100.100.200.0/24 sits in the upper half of the /16 and is outside /17.
	high := []byte(`{"config":{"Network":"100.100.0.0/16","SubnetLen":24},"subnets":{"100.100.200.0-24":{"PublicIP":"192.168.57.30"}}}`)
	if _, err := Plan(high, "100.100.0.0/17", 0, nil); err == nil || !strings.Contains(err.Error(), "100.100.200.0/24") {
		t.Fatalf("shrink err = %v", err)
	}
	if _, err := Plan(defaultMeta(), "192.168.57.0/24", 0, []string{"192.168.57.20"}); err == nil || !strings.Contains(err.Error(), "192.168.57.20") {
		t.Fatalf("underlay err = %v", err)
	}
	if _, err := Plan(defaultMeta(), "11.0.0.0/16", 0, nil); err == nil || !strings.Contains(err.Error(), "11.0.0.0/16") {
		t.Fatalf("range err = %v", err)
	}
	if _, err := Plan(defaultMeta(), "100.64.0.0/7", 0, nil); err == nil || !strings.Contains(err.Error(), "/7") {
		t.Fatalf("prefix err = %v", err)
	}
	empty := []byte(`{"config":{"Network":"100.100.0.0/16","Backend":{"Type":"vxlan"}},"subnets":{}}`)
	if _, err := Plan(empty, "10.1.0.0/16", 0, nil); err == nil || !strings.Contains(err.Error(), "move") {
		t.Fatalf("move err = %v", err)
	}
	if _, err := Plan(defaultMeta(), "100.100.0.0/16", 32, nil); err == nil {
		t.Fatal("subnet length 32 must be rejected")
	}
}

func TestPlanShrinkThatKeepsLeases(t *testing.T) {
	meta := []byte(`{"config":{"Network":"100.100.0.0/16","SubnetLen":24},"subnets":{"100.100.1.0-24":{"PublicIP":"192.168.57.20"}}}`)
	change, err := Plan(meta, "100.100.0.0/17", 24, []string{"192.168.57.20"})
	if err != nil {
		t.Fatal(err)
	}
	if change.Phase != PhaseMetaThenHosts || !change.Changed {
		t.Fatalf("phase=%s changed=%v", change.Phase, change.Changed)
	}
	if change.Next.CIDR != "100.100.0.0/17" {
		t.Fatalf("next = %s", change.Next.CIDR)
	}
}

func TestPlanSubnetLenOnly(t *testing.T) {
	change, err := Plan(defaultMeta(), "100.100.0.0/16", 25, []string{"192.168.57.20"})
	if err != nil {
		t.Fatal(err)
	}
	if !change.Changed || change.Phase != PhaseMetaThenHosts || change.Next.SubnetLen != 25 {
		t.Fatalf("%+v", change)
	}
	if len(change.Leases) != 3 {
		t.Fatalf("leases changed: %+v", change.Leases)
	}
}

func TestWrappedMetaAndHostMatch(t *testing.T) {
	wrapped := []byte(`"{\"config\":{\"Network\":\"100.100.0.0/16\"},\"subnets\":{\"100.100.9.0-24\":{\"PublicIP\":\"192.0.2.9\"}}}"`)
	change, err := Inspect(wrapped)
	if err != nil {
		t.Fatal(err)
	}
	if change.Current.CIDR != "100.100.0.0/16" || change.Capacity.Allocated != 1 {
		t.Fatalf("%+v", change)
	}
	if !HostMatches("100.100.0.0/16", "100.100.9.1/24", "") {
		t.Fatal("inferred /16 must match the default overlay")
	}
	if HostMatches("100.64.0.0/10", "100.100.9.1/24", "") {
		t.Fatal("inferred /16 must not count as a /10")
	}
	if !HostMatches("100.64.0.0/10", "100.100.9.1/24", "100.64.0.1/10") {
		t.Fatal("stored overlay must match the live CIDR")
	}
}

func TestLinkRoutePlan(t *testing.T) {
	add, del := LinkRoutePlan("", "100.100.9.1/24", "100.64.0.0/10")
	if add != "100.64.0.0/10" || del != "" {
		t.Fatalf("expand add=%s del=%s", add, del)
	}
	add, del = LinkRoutePlan("100.64.0.0/10", "100.100.9.1/24", "100.100.0.0/16")
	if add != "100.100.0.0/16" || del != "100.64.0.0/10" {
		t.Fatalf("shrink add=%s del=%s", add, del)
	}
	add, del = LinkRoutePlan("100.64.0.0/10", "100.100.9.1/24", "100.64.0.0/10")
	if add != "100.64.0.0/10" || del != "" {
		t.Fatalf("same add=%s del=%s", add, del)
	}
}

func TestShowReportsCapacityAndBehindHost(t *testing.T) {
	change, err := Inspect(defaultMeta())
	if err != nil {
		t.Fatal(err)
	}
	// Pretend the live network was expanded; show uses Inspect on real meta.
	expanded, err := Plan(defaultMeta(), "100.64.0.0/10", 24, []string{"192.168.57.20"})
	if err != nil {
		t.Fatal(err)
	}
	text := FormatShow(expanded, []HostView{
		{ID: "node-1", Subnet: "100.100.1.1/24", Overlay: "100.64.0.0/10"},
		{ID: "node-2", Subnet: "100.100.2.1/24", Overlay: ""},
	})
	for _, want := range []string{
		"network=100.64.0.0/10 subnet_len=24",
		"allocated=3 free=16380 host_subnets=16383",
		"100.100.1.0/24 192.168.57.20",
		"node-1 applied=100.64.0.0/10",
		"node-2 applied=100.100.0.0/16 behind",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("show missing %q:\n%s", want, text)
		}
	}
	same := FormatShow(change, []HostView{{ID: "node-1", Subnet: "100.100.1.1/24"}})
	if strings.Contains(same, "behind") {
		t.Fatalf("default host must not look behind:\n%s", same)
	}
	if !strings.Contains(same, "host_subnets=255") {
		t.Fatalf("default show:\n%s", same)
	}
}

type memStore struct {
	data  []byte
	index uint64
	sets  int
}

func (m *memStore) Get() ([]byte, uint64, error) {
	return append([]byte(nil), m.data...), m.index, nil
}

func (m *memStore) Set(data []byte, index uint64) error {
	m.sets++
	if index != m.index {
		return errors.New("bad index")
	}
	m.data = append([]byte(nil), data...)
	m.index++
	return nil
}

type raceStore struct {
	reads      int
	index      uint64
	first      []byte
	second     []byte
	wrote      []byte
	failWrites int
}

func (r *raceStore) Get() ([]byte, uint64, error) {
	r.reads++
	if r.reads == 1 {
		return r.first, r.index, nil
	}
	return r.second, r.index, nil
}

func (r *raceStore) Set(data []byte, index uint64) error {
	if index != r.index {
		return errors.New("bad index")
	}
	if r.failWrites > 0 {
		r.failWrites--
		return hh.PreconditionFailedErr("flannel metadata index")
	}
	r.wrote = append([]byte(nil), data...)
	r.index++
	return nil
}

func TestCommitRetriesIndexAndKeepsNewLease(t *testing.T) {
	first := defaultMeta()
	second := []byte(`{"config":{"Network":"100.100.0.0/16","Backend":{"Type":"vxlan"}},"subnets":{"100.100.1.0-24":{"PublicIP":"192.168.57.20"},"100.100.2.0-24":{"PublicIP":"192.168.57.21"},"100.100.3.0-24":{"PublicIP":"192.168.57.22"},"100.100.4.0-24":{"PublicIP":"192.168.57.23"}}}`)
	store := &raceStore{first: first, second: second, index: 7, failWrites: 1}
	if err := Commit(store, "100.64.0.0/10", 24, []string{"192.168.57.20"}); err != nil {
		t.Fatal(err)
	}
	if store.reads < 2 {
		t.Fatalf("reads = %d", store.reads)
	}
	body := string(store.wrote)
	if !strings.Contains(body, "100.64.0.0/10") || !strings.Contains(body, "100.100.4.0-24") || !strings.Contains(body, "100.100.1.0-24") {
		t.Fatalf("write lost a lease or the network:\n%s", body)
	}
}

func TestExecuteWidenAppliesBeforeMeta(t *testing.T) {
	store := &memStore{data: defaultMeta(), index: 4}
	var order []string
	err := error(nil)
	_, err = Execute(store, hosts3(), func(id, network string) error {
		order = append(order, "apply:"+id+":"+network)
		if id == "node-2" {
			return errors.New("flannel.1: no such device")
		}
		return nil
	}, "100.64.0.0/10", 0)
	if err == nil || !strings.Contains(err.Error(), "node-2") || !strings.Contains(err.Error(), "metadata was not changed") || !strings.Contains(err.Error(), "re-run") {
		t.Fatalf("err = %v", err)
	}
	if store.sets != 0 {
		t.Fatalf("metadata written on failed widen: %d", store.sets)
	}
	if len(order) != 3 || !strings.HasPrefix(order[0], "apply:") {
		t.Fatalf("order = %v", order)
	}

	order = nil
	store = &memStore{data: defaultMeta(), index: 4}
	out, err := Execute(store, hosts3(), func(id, network string) error {
		order = append(order, "apply:"+id)
		if network != "100.64.0.0/10" {
			t.Fatalf("network %s", network)
		}
		return nil
	}, "100.64.0.0/10", 24)
	if err != nil {
		t.Fatal(err)
	}
	if store.sets != 1 || !strings.Contains(string(store.data), "100.64.0.0/10") || !strings.Contains(string(store.data), "100.100.2.0-24") {
		t.Fatalf("stored sets=%d\n%s", store.sets, store.data)
	}
	if len(order) != 3 {
		t.Fatalf("order = %v", order)
	}
	if !strings.Contains(out, "100.64.0.0/10") || !strings.Contains(out, "host_subnets=16383") {
		t.Fatalf("out = %s", out)
	}
}

func TestExecuteShrinkWritesMetaFirst(t *testing.T) {
	meta := []byte(`{"config":{"Network":"100.100.0.0/16","Backend":{"Type":"vxlan"}},"subnets":{"100.100.1.0-24":{"PublicIP":"192.168.57.20"}}}`)
	store := &memStore{data: meta, index: 3}
	var order []string
	hosts := []Host{{ID: "node-1", Subnet: "100.100.1.1/24", Underlay: "192.168.57.20:1113", Overlay: "100.100.0.0/16"}}
	_, err := Execute(store, hosts, func(id, network string) error {
		order = append(order, "apply:"+network)
		return errors.New("iptables failed")
	}, "100.100.0.0/17", 24)
	if err == nil || !strings.Contains(err.Error(), "metadata is 100.100.0.0/17") || !strings.Contains(err.Error(), "re-run") || !strings.Contains(err.Error(), "shrink") && !strings.Contains(err.Error(), "leases") {
		t.Fatalf("err = %v", err)
	}
	if store.sets != 1 || !strings.Contains(string(store.data), "100.100.0.0/17") {
		t.Fatalf("meta not written before apply failure:\n%s", store.data)
	}
	if len(order) != 1 {
		t.Fatalf("apply order = %v", order)
	}
}

func TestExecuteRepeatIsNoOpUnlessHostBehind(t *testing.T) {
	store := &memStore{data: defaultMeta(), index: 9}
	hosts := hosts3()
	out, err := Execute(store, hosts, func(id, network string) error {
		t.Fatalf("apply %s %s", id, network)
		return nil
	}, "100.100.0.0/16", 0)
	if err != nil {
		t.Fatal(err)
	}
	if store.sets != 0 || !strings.Contains(out, "unchanged") {
		t.Fatalf("sets=%d out=%s", store.sets, out)
	}

	hosts[1].Subnet = "100.100.2.1/24"
	// Same metadata, but report this host as already storing a stale narrower overlay.
	hosts[1].Overlay = "100.100.0.0/17"
	var applied []string
	out, err = Execute(store, hosts, func(id, network string) error {
		applied = append(applied, id+":"+network)
		return nil
	}, "100.100.0.0/16", 24)
	if err != nil {
		t.Fatal(err)
	}
	if store.sets != 0 {
		t.Fatal("reconcile must not rewrite metadata")
	}
	if len(applied) != 1 || applied[0] != "node-2:100.100.0.0/16" {
		t.Fatalf("applied = %v", applied)
	}
	if !strings.Contains(out, "node-2") {
		t.Fatalf("out = %s", out)
	}
}
