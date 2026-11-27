package app_deletion

import (
	"errors"
	"strings"
	"testing"

	"github.com/inconshreveable/log15"
	ct "github.com/randy-girard/flynn/controller/types"
)

// refusingLeaderClient refuses to deprovision a primary while a resource
// naming it as leader still exists, as the postgres/mysql/redis plugins do.
type refusingLeaderClient struct {
	fakeClient
}

func (r *refusingLeaderClient) DeleteResource(providerID, resourceID string) (*ct.Resource, error) {
	r.mu.Lock()
	var target *ct.Resource
	for _, res := range r.resources {
		if res.ID == resourceID {
			target = res
		}
	}
	if target != nil {
		name := target.Env["FLYNN_POSTGRES"]
		for _, res := range r.resources {
			if res.ID != resourceID && res.Env["FLYNN_POSTGRES_LEADER"] == name {
				r.mu.Unlock()
				return nil, errors.New("validation_error: postgres cannot remove a resource while it still has followers: " + res.Env["FLYNN_POSTGRES"])
			}
		}
	}
	r.mu.Unlock()
	return r.fakeClient.DeleteResource(providerID, resourceID)
}

// CORE-57: deleting an app whose primary has a follower must deprovision the
// follower first, whatever order the controller lists them in.
func TestDeleteResourcesRemovesFollowersBeforeTheirPrimary(t *testing.T) {
	c := &refusingLeaderClient{fakeClient: fakeClient{
		app: &ct.App{ID: "app1", Name: "up-go"},
		resources: []*ct.Resource{
			{ID: "primary", ProviderID: "pg", Apps: []string{"app1"}, Env: map[string]string{"FLYNN_POSTGRES": "postgresql-savanna-92697", "FLYNN_POSTGRES_ROLE": "primary"}},
			{ID: "follower", ProviderID: "pg", Apps: []string{"app1"}, Env: map[string]string{"FLYNN_POSTGRES": "postgresql-quartz-74095", "FLYNN_POSTGRES_ROLE": "follower", "FLYNN_POSTGRES_LEADER": "postgresql-savanna-92697"}},
		},
	}}
	ctx := &context{client: c, logger: log15.New()}
	a := &ct.AppDeletion{AppID: "app1"}
	if err := ctx.deleteResources(log15.New(), a); err != nil {
		t.Fatalf("a primary listed before its follower must still be removed: %v", err)
	}
	if got := strings.Join(c.destructive(), ","); got != "0:follower,0:primary" {
		t.Fatalf("deprovision order %q, want follower then primary", got)
	}
}
