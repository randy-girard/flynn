package appmanifest

import (
	"fmt"
	"io"
	"strings"

	controller "github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/host/resource"
	"github.com/randy-girard/flynn/pkg/cluster"
	"github.com/randy-girard/flynn/pkg/dbruntime"
	"github.com/randy-girard/flynn/pkg/plugin"
	router "github.com/randy-girard/flynn/router/types"
)

// backend is the controller surface the applier uses. Tests stub it.
type backend interface {
	GetApp(id string) (*ct.App, error)
	UpdateApp(app *ct.App) error
	UpdateAppMeta(app *ct.App) error
	DeleteApp(id string) error
	Head(appID string) (*ct.Release, error)
	SaveRelease(appID string, rel *ct.Release, deploy bool) error
	Provision(req *ct.ResourceReq) (*ct.Resource, error)
	PutPluginAddon(appID, addonType string) error
	Routes(appID string) ([]*router.Route, error)
	CreateRoute(appID string, route *router.Route) error
	Scale(appID, releaseID string, opts ct.ScaleOptions) error
	Profiles() ([]*ct.RuntimeProfile, error)
	Settings() (*ct.RuntimeSettings, error)
	RunCommand(appID string, rel *ct.Release, command string, resources resource.Resources) error
	Formation(appID, releaseID string) (*ct.Formation, error)
	Resources(appID string) ([]*ct.Resource, error)
}

// New builds an applier for a controller client. admin is true for the
// in-cluster receiver (formation tags are allowed). The laptop CLI passes
// false.
func New(client controller.Client, log io.Writer, admin bool) (*Applier, error) {
	if log == nil {
		log = io.Discard
	}
	cat, err := plugin.LoadCatalog(client)
	if err != nil {
		return nil, err
	}
	var rts dbruntime.Catalog
	if listed, err := client.ListDBRuntimes(); err == nil && listed != nil {
		rts = *listed
	}
	https := false
	if cfg, err := client.GetACMEConfig(); err == nil && cfg != nil && cfg.Enabled {
		https = true
	}
	return &Applier{
		b:        controllerBackend{client: client, log: log},
		Catalog:  cat,
		Runtimes: rts,
		Admin:    admin,
		HTTPS:    https,
		Log:      log,
	}, nil
}

// LoadState reads the app for Decide. The preview route and the included
// HTTP route are not extra routes. A preview seed release is not a user release.
func (a *Applier) LoadState(appID string) (AppState, error) {
	app, err := a.b.GetApp(appID)
	if err != nil {
		return AppState{}, err
	}
	head, err := a.b.Head(appID)
	if err != nil {
		return AppState{}, err
	}
	st := AppState{App: app, Head: head}
	if a.Hooks.Pipeline != nil {
		ctx, err := a.Hooks.Pipeline(appID)
		if err != nil && metaValue(app, "flynn-pipeline") != "" {
			return AppState{}, err
		}
		if err == nil {
			st.Environment = ctx.Environment
		}
	}
	if res, err := a.b.Resources(appID); err == nil {
		st.Resources = len(res)
	}
	previewDomain := metaValue(app, MetaPreviewRoute)
	if routes, err := a.b.Routes(appID); err == nil {
		for _, r := range routes {
			if r == nil || r.Included {
				continue
			}
			if previewDomain != "" && strings.EqualFold(r.Domain, previewDomain) {
				continue
			}
			st.ExtraRoutes++
		}
	}
	if head != nil && head.ID != "" {
		if form, err := a.b.Formation(appID, head.ID); err == nil && form != nil && len(form.Processes) > 0 {
			st.Scaled = true
		}
	}
	if a.Hooks.GitHubConnected != nil {
		ok, err := a.Hooks.GitHubConnected(appID)
		if err != nil && a.Hooks.GitHubInstalled != nil && a.Hooks.GitHubInstalled() {
			return AppState{}, err
		}
		st.GitHubConnected = ok
	}
	return st, nil
}

type controllerBackend struct {
	client controller.Client
	log    io.Writer
}

func (c controllerBackend) GetApp(id string) (*ct.App, error) { return c.client.GetApp(id) }

func (c controllerBackend) UpdateApp(app *ct.App) error { return c.client.UpdateApp(app) }

func (c controllerBackend) UpdateAppMeta(app *ct.App) error {
	return c.client.UpdateAppMeta(app)
}

func (c controllerBackend) DeleteApp(id string) error {
	_, err := c.client.DeleteApp(id)
	return err
}

func (c controllerBackend) Head(appID string) (*ct.Release, error) {
	rel, err := controller.HeadRelease(c.client, appID)
	if err == controller.ErrNotFound {
		return nil, nil
	}
	return rel, err
}

func (c controllerBackend) SaveRelease(appID string, rel *ct.Release, deploy bool) error {
	rel.ID = ""
	if err := c.client.CreateRelease(appID, rel); err != nil {
		return err
	}
	if deploy && len(rel.ArtifactIDs) > 0 {
		return c.client.DeployAppRelease(appID, rel.ID, nil)
	}
	return c.client.SetAppRelease(appID, rel.ID)
}

func (c controllerBackend) Provision(req *ct.ResourceReq) (*ct.Resource, error) {
	return c.client.ProvisionResource(req)
}

func (c controllerBackend) PutPluginAddon(appID, addonType string) error {
	_, err := c.client.PutPluginAddon(appID, addonType)
	return err
}

func (c controllerBackend) Routes(appID string) ([]*router.Route, error) {
	return c.client.AppRouteList(appID)
}

func (c controllerBackend) CreateRoute(appID string, route *router.Route) error {
	return c.client.CreateRoute(appID, route)
}

func (c controllerBackend) Scale(appID, releaseID string, opts ct.ScaleOptions) error {
	return c.client.ScaleAppRelease(appID, releaseID, opts)
}

func (c controllerBackend) Profiles() ([]*ct.RuntimeProfile, error) {
	return c.client.ListRuntimeProfiles()
}

func (c controllerBackend) Settings() (*ct.RuntimeSettings, error) {
	return c.client.GetRuntimeSettings()
}

func (c controllerBackend) Formation(appID, releaseID string) (*ct.Formation, error) {
	form, err := c.client.GetFormation(appID, releaseID)
	if err == controller.ErrNotFound {
		return nil, nil
	}
	return form, err
}

func (c controllerBackend) Resources(appID string) ([]*ct.Resource, error) {
	return c.client.AppResourceList(appID)
}

func (c controllerBackend) RunCommand(appID string, rel *ct.Release, command string, resources resource.Resources) error {
	if rel == nil || rel.ID == "" {
		return fmt.Errorf("postdeploy needs a release")
	}
	rwc, err := c.client.RunJobAttached(appID, &ct.NewJob{
		ReleaseID:  rel.ID,
		ReleaseEnv: true,
		Args:       rel.RunJobArgs([]string{"sh", "-c", command}),
		Resources:  resources,
		Type:       "postdeploy",
	})
	if err != nil {
		return err
	}
	defer rwc.Close()
	attach := cluster.NewAttachClient(rwc)
	_ = attach.CloseWrite()
	out := c.log
	if out == nil {
		out = io.Discard
	}
	exit, err := attach.Receive(out, out)
	if err != nil {
		return fmt.Errorf("postdeploy: %w", err)
	}
	if exit != 0 {
		return fmt.Errorf("postdeploy exited %d", exit)
	}
	return nil
}
