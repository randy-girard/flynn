package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	controllerclient "github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/ctxhelper"
	"github.com/randy-girard/flynn/pkg/httphelper"
	"github.com/randy-girard/flynn/pkg/plugin"
	"golang.org/x/net/context"
)

func (c *controllerAPI) GetPluginAddons(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	app := c.getApp(ctx)
	specs := c.pluginBackendAddons()
	out := make([]*ct.PluginAddon, 0)
	for i := range specs {
		spec := &specs[i]
		added, err := c.pluginAddonAdded(spec, app.ID)
		if err != nil {
			respondWithError(w, err)
			return
		}
		if !added {
			continue
		}
		out = append(out, pluginAddonView(spec))
	}
	httphelper.JSON(w, 200, out)
}

func (c *controllerAPI) PutPluginAddon(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	c.writePluginAddon(ctx, w, req, http.MethodPut)
}

func (c *controllerAPI) DeletePluginAddon(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	c.writePluginAddon(ctx, w, req, http.MethodDelete)
}

func (c *controllerAPI) writePluginAddon(ctx context.Context, w http.ResponseWriter, req *http.Request, method string) {
	app := c.getApp(ctx)
	if !c.requireAppManage(ctx, w, app) {
		return
	}
	params, _ := ctxhelper.ParamsFromContext(ctx)
	typ := ""
	if params != nil {
		typ = params.ByName("addon_type")
	}
	spec := c.pluginAddonByType(typ)
	if spec == nil {
		respondWithError(w, ct.ValidationError{Field: "type", Message: fmt.Sprintf("%s is not installed on this cluster. Operators: flynn-host plugin:install %s", typ, typ)})
		return
	}
	verb := plugin.VerbCreate
	if method == http.MethodDelete {
		verb = plugin.VerbDestroy
	}
	if !spec.Supports(verb) {
		respondWithError(w, ct.ValidationError{Field: "type", Message: spec.VerbError(verb).Error()})
		return
	}
	if err := c.callPluginAddon(method, spec, app.ID); err != nil {
		respondWithError(w, err)
		return
	}
	httphelper.JSON(w, 200, pluginAddonView(spec))
}

func pluginAddonView(spec *plugin.AddonSpec) *ct.PluginAddon {
	return &ct.PluginAddon{Type: spec.Type, Name: spec.Type, Description: spec.Description}
}

func (c *controllerAPI) pluginAddonByType(name string) *plugin.AddonSpec {
	specs := c.pluginBackendAddons()
	for i := range specs {
		if specs[i].MatchesType(name) {
			spec := specs[i]
			return &spec
		}
	}
	return nil
}

func (c *controllerAPI) pluginBackendAddons() []plugin.AddonSpec {
	if c == nil || c.appRepo == nil {
		return nil
	}
	raw, err := c.appRepo.List()
	if err != nil {
		return nil
	}
	apps, _ := raw.([]*ct.App)
	var out []plugin.AddonSpec
	seen := map[string]struct{}{}
	for _, app := range apps {
		spec := plugin.AddonFromApp(app)
		if spec == nil || spec.Backend != plugin.BackendPlugin {
			continue
		}
		if _, ok := seen[spec.Type]; ok {
			continue
		}
		seen[spec.Type] = struct{}{}
		out = append(out, *spec)
	}
	return out
}

func (c *controllerAPI) pluginAddonAdded(spec *plugin.AddonSpec, appID string) (bool, error) {
	status, body, err := c.pluginAddonRequest(http.MethodGet, spec, appID)
	if err != nil {
		return false, err
	}
	if status == http.StatusNotFound {
		return false, nil
	}
	if status != http.StatusOK {
		return false, fmt.Errorf("%s addon: %s", spec.Type, strings.TrimSpace(string(body)))
	}
	var payload struct {
		Added bool `json:"added"`
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &payload); err != nil {
			return false, err
		}
	}
	return payload.Added, nil
}

func (c *controllerAPI) callPluginAddon(method string, spec *plugin.AddonSpec, appID string) error {
	status, body, err := c.pluginAddonRequest(method, spec, appID)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			msg = http.StatusText(status)
		}
		return fmt.Errorf("%s addon: %s", spec.Type, msg)
	}
	return nil
}

func (c *controllerAPI) pluginAddonRequest(method string, spec *plugin.AddonSpec, appID string) (int, []byte, error) {
	if spec == nil || spec.API == "" {
		return 0, nil, fmt.Errorf("addon api is not configured")
	}
	endpoint := spec.API + "/apps/" + url.PathEscape(appID) + "/addon"
	req, err := http.NewRequest(method, endpoint, nil)
	if err != nil {
		return 0, nil, err
	}
	if key := controllerclient.KeyFromEnvOrMeta(nil); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	res, err := httphelper.RetryClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return res.StatusCode, nil, err
	}
	return res.StatusCode, body, nil
}
