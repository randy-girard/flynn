package plugin

import (
	"os"
	"path/filepath"
	"testing"

	ct "github.com/flynn/flynn/controller/types"
)

func TestDiscoverLocalPluginAliases(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "flynn-plugin-widget")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(dir, ManifestName), map[string]interface{}{
		"name":    "widget",
		"kind":    "resource-provider",
		"aliases": []string{"widg"},
		"provider": map[string]string{
			"name": "cache",
			"url":  "http://widget-api.discoverd/x",
		},
		"app": map[string]interface{}{
			"name": "widget",
			"processes": map[string]interface{}{
				"web": map[string]interface{}{"args": []string{"/bin/x"}},
			},
		},
		"cli": map[string]interface{}{"command": "widget"},
	})
	got := DiscoverLocalPlugins(root)
	for _, name := range []string{"widget", "cache", "widg"} {
		a := got[name]
		if a.Path != dir || a.Repo != "flynn-plugin-widget" {
			t.Fatalf("%s: %+v", name, a)
		}
	}
}

func TestWriteReadInstalled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "installed-plugins.json")
	in := []Installed{{
		Name:            "widget",
		Aliases:         []string{"widg"},
		Datastore:       true,
		Sirenia:         true,
		SireniaOptional: true,
		GitHubRepo:      "flynn-plugin-widget",
		Backup:          &BackupSpec{File: "widget.sql.gz", Args: []string{"dump"}},
	}}
	if err := WriteInstalled(path, in); err != nil {
		t.Fatal(err)
	}
	got := ReadInstalled(path)
	if len(got) != 1 || got[0].Name != "widget" || !got[0].MatchesName("widg") || got[0].Backup.File != "widget.sql.gz" {
		t.Fatalf("%+v", got)
	}
}

func TestRecordFromAppUsesPluginRecord(t *testing.T) {
	m := &Manifest{
		Name:    "widget",
		Kind:    KindResourceProvider,
		Aliases: []string{"widg"},
		Provider: &Provider{
			Name: "cache",
			URL:  "http://widget-api.discoverd/x",
		},
		App: AppSpec{
			Name:     "widget",
			Strategy: "sirenia",
			Meta:     map[string]string{MetaDatastore: "true"},
			Scale:    map[string]int{"widget": 0},
			Processes: map[string]ct.ProcessType{
				"widget": {},
				"web":    {},
			},
		},
		Env: map[string]string{"SIRENIA_PROCESS": "widget"},
		CLI: &CLI{Command: "cache"},
	}
	meta := m.AnnotateInstall(m.AppMeta(), "widget", "v1")
	app := &ct.App{ID: "a1", Name: "widget", Strategy: "sirenia", Meta: meta}
	rec := RecordFromApp(app)
	if rec.Name != "widget" || !rec.Datastore || !rec.Sirenia || !rec.SireniaOptional {
		t.Fatalf("record: %+v", rec)
	}
	if !rec.MatchesName("cache") || !rec.MatchesName("widg") {
		t.Fatalf("aliases: %+v", rec)
	}
}

func TestBackupRestoreSpecInference(t *testing.T) {
	p := Installed{Name: "db"}
	mysql := &ct.ExpandedFormation{Release: &ct.Release{Env: map[string]string{"MYSQL_PWD": "x"}}}
	b := BackupSpecFor(p, mysql)
	if b == nil || b.File != "mysql.sql.gz" {
		t.Fatalf("mysql backup: %+v", b)
	}
	r := RestoreSpecFor(p, mysql)
	if r == nil || r.File != "mysql.sql.gz" || len(r.Args) < 5 || r.Args[4] != "leader.db.discoverd" {
		t.Fatalf("mysql restore: %+v", r)
	}
	mongo := &ct.ExpandedFormation{Release: &ct.Release{Env: map[string]string{"MONGO_PWD": "y"}}}
	if BackupSpecFor(p, mongo).File != "mongodb.archive.gz" {
		t.Fatal("mongo backup")
	}
}

func TestSireniaServiceNamesFromInventory(t *testing.T) {
	got := SireniaServiceNamesFrom(nil)
	if len(got) != 1 || got[0] != "postgres" {
		t.Fatalf("%v", got)
	}
	got = SireniaServiceNamesFrom([]Installed{{Name: "widget", Sirenia: true}})
	if len(got) != 2 || got[1] != "widget" {
		t.Fatalf("%v", got)
	}
	if IsSireniaManaged(&ct.App{Name: "postgres"}) != true {
		t.Fatal("postgres is sirenia")
	}
	if IsSireniaManaged(&ct.App{Name: "shop"}) {
		t.Fatal("user app")
	}
}
