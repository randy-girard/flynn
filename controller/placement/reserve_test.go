package placement

import (
	"testing"

	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/host/resource"
	"github.com/randy-girard/flynn/pkg/typeconv"
)

func sys(name string, extra ...string) *ct.App {
	meta := map[string]string{"flynn-system-app": "true"}
	for i := 0; i+1 < len(extra); i += 2 {
		meta[extra[i]] = extra[i+1]
	}
	return &ct.App{Name: name, Meta: meta}
}

func TestPlatformApp(t *testing.T) {
	for _, app := range []*ct.App{
		sys("controller"), sys("router"), sys("discoverd"), sys("blobstore"),
		sys("postgres", "flynn-datastore", "true"),
		sys("redis-plugin", "flynn-plugin", "true", "flynn-datastore", "true"),
		sys("dashboard", "flynn-plugin", "true"),
	} {
		if !PlatformApp(app) {
			t.Errorf("%s must be a platform app", app.Name)
		}
	}
	for _, app := range []*ct.App{
		nil,
		{Name: "shop"},
		{Name: "router"}, // a tenant app may not be named router, but meta decides
		sys("redis-11111111-1111-1111-1111-111111111111", "flynn-datastore", "true"),
		sys("postgresql-concave-48291", "flynn-datastore", "true"),
	} {
		if PlatformApp(app) {
			t.Errorf("%v must not be a platform app", app)
		}
	}
}

func TestReservationsHosted(t *testing.T) {
	small := ct.ProcessType{RuntimeProfile: "Small"}
	custom := ct.ProcessType{RuntimeProfile: "gpu"}
	none := ct.ProcessType{}
	hosted := Reservations{TenancyMode: TenancyHosted, ProfileReserve: map[string]bool{"gpu": true}}
	if !hosted.Reserves(&ct.App{Name: "shop"}, small) {
		t.Fatal("hosted small reserves for a tenant app")
	}
	if hosted.Reserves(sys("router"), small) || hosted.Reserves(sys("controller"), custom) {
		t.Fatal("hosted mode never reserves for a platform app (NEW-45)")
	}
	if !hosted.Reserves(sys("redis-11111111-1111-1111-1111-111111111111", "flynn-datastore", "true"), small) {
		t.Fatal("a tenant datastore still reserves")
	}
	if !hosted.Reserves(&ct.App{Name: "shop"}, custom) {
		t.Fatal("a flagged custom profile reserves")
	}
	if hosted.Reserves(&ct.App{Name: "shop"}, none) {
		t.Fatal("no profile never reserves")
	}
	self := Reservations{TenancyMode: "self_hosted", ProfileReserve: map[string]bool{"small": true}}
	if !self.Reserves(sys("controller"), small) {
		t.Fatal("self_hosted applies the stored flag to every app")
	}
	if (Reservations{TenancyMode: "self_hosted"}).Reserves(&ct.App{Name: "shop"}, small) {
		t.Fatal("self_hosted small with the flag off does not reserve")
	}
}

func TestRequestFallsBackToLimitOnlyWhenReserving(t *testing.T) {
	proc := ct.ProcessType{Resources: resource.Resources{
		resource.TypeMemory: {Limit: typeconv.Int64Ptr(100)},
		resource.TypeCPU:    {Limit: typeconv.Int64Ptr(10)},
	}}
	if m, c := Request(proc, false); m != 0 || c != 0 {
		t.Fatalf("shared request %d %d", m, c)
	}
	if m, c := Request(proc, true); m != 100 || c != 10 {
		t.Fatalf("reserved fallback %d %d", m, c)
	}
}

func TestFits(t *testing.T) {
	if !Fits(0, 0, 0, 0, 0, 0) {
		t.Fatal("nothing needed always fits")
	}
	if Fits(0, 0, 0, 0, 1, 0) {
		t.Fatal("an unread host does not fit a reserving job")
	}
	if !Fits(1000, 0, 500, 0, 500, 100) {
		t.Fatal("zero CPU total skips that dimension")
	}
	if Fits(1000, 1000, 600, 0, 500, 0) {
		t.Fatal("memory overcommit")
	}
	if Fits(1000, 1000, 0, 600, 0, 500) {
		t.Fatal("cpu overcommit")
	}
}
