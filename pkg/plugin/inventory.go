package plugin

import ct "github.com/flynn/flynn/controller/types"

// Installed is the backup/restore inventory of one plugin. It is name-agnostic:
// any flynn-plugin=true app is listed so restore knows what was present.
type Installed struct {
	Name   string `json:"name"`
	Kind   string `json:"kind,omitempty"`
	Source string `json:"source,omitempty"`
	Ref    string `json:"ref,omitempty"`
	Wait   string `json:"wait,omitempty"`
	AppID  string `json:"app_id,omitempty"`
	CLI    *CLI   `json:"cli,omitempty"`
}

// ListInstalled returns plugin apps from a controller AppList. Cluster backup
// writes this as plugins.json; postgres already stores the apps/artifacts/
// blobstore objects, so restore does not run plugin install again.
func ListInstalled(apps []*ct.App) []Installed {
	out := []Installed{}
	for _, app := range apps {
		if app == nil || !app.Plugin() {
			continue
		}
		out = append(out, Installed{
			Name:   app.Name,
			Kind:   app.Meta[MetaPluginKind],
			Source: app.Meta[MetaPluginSource],
			Ref:    app.Meta[MetaPluginRef],
			Wait:   app.Meta[MetaPluginWait],
			AppID:  app.ID,
			CLI:    CLIFromApp(app),
		})
	}
	return out
}
