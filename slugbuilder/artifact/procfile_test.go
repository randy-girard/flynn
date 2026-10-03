package main

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestDetermineProcessCommandsFromProcfile(t *testing.T) {
	dir := t.TempDir()
	app := filepath.Join(dir, "app")
	if err := os.MkdirAll(app, 0755); err != nil {
		t.Fatal(err)
	}
	procfile := []byte("web: bundle exec puma -C config/puma.rb\nrelease: bundle exec rake db:migrate\nworker: bundle exec sidekiq\n")
	if err := ioutil.WriteFile(filepath.Join(app, "Procfile"), procfile, 0644); err != nil {
		t.Fatal(err)
	}
	got := determineProcessCommands(dir)
	want := map[string]string{
		"web":     "bundle exec puma -C config/puma.rb",
		"release": "bundle exec rake db:migrate",
		"worker":  "bundle exec sidekiq",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("commands = %#v, want %#v", got, want)
	}
	names := processTypeNames(got)
	if !reflect.DeepEqual(names, []string{"release", "web", "worker"}) {
		t.Fatalf("names = %#v", names)
	}
}

func TestDetermineProcessCommandsFromReleaseDefaults(t *testing.T) {
	dir := t.TempDir()
	app := filepath.Join(dir, "app")
	if err := os.MkdirAll(app, 0755); err != nil {
		t.Fatal(err)
	}
	if err := ioutil.WriteFile(filepath.Join(app, ".release"), []byte("default_process_types:\n  web: npm start\n"), 0644); err != nil {
		t.Fatal(err)
	}
	got := determineProcessCommands(dir)
	if got["web"] != "npm start" {
		t.Fatalf("commands = %#v", got)
	}
}
