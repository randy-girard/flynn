package main

import (
	"reflect"
	"testing"

	ct "github.com/randy-girard/flynn/controller/types"
)

type stubAppCatalog struct {
	all     []*ct.App
	visible []*ct.App
}

func (s stubAppCatalog) AppList() ([]*ct.App, error)        { return s.all, nil }
func (s stubAppCatalog) AppListVisible() ([]*ct.App, error) { return s.visible, nil }

func TestListAppsDefaultVsAll(t *testing.T) {
	visible := []*ct.App{
		{ID: "1", Name: "myapp"},
		{ID: "3", Name: "dashboard", Meta: map[string]string{"flynn-plugin": "true"}},
		{ID: "4", Name: "postgres-plugin", Meta: map[string]string{"flynn-system-app": "true", "flynn-plugin": "true"}},
	}
	all := []*ct.App{
		{ID: "1", Name: "myapp"},
		{ID: "2", Name: "controller", Meta: map[string]string{"flynn-system-app": "true"}},
		{ID: "3", Name: "dashboard", Meta: map[string]string{"flynn-plugin": "true"}},
		{ID: "4", Name: "postgres-plugin", Meta: map[string]string{"flynn-system-app": "true", "flynn-plugin": "true"}},
	}
	client := stubAppCatalog{all: all, visible: visible}

	got, err := listApps(client, false)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(namesOf(got), []string{"myapp", "dashboard", "postgres-plugin"}) {
		t.Fatalf("default list = %v, want user apps and Flynn-installed plugins", namesOf(got))
	}

	got, err = listApps(client, true)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(namesOf(got), []string{"myapp", "controller", "dashboard", "postgres-plugin"}) {
		t.Fatalf("--all list = %v, want bootstrap system apps included", namesOf(got))
	}
}

func namesOf(apps []*ct.App) []string {
	out := make([]string, 0, len(apps))
	for _, a := range apps {
		out = append(out, a.Name)
	}
	return out
}
