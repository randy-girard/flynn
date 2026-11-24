package updater

import (
	ct "github.com/randy-girard/flynn/controller/types"
	host "github.com/randy-girard/flynn/host/types"
)

// PostgresMetricsPort is where platform postgres peers serve GET /metrics/log.
const PostgresMetricsPort = "9180"

type SystemApp struct {
	Name          string
	MinVersion    string          // minimum version this updater binary is capable of updating
	ImageOnly     bool            // no application, just update the image
	UpdateRelease UpdateReleaseFn // function to migrate changes to release
	Optional      bool            // This system component is optional and may not be present
}

type UpdateReleaseFn func(r *ct.Release)

// SystemApps is the order `flynn-host update` deploys the system apps in.
// An app updates after the apps it needs to run (discoverd, the blobstore)
// and after the controller, whose API it calls; postgres follows the
// controller because the controller's sirenia deployer moves it, and status,
// which checks the rest, comes last.
var SystemApps = []SystemApp{
	{
		Name: "discoverd",
		// versions prior to this one do not have hooks to update
		MinVersion: "v20151129.0",
	},
	{Name: "blobstore"},
	// The controller before the apps that call its API (EDGE-30): it runs
	// every later deploy (deployer, scheduler) and serves the router's route
	// sync, gitreceive's build lease and taffy's deploys. Each app must
	// still work with the previous release of the other, because an update
	// can stop between two apps.
	{Name: "controller"},
	{Name: "taffy"},
	{Name: "router"},
	{Name: "gitreceive"},
	{Name: "tarreceive"},
	{Name: "logaggregator"},
	{
		Name: "postgres",
		UpdateRelease: func(r *ct.Release) {
			r.Env["SIRENIA_PROCESS"] = "postgres"
			if proc, ok := r.Processes["postgres"]; ok && proc.Env[host.MetricsPortEnv] == "" {
				env := make(map[string]string, len(proc.Env)+1)
				for k, v := range proc.Env {
					env[k] = v
				}
				env[host.MetricsPortEnv] = PostgresMetricsPort
				proc.Env = env
				r.Processes["postgres"] = proc
			}
		},
	},
	{Name: "status"},
	{Name: "slugbuilder", ImageOnly: true},
	{Name: "slugrunner", ImageOnly: true},
	{Name: "dockerbuilder", ImageOnly: true},
}
