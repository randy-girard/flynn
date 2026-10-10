package subnet

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/randy-girard/flynn/flannel/pkg/ip"
)

type testSubnet struct {
	attrs      []byte
	index      uint64
	expiration *time.Time
}

type mockSubnetRegistry struct {
	subnets map[string]*testSubnet
	addCh   chan string
	delCh   chan string
	index   uint64
	ttl     uint64
	mtx     sync.RWMutex
}

func newMockSubnetRegistry(ttlOverride uint64) *mockSubnetRegistry {
	return &mockSubnetRegistry{
		subnets: map[string]*testSubnet{
			"10.3.1.0-24": {attrs: []byte(`{ "PublicIP": "1.1.1.1" }`), index: 10},
			"10.3.2.0-24": {attrs: []byte(`{ "PublicIP": "1.1.1.1" }`), index: 11},
			"10.3.4.0-24": {attrs: []byte(`{ "PublicIP": "1.1.1.1" }`), index: 12},
			"10.3.5.0-24": {attrs: []byte(`{ "PublicIP": "1.1.1.1" }`), index: 13},
		},
		addCh: make(chan string),
		delCh: make(chan string),
		index: 14,
		ttl:   ttlOverride,
	}
}

func (msr *mockSubnetRegistry) GetConfig() ([]byte, error) {
	return []byte(`{ "Network": "10.3.0.0/16", "SubnetMin": "10.3.1.0", "SubnetMax": "10.3.5.0" }`), nil
}

func (msr *mockSubnetRegistry) GetSubnets() (*Response, error) {
	msr.mtx.RLock()
	defer msr.mtx.RUnlock()
	res := &Response{
		Subnets: make(map[string][]byte, len(msr.subnets)),
		Index:   msr.index,
	}
	for subnet, t := range msr.subnets {
		res.Subnets[subnet] = t.attrs
	}
	return res, nil
}

func (msr *mockSubnetRegistry) CreateSubnet(sn, data string, ttl uint64) (*Response, error) {
	msr.mtx.Lock()
	defer msr.mtx.Unlock()
	msr.index += 1

	if msr.ttl > 0 {
		ttl = msr.ttl
	}

	msr.subnets[sn] = &testSubnet{attrs: []byte(data), index: msr.index}

	// add squared durations :)
	exp := time.Now().Add(time.Duration(ttl) * time.Second)

	return &Response{
		Index:      msr.index,
		Expiration: &exp,
	}, nil
}

func (msr *mockSubnetRegistry) UpdateSubnet(sn, data string, ttl uint64) (*Response, error) {
	msr.mtx.Lock()
	defer msr.mtx.Unlock()
	msr.index += 1

	// add squared durations :)
	exp := time.Now().Add(time.Duration(ttl) * time.Second)

	for subnet, t := range msr.subnets {
		if subnet == sn {
			t.attrs = []byte(data)
			t.index = msr.index
			t.expiration = &exp

			return &Response{
				Index:      msr.index,
				Expiration: &exp,
			}, nil
		}
	}

	return nil, fmt.Errorf("Subnet not found")

}

func (msr *mockSubnetRegistry) WatchSubnets(since uint64, stop chan bool) (*Response, error) {
	var sn string
	msr.mtx.Lock()
	defer msr.mtx.Unlock()

	select {
	case <-stop:
		return nil, nil

	case sn = <-msr.addCh:
		t := &testSubnet{
			attrs: []byte(`{"PublicIP": "1.1.1.1"}`),
			index: msr.index,
		}
		msr.subnets[sn] = t
		return &Response{
			Subnets: map[string][]byte{sn: t.attrs},
			Action:  "add",
			Index:   msr.index,
		}, nil

	case sn = <-msr.delCh:
		for subnet, t := range msr.subnets {
			if subnet == sn {
				delete(msr.subnets, subnet)
				return &Response{
					Subnets: map[string][]byte{subnet: t.attrs},
					Action:  "expire",
					Index:   msr.index,
				}, nil
			}
		}
		return nil, fmt.Errorf("Subnet (%s) to delete was not found: ", sn)
	}
}

func TestAcquireLease(t *testing.T) {
	msr := newMockSubnetRegistry(0)
	sm, err := NewSubnetManager(msr)
	if err != nil {
		t.Fatalf("Failed to create subnet manager: %s", err)
	}

	extIP, _ := ip.ParseIP4("1.2.3.4")
	attrs := LeaseAttrs{
		PublicIP: extIP,
	}

	cancel := make(chan bool)
	sn, err := sm.AcquireLease(&attrs, cancel)
	if err != nil {
		t.Fatal("AcquireLease failed: ", err)
	}

	if sn.String() != "10.3.3.0/24" {
		t.Fatal("Subnet mismatch: expected 10.3.3.0/24, got: ", sn)
	}

	// Acquire again, should reuse
	if sn, err = sm.AcquireLease(&attrs, cancel); err != nil {
		t.Fatal("AcquireLease failed: ", err)
	}

	if sn.String() != "10.3.3.0/24" {
		t.Fatal("Subnet mismatch: expected 10.3.3.0/24, got: ", sn)
	}
}

func TestWatchLeaseAdded(t *testing.T) {
	msr := newMockSubnetRegistry(0)
	sm, err := NewSubnetManager(msr)
	if err != nil {
		t.Fatalf("Failed to create subnet manager: %s", err)
	}

	events := make(chan EventBatch)
	cancel := make(chan bool)
	go sm.WatchLeases(events, cancel)

	expected := "10.3.3.0-24"
	msr.addCh <- expected

	evtBatch, ok := <-events
	if !ok {
		t.Fatalf("WatchSubnets did not publish")
	}

	if len(evtBatch) != 1 {
		t.Fatalf("WatchSubnets produced wrong sized event batch")
	}

	evt := evtBatch[0]

	if evt.Type != SubnetAdded {
		t.Fatalf("WatchSubnets produced wrong event type")
	}

	actual := evt.Lease.Network.StringSep(".", "-")
	if actual != expected {
		t.Errorf("WatchSubnet produced wrong subnet: expected %s, got %s", expected, actual)
	}

	close(cancel)
}

func TestWatchLeaseRemoved(t *testing.T) {
	msr := newMockSubnetRegistry(0)
	sm, err := NewSubnetManager(msr)
	if err != nil {
		t.Fatalf("Failed to create subnet manager: %s", err)
	}

	events := make(chan EventBatch)
	cancel := make(chan bool)
	go sm.WatchLeases(events, cancel)

	expected := "10.3.4.0-24"
	msr.delCh <- expected

	evtBatch, ok := <-events
	if !ok {
		t.Fatalf("WatchSubnets did not publish")
	}

	if len(evtBatch) != 1 {
		t.Fatalf("WatchSubnets produced wrong sized event batch")
	}

	evt := evtBatch[0]

	if evt.Type != SubnetRemoved {
		t.Fatalf("WatchSubnets produced wrong event type")
	}

	actual := evt.Lease.Network.StringSep(".", "-")
	if actual != expected {
		t.Errorf("WatchSubnet produced wrong subnet: expected %s, got %s", expected, actual)
	}

	close(cancel)
}

type leaseData struct {
	Dummy string
}

func TestRenewLease(t *testing.T) {
	msr := newMockSubnetRegistry(1)
	sm, err := NewSubnetManager(msr)
	if err != nil {
		t.Fatalf("Failed to create subnet manager: %v", err)
	}

	// Create LeaseAttrs
	extIP, _ := ip.ParseIP4("1.2.3.4")
	attrs := LeaseAttrs{
		PublicIP:    extIP,
		BackendType: "vxlan",
	}

	ld, err := json.Marshal(&leaseData{Dummy: "test string"})
	if err != nil {
		t.Fatalf("Failed to marshal leaseData: %v", err)
	}
	attrs.BackendData = json.RawMessage(ld)

	// Acquire lease
	cancel := make(chan bool)
	defer close(cancel)

	sn, err := sm.AcquireLease(&attrs, cancel)
	if err != nil {
		t.Fatal("AcquireLease failed: ", err)
	}

	go sm.LeaseRenewer(cancel)

	fmt.Println("Waiting for lease to pass original expiration")
	time.Sleep(2 * time.Second)

	// check that it's still good
	msr.mtx.RLock()
	defer msr.mtx.RUnlock()
	for subnet, v := range msr.subnets {
		if subnet == sn.StringSep(".", "-") {
			if v.expiration.Before(time.Now()) {
				t.Error("Failed to renew lease: expiration did not advance")
			}
			a := LeaseAttrs{}
			if err := json.Unmarshal(v.attrs, &a); err != nil {
				t.Errorf("Failed to JSON-decode LeaseAttrs: %v", err)
				return
			}
			if !reflect.DeepEqual(a, attrs) {
				t.Errorf("LeaseAttrs changed: was %#v, now %#v", attrs, a)
			}
			return
		}
	}

	t.Fatalf("Failed to find acquired lease")
}

func TestParseSubnetWatchResponseEmitsEveryPeer(t *testing.T) {
	self, err := parseSubnetKey("100.100.15.0-24")
	if err != nil {
		t.Fatal(err)
	}
	sm := &SubnetManager{myLease: SubnetLease{Network: self}}
	batch, err := sm.parseSubnetWatchResponse(&Response{
		Index: 9,
		Subnets: map[string][]byte{
			"100.100.15.0-24": []byte(`{"PublicIP":"192.168.57.20"}`),
			"100.100.31.0-24": []byte(`{"PublicIP":"192.168.57.21"}`),
			"100.100.70.0-24": []byte(`{"PublicIP":"192.168.57.22"}`),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if batch == nil || len(*batch) != 2 {
		t.Fatalf("got %v, want both peer leases (Watch used to keep only the last event)", batch)
	}
	seen := map[string]bool{}
	for _, evt := range *batch {
		if evt.Type != SubnetAdded {
			t.Fatalf("type=%v", evt.Type)
		}
		seen[evt.Lease.Network.StringSep(".", "-")] = true
	}
	if !seen["100.100.31.0-24"] || !seen["100.100.70.0-24"] {
		t.Fatalf("peers=%v", seen)
	}
	if seen["100.100.15.0-24"] {
		t.Fatal("own lease must be skipped")
	}
}

func TestPeerLeasesOmitsSelf(t *testing.T) {
	self, err := parseSubnetKey("100.100.15.0-24")
	if err != nil {
		t.Fatal(err)
	}
	peer, err := parseSubnetKey("100.100.31.0-24")
	if err != nil {
		t.Fatal(err)
	}
	sm := &SubnetManager{
		myLease: SubnetLease{Network: self},
		leases: []SubnetLease{
			{Network: self},
			{Network: peer},
		},
	}
	got := sm.PeerLeases()
	if len(got) != 1 || !got[0].Network.Equal(peer) {
		t.Fatalf("PeerLeases=%v, want only the other host", got)
	}
}

func TestLeaseRepublishWaitCapsDiscoverdPlaceholderExpiry(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		exp  time.Time
		want time.Duration
	}{
		{"discoverd 10-year placeholder", now.AddDate(10, 0, 0), discoverdRepublishInterval},
		{"2h etcd-style remaining", now.Add(2 * time.Hour), discoverdRepublishInterval},
		{"already due", now.Add(2 * time.Second), time.Second},
	}
	for _, tc := range cases {
		if got := leaseRepublishWait(tc.exp, now); got != tc.want {
			t.Fatalf("%s: wait = %s, want %s", tc.name, got, tc.want)
		}
	}
}

// blockingUpdateRegistry parks every UpdateSubnet until release is closed so
// a test can observe what else can run while the renewer is mid-request.
type blockingUpdateRegistry struct {
	*mockSubnetRegistry
	entered chan struct{}
	release chan struct{}
}

func (r *blockingUpdateRegistry) UpdateSubnet(sn, data string, ttl uint64) (*Response, error) {
	select {
	case r.entered <- struct{}{}:
	default:
	}
	<-r.release
	return r.mockSubnetRegistry.UpdateSubnet(sn, data, ttl)
}

func TestLeaseRenewerDoesNotHoldLockDuringUpdate(t *testing.T) {
	reg := &blockingUpdateRegistry{
		mockSubnetRegistry: newMockSubnetRegistry(0),
		entered:            make(chan struct{}, 1),
		release:            make(chan struct{}),
	}
	defer close(reg.release)
	sm, err := NewSubnetManager(reg)
	if err != nil {
		t.Fatal(err)
	}
	extIP, _ := ip.ParseIP4("1.2.3.4")
	cancel := make(chan bool)
	defer close(cancel)
	if _, err := sm.AcquireLease(&LeaseAttrs{PublicIP: extIP}, cancel); err != nil {
		t.Fatal(err)
	}
	sm.leaseExp = time.Now()
	go sm.LeaseRenewer(cancel)

	select {
	case <-reg.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("renewer never called UpdateSubnet")
	}
	if sm.RenewerAlive().IsZero() {
		t.Fatal("RenewerAlive must be set once the renewer runs")
	}

	peer, _ := parseSubnetKey("10.3.9.0-24")
	done := make(chan struct{})
	go func() {
		if _, err := sm.applySubnetChange("add", peer, []byte(`{"PublicIP":"5.6.7.8"}`)); err != nil {
			t.Error(err)
		}
		sm.PeerLeases()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("applySubnetChange/PeerLeases blocked while the renewer was in UpdateSubnet; a slow registry would stall the VXLAN loop")
	}
}

func TestApplySubnetChangeDeleteRemovesLease(t *testing.T) {
	a, _ := parseSubnetKey("10.3.1.0-24")
	b, _ := parseSubnetKey("10.3.2.0-24")
	c, _ := parseSubnetKey("10.3.3.0-24")
	sm := &SubnetManager{leases: []SubnetLease{{Network: a}, {Network: b}, {Network: c}}}
	evt, err := sm.applySubnetChange("expire", b, nil)
	if err != nil {
		t.Fatal(err)
	}
	if evt.Type != SubnetRemoved || !evt.Lease.Network.Equal(b) {
		t.Fatalf("event=%v", evt)
	}
	got := sm.Leases()
	if len(got) != 2 {
		t.Fatalf("leases=%v, want 2 after delete (result of deleteLease was discarded)", got)
	}
	for _, l := range got {
		if l.Network.Equal(b) {
			t.Fatalf("deleted lease still present: %v", got)
		}
	}
}
