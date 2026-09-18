package updaterdeploy

import (
	"errors"
	"testing"

	controller "github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
)

func TestMissingAppReleaseSkip(t *testing.T) {
	userApp := &ct.App{Name: "testappone"}
	systemApp := &ct.App{
		Name: "controller",
		Meta: map[string]string{"flynn-system-app": "true"},
	}
	redisApp := ct.NewRedisApplianceApp("redis-abc")

	cases := []struct {
		name string
		app  *ct.App
		err  error
		skip bool
	}{
		{name: "user app with no release", app: userApp, err: controller.ErrNotFound, skip: true},
		{name: "user app other error", app: userApp, err: errors.New("controller unavailable"), skip: false},
		{name: "user app nil error", app: userApp, err: nil, skip: false},
		{name: "system app with no release", app: systemApp, err: controller.ErrNotFound, skip: false},
		{name: "redis appliance with no release", app: redisApp, err: controller.ErrNotFound, skip: false},
		{name: "nil app", app: nil, err: controller.ErrNotFound, skip: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := MissingAppReleaseSkip(tc.app, tc.err); got != tc.skip {
				t.Fatalf("MissingAppReleaseSkip(%q) = %v, want %v", tc.name, got, tc.skip)
			}
		})
	}
}
