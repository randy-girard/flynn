package discoverd

import (
	"reflect"
	"testing"
	"time"

	"github.com/randy-girard/flynn/discoverd/client"
	"github.com/randy-girard/flynn/discoverd/testutil"
	"github.com/randy-girard/flynn/flannel/subnet"
)

type test struct {
	*testing.T

	client   *discoverd.Client
	service  discoverd.Service
	events   chan *discoverd.Event
	registry subnet.Registry

	cleanup func()
}

func newTest(t *testing.T) (s *test) {
	defer func() {
		if t.Failed() {
			if s != nil && s.cleanup != nil {
				s.cleanup()
			}
			t.FailNow()
		}
	}()

	client, killDiscoverd := testutil.BootDiscoverd(t, "")

	serviceName := "flannel-test"
	if err := client.AddService(serviceName, nil); err != nil {
		t.Errorf("error adding service: %s", err)
	}

	service := client.Service(serviceName)
	events := make(chan *discoverd.Event)
	stream, err := service.Watch(events)
	if err != nil {
		t.Errorf("error creating watch: %s", err)
	}

	data := []byte(`{"config":{"network": "10.3.0.0/16"}}`)
	if err := service.SetMeta(&discoverd.ServiceMeta{Data: data}); err != nil {
		t.Errorf("error setting meta: %s", err)
	}

	registry, err := NewRegistry(client, serviceName)
	if err != nil {
		t.Errorf("error creating registry: %s", err)
	}

	return &test{
		T:        t,
		client:   client,
		service:  service,
		events:   events,
		registry: registry,
		cleanup: func() {
			stream.Close()
			killDiscoverd()
		},
	}
}

func (t *test) assertSubnets(expected map[string][]byte) {
loop:
	for {
		select {
		case event := <-t.events:
			if event.Kind == discoverd.EventKindServiceMeta {
				break loop
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for discoverd event")
		}
	}
	res, err := t.registry.GetSubnets()
	if err != nil {
		t.Fatalf("error getting subnets: %s", err)
	}
	if !reflect.DeepEqual(res.Subnets, expected) {
		t.Fatalf("unexpected subnets, expected %s, got %s", expected, res.Subnets)
	}
}

func TestGetConfig(t *testing.T) {
	s := newTest(t)
	defer s.cleanup()

	actual, err := s.registry.GetConfig()
	if err != nil {
		t.Fatalf("error getting config: %s", err)
	}

	expected := `{"network":"10.3.0.0/16"}`
	if !reflect.DeepEqual(actual, []byte(expected)) {
		t.Fatalf("unexpected config, expected %s, got %s", expected, string(actual))
	}
}

func TestCreateSubnet(t *testing.T) {
	s := newTest(t)
	defer s.cleanup()

	_, err := s.registry.CreateSubnet("10.3.1.0-24", `{"PublicIP":"1.2.3.4"}`, 0)
	if err != nil {
		t.Fatalf("error creating subnet: %s", err)
	}
	s.assertSubnets(map[string][]byte{"10.3.1.0-24": []byte(`{"PublicIP":"1.2.3.4"}`)})

	_, err = s.registry.CreateSubnet("10.3.1.0-24", `{"PublicIP":"1.2.3.4"}`, 0)
	if err != subnet.ErrSubnetExists {
		t.Fatalf("expected ErrSubnetExists, got: %s", err)
	}
}

func TestUpdateSubnet(t *testing.T) {
	s := newTest(t)
	defer s.cleanup()

	_, err := s.registry.CreateSubnet("10.3.1.0-24", `{"PublicIP":"1.2.3.4"}`, 0)
	if err != nil {
		t.Fatalf("error creating subnet: %s", err)
	}
	s.assertSubnets(map[string][]byte{"10.3.1.0-24": []byte(`{"PublicIP":"1.2.3.4"}`)})

	_, err = s.registry.UpdateSubnet("10.3.1.0-24", `{"PublicIP":"4.5.6.7"}`, 0)
	if err != nil {
		t.Fatalf("error updating subnet: %s", err)
	}
	s.assertSubnets(map[string][]byte{"10.3.1.0-24": []byte(`{"PublicIP":"4.5.6.7"}`)})
}

func TestGetSubnetsPrunesDeadHosts(t *testing.T) {
	s := newTest(t)
	defer s.cleanup()

	if _, err := s.registry.CreateSubnet("10.3.1.0-24", `{"PublicIP":"1.2.3.4"}`, 0); err != nil {
		t.Fatalf("error creating live subnet: %s", err)
	}
	if _, err := s.registry.CreateSubnet("10.3.2.0-24", `{"PublicIP":"5.6.7.8"}`, 0); err != nil {
		t.Fatalf("error creating dead subnet: %s", err)
	}

	hb, err := s.client.Register("flannel-test", "1.2.3.4:5001")
	if err != nil {
		t.Fatalf("error registering instance: %s", err)
	}
	defer hb.Close()

	res, err := s.registry.GetSubnets()
	if err != nil {
		t.Fatalf("error getting subnets: %s", err)
	}
	expected := map[string][]byte{"10.3.1.0-24": []byte(`{"PublicIP":"1.2.3.4"}`)}
	if !reflect.DeepEqual(res.Subnets, expected) {
		t.Fatalf("unexpected subnets, expected %s, got %s", expected, res.Subnets)
	}

	// A booting host's discoverd view may miss a live peer; GetSubnets must
	// not delete that peer's lease from the shared service meta.
	net, err := s.registry.(*registry).getNetwork()
	if err != nil {
		t.Fatalf("error reading network: %s", err)
	}
	if _, ok := net.Subnets["10.3.2.0-24"]; !ok {
		t.Fatalf("GetSubnets persisted a prune of 10.3.2.0-24: %v", net.Subnets)
	}
}

// A discoverd reconnect sends Down for every instance missing from the new
// server's first snapshot, including hosts whose flannel is still alive.
func TestDownEventKeepsLeaseOfLiveHost(t *testing.T) {
	s := newTest(t)
	defer s.cleanup()

	defer func(d time.Duration) { downPruneGrace = d }(downPruneGrace)
	downPruneGrace = 100 * time.Millisecond

	if _, err := s.registry.CreateSubnet("10.3.1.0-24", `{"PublicIP":"1.2.3.4"}`, 0); err != nil {
		t.Fatalf("error creating live subnet: %s", err)
	}
	if _, err := s.registry.CreateSubnet("10.3.2.0-24", `{"PublicIP":"5.6.7.8"}`, 0); err != nil {
		t.Fatalf("error creating dead subnet: %s", err)
	}
	hb, err := s.client.Register("flannel-test", "1.2.3.4:5001")
	if err != nil {
		t.Fatalf("error registering instance: %s", err)
	}
	defer hb.Close()

	r := s.registry.(*registry)
	r.pruneDownHost("1.2.3.4")
	r.pruneDownHost("5.6.7.8")

	net, err := r.getNetwork()
	if err != nil {
		t.Fatalf("error reading network: %s", err)
	}
	if _, ok := net.Subnets["10.3.1.0-24"]; !ok {
		t.Fatalf("Down event pruned the lease of a host with a live flannel instance: %v", net.Subnets)
	}
	if _, ok := net.Subnets["10.3.2.0-24"]; ok {
		t.Fatalf("Down event did not prune the lease of a dead host: %v", net.Subnets)
	}
}

func TestWatchSubnets(t *testing.T) {
	s := newTest(t)
	defer s.cleanup()

	createRes, err := s.registry.CreateSubnet("10.3.1.0-24", `{"PublicIP":"1.2.3.4"}`, 0)
	if err != nil {
		t.Fatalf("error creating subnet: %s", err)
	}
	s.assertSubnets(map[string][]byte{"10.3.1.0-24": []byte(`{"PublicIP":"1.2.3.4"}`)})

	_, err = s.registry.CreateSubnet("10.3.2.0-24", `{"PublicIP":"4.5.6.7"}`, 0)
	if err != nil {
		t.Fatalf("error creating subnet: %s", err)
	}

	res, err := s.registry.WatchSubnets(createRes.Index+1, make(chan bool))
	if err != nil {
		t.Fatalf("error watching subnets: %s", err)
	}
	expected := map[string][]byte{"10.3.2.0-24": []byte(`{"PublicIP":"4.5.6.7"}`)}
	if !reflect.DeepEqual(res.Subnets, expected) {
		t.Fatalf("unexpected subnets, expected %s, got %s", expected, res.Subnets)
	}
}

// GetSubnets hides a lease whose host has no live flannel instance yet (a
// peer that is still re-registering after a reload). WatchSubnets must still
// deliver that lease once, because the caller never saw it. Otherwise the
// event that skipped it marks it "known" and this host never installs the
// route, so its overlay to that peer stays blackholed until flannel restarts.
func TestWatchSubnetsDeliversLeaseHiddenFromSnapshot(t *testing.T) {
	s := newTest(t)
	defer s.cleanup()

	if _, err := s.registry.CreateSubnet("10.3.1.0-24", `{"PublicIP":"1.2.3.4"}`, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.registry.CreateSubnet("10.3.2.0-24", `{"PublicIP":"5.6.7.8"}`, 0); err != nil {
		t.Fatal(err)
	}
	// A later write bumps the meta index past both creations.
	if _, err := s.registry.UpdateSubnet("10.3.1.0-24", `{"PublicIP":"1.2.3.4","n":1}`, 0); err != nil {
		t.Fatal(err)
	}
	hb, err := s.client.Register("flannel-test", "1.2.3.4:5001")
	if err != nil {
		t.Fatal(err)
	}
	defer hb.Close()

	snap, err := s.registry.GetSubnets()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := snap.Subnets["10.3.2.0-24"]; ok {
		t.Fatalf("setup: snapshot should hide the lease of the host with no live instance: %v", snap.Subnets)
	}

	type result struct {
		res *subnet.Response
		err error
	}
	got := make(chan result, 1)
	go func() {
		res, err := s.registry.WatchSubnets(snap.Index, make(chan bool))
		got <- result{res, err}
	}()
	select {
	case r := <-got:
		if r.err != nil {
			t.Fatal(r.err)
		}
		if _, ok := r.res.Subnets["10.3.2.0-24"]; !ok {
			// The snapshot hid it and the watch did not deliver it.
			t.Fatalf("lease 10.3.2.0-24 was never delivered to the caller: %v", r.res.Subnets)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for WatchSubnets")
	}
}
