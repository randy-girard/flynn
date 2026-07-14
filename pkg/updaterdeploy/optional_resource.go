package updaterdeploy

import (
	"fmt"
	"time"

	"github.com/flynn/flynn/controller/client"
	ct "github.com/flynn/flynn/controller/types"
	"github.com/inconshreveable/log15"
)

const optionalResourceDeployTimeout = 5 * time.Minute

// OptionalResourceApps are system apps that provision user-facing resources and
// may be absent on clusters bootstrapped before the appliance was added.
var OptionalResourceApps = map[string]optionalResourceApp{
	"kafka": {
		ServiceEnv:  "FLYNN_KAFKA",
		ImageEnv:    "KAFKA_IMAGE_ID",
		ProviderURL: "http://kafka-api.discoverd/clusters",
		ExtraEnv: map[string]string{
			"KAFKA_TLS_ENABLED": "true",
		},
		StartArgs: []string{"/bin/start-flynn-kafka", "api"},
	},
	"clickhouse": {
		ServiceEnv:  "FLYNN_CLICKHOUSE",
		ImageEnv:    "CLICKHOUSE_IMAGE_ID",
		ProviderURL: "http://clickhouse-api.discoverd/clusters",
		StartArgs:   []string{"/bin/start-flynn-clickhouse", "api"},
	},
}

type optionalResourceApp struct {
	ServiceEnv  string
	ImageEnv    string
	ProviderURL string
	ExtraEnv    map[string]string
	StartArgs   []string
}

// IsOptionalResourceApp reports whether name is a resource appliance that should
// be bootstrapped automatically during cluster updates when missing.
func IsOptionalResourceApp(name string) bool {
	_, ok := OptionalResourceApps[name]
	return ok
}

// EnsureOptionalResourceApp deploys a missing optional resource system app and
// registers its provider. It is a no-op when the app already exists.
func EnsureOptionalResourceApp(client controller.Client, name string, image *ct.Artifact, log log15.Logger) error {
	spec, ok := OptionalResourceApps[name]
	if !ok {
		return fmt.Errorf("unknown optional resource app %q", name)
	}

	if _, err := client.GetApp(name); err == nil {
		return nil
	} else if err != controller.ErrNotFound {
		return err
	}

	controllerKey, err := controllerKeyFromCluster(client)
	if err != nil {
		return err
	}
	singleton := clusterSingleton(client)
	webCount := 2
	if singleton {
		webCount = 1
	}

	log.Info("deploying missing optional resource app", "name", name, "singleton", singleton)

	if err := client.CreateArtifact(image); err != nil {
		return fmt.Errorf("error creating %s image artifact: %w", name, err)
	}

	app := &ct.App{
		Name: name,
		Meta: map[string]string{"flynn-system-app": "true"},
	}
	if err := client.CreateApp(app); err != nil {
		return fmt.Errorf("error creating %s app: %w", name, err)
	}

	env := map[string]string{
		spec.ServiceEnv:  name,
		"CONTROLLER_KEY": controllerKey,
		spec.ImageEnv:    image.ID,
		"SINGLETON":      fmt.Sprintf("%t", singleton),
	}
	for k, v := range spec.ExtraEnv {
		env[k] = v
	}

	release := &ct.Release{
		ArtifactIDs: []string{image.ID},
		Env:         env,
		Processes: map[string]ct.ProcessType{
			"web": {
				Ports: []ct.Port{{Port: 80, Proto: "tcp"}},
				Args:  spec.StartArgs,
			},
		},
	}
	if err := client.CreateRelease(app.ID, release); err != nil {
		return fmt.Errorf("error creating %s release: %w", name, err)
	}

	timeout := optionalResourceDeployTimeout
	if err := client.ScaleAppRelease(app.ID, release.ID, ct.ScaleOptions{
		Processes: map[string]int{"web": webCount},
		Timeout:   &timeout,
	}); err != nil {
		return fmt.Errorf("error scaling %s: %w", name, err)
	}
	if err := client.SetAppRelease(app.ID, release.ID); err != nil {
		return fmt.Errorf("error setting %s release: %w", name, err)
	}

	if err := ensureProvider(client, name, spec.ProviderURL); err != nil {
		return err
	}

	log.Info("finished deploying missing optional resource app", "name", name)
	return nil
}

func ensureProvider(client controller.Client, name, url string) error {
	providers, err := client.ProviderList()
	if err != nil {
		return fmt.Errorf("error listing providers: %w", err)
	}
	for _, p := range providers {
		if p.Name == name {
			return nil
		}
	}
	if err := client.CreateProvider(&ct.Provider{Name: name, URL: url}); err != nil {
		return fmt.Errorf("error creating %s provider: %w", name, err)
	}
	return nil
}

func controllerKeyFromCluster(client controller.Client) (string, error) {
	for _, appName := range []string{"redis", "kafka", "clickhouse", "controller"} {
		release, err := client.GetAppRelease(appName)
		if err != nil {
			continue
		}
		if key := release.Env["CONTROLLER_KEY"]; key != "" {
			return key, nil
		}
		if key := release.Env["AUTH_KEY"]; key != "" {
			return key, nil
		}
	}
	return "", fmt.Errorf("unable to find controller key in cluster")
}

func clusterSingleton(client controller.Client) bool {
	release, err := client.GetAppRelease("postgres")
	if err != nil {
		return false
	}
	return release.Env["SINGLETON"] == "true"
}
