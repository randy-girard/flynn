package plugin

import (
	"fmt"
	"testing"

	ct "github.com/flynn/flynn/controller/types"
	router "github.com/flynn/flynn/router/types"
)

type apexStub struct {
	byApp   map[string][]*router.Route
	created []*router.Route
	deleted []string
	acme    *ct.ACMEConfig
}

func (s *apexStub) AppRouteList(appID string) ([]*router.Route, error) {
	return s.byApp[appID], nil
}

func (s *apexStub) CreateRoute(appID string, route *router.Route) error {
	cp := *route
	if cp.ID == "" {
		cp.ID = fmt.Sprintf("r%d", len(s.created)+1)
	}
	s.created = append(s.created, &cp)
	s.byApp[appID] = append(s.byApp[appID], &cp)
	return nil
}

func (s *apexStub) UpdateRoute(string, string, *router.Route) error { return nil }

func (s *apexStub) DeleteRoute(appID, routeID string) error {
	out := s.byApp[appID][:0]
	found := false
	for _, r := range s.byApp[appID] {
		if r != nil && (r.FormattedID() == routeID || r.ID == routeID) {
			found = true
			continue
		}
		out = append(out, r)
	}
	if !found {
		return fmt.Errorf("route %s not found", routeID)
	}
	s.byApp[appID] = out
	s.deleted = append(s.deleted, routeID)
	return nil
}

func (s *apexStub) GetACMEConfig() (*ct.ACMEConfig, error) {
	if s.acme == nil {
		return &ct.ACMEConfig{Enabled: false}, nil
	}
	return s.acme, nil
}

func TestAssignAndClearApex(t *testing.T) {
	www := pluginApp("www")
	dash := pluginApp("dashboard")
	apps := []*ct.App{www, dash}
	stub := &apexStub{
		byApp: map[string][]*router.Route{
			www.ID:  {{Type: "http", ID: "www1", Domain: "www.ex.local", Service: "www"}},
			dash.ID: {{Type: "http", ID: "dash1", Domain: "dashboard.ex.local", Service: "dashboard"}},
		},
		acme: &ct.ACMEConfig{Enabled: true},
	}

	info, err := LookupApex(stub, apps, "ex.local")
	if err != nil || info.App != nil {
		t.Fatalf("empty apex: %+v %v", info, err)
	}

	route, err := AssignApex(stub, apps, "ex.local", www)
	if err != nil {
		t.Fatal(err)
	}
	if route.Domain != "ex.local" || route.Service != "www" || route.ManagedCertificateDomain == nil {
		t.Fatalf("route %+v", route)
	}

	info, err = LookupApex(stub, apps, "ex.local")
	if err != nil || info.App == nil || info.App.Name != "www" {
		t.Fatalf("lookup %+v %v", info, err)
	}

	moved, err := AssignApex(stub, apps, "ex.local", dash)
	if err != nil {
		t.Fatal(err)
	}
	if moved.Service != "dashboard" {
		t.Fatalf("moved %+v", moved)
	}
	info, err = LookupApex(stub, apps, "ex.local")
	if err != nil || info.App == nil || info.App.Name != "dashboard" {
		t.Fatalf("after move %+v %v", info, err)
	}

	cleared, err := ClearApex(stub, apps, "ex.local")
	if err != nil || cleared.App == nil || cleared.App.Name != "dashboard" {
		t.Fatalf("clear %+v %v", cleared, err)
	}
	info, err = LookupApex(stub, apps, "ex.local")
	if err != nil || info.Route != nil {
		t.Fatalf("apex still present %+v %v", info, err)
	}
}

func TestAssignApexIdempotent(t *testing.T) {
	www := pluginApp("www")
	stub := &apexStub{
		byApp: map[string][]*router.Route{
			www.ID: {{Type: "http", ID: "apex", Domain: "ex.local", Service: "www"}},
		},
	}
	route, err := AssignApex(stub, []*ct.App{www}, "ex.local", www)
	if err != nil {
		t.Fatal(err)
	}
	if route.ID != "apex" || len(stub.created) != 0 {
		t.Fatalf("should keep existing apex %+v created=%d", route, len(stub.created))
	}
}
