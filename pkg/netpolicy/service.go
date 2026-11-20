package netpolicy

import (
	"fmt"
	"strings"

	host "github.com/randy-girard/flynn/host/types"
	"github.com/randy-girard/flynn/pkg/resname"
)

// systemServiceNames are discoverd services that system apps register. A
// user app whose name is a prefix of one of them (an app named "router" or
// "controller-scheduler") still may not register it.
var systemServiceNames = map[string]struct{}{
	"acme-challenge":       {},
	"blobstore":            {},
	"controller":           {},
	"controller-grpc":      {},
	"controller-scheduler": {},
	"controller-worker":    {},
	"dashboard":            {},
	"discoverd":            {},
	"flannel":              {},
	"flynn-host":           {},
	"gitreceive":           {},
	"logaggregator":        {},
	"partial-logger":       {},
	"postgres":             {},
	"postgres-api":         {},
	"router":               {},
	"router-api":           {},
	"router-http":          {},
	"status":               {},
	"status-web":           {},
	"tarreceive":           {},
}

// SystemServiceName reports whether service belongs to the platform: a
// well-known system service, a flynn-net-* overlay service, an isolated
// datastore instance (redis-lagoon-59415) or a UUID appliance.
func SystemServiceName(service string) bool {
	if _, ok := systemServiceNames[service]; ok {
		return true
	}
	if strings.HasPrefix(service, "flynn-net-") {
		return true
	}
	return resname.IsolatedService(service) || isUUIDApplianceName(service)
}

// UserServiceAllowed reports whether a user (non-system) app named appName
// may register the discoverd service name. The name must be the app name or
// start with "<app name>-", and must not be a platform service. System and
// plugin apps are not checked.
func UserServiceAllowed(appName, service string) bool {
	if appName == "" || service == "" {
		return false
	}
	if service != appName && !strings.HasPrefix(service, appName+"-") {
		return false
	}
	return !SystemServiceName(service)
}

// UserServiceError describes why appName may not register service.
func UserServiceError(appName, service string) error {
	if UserServiceAllowed(appName, service) {
		return nil
	}
	if appName == "" {
		return fmt.Errorf("service %q: the job has no app name", service)
	}
	if SystemServiceName(service) {
		return fmt.Errorf("service %q is reserved for the platform", service)
	}
	return fmt.Errorf("service %q must be %q or start with %q", service, appName, appName+"-")
}

// UserJobServiceError returns an error when a user-class job declares a port
// service its app does not own. Other job classes are not checked.
func UserJobServiceError(job *host.Job) error {
	if job == nil || ClassifyJob(job) != ClassUser {
		return nil
	}
	appName := ""
	if job.Metadata != nil {
		appName = job.Metadata[host.MetaControllerAppName]
	}
	for _, port := range job.Config.Ports {
		if port.Service == nil {
			continue
		}
		if err := UserServiceError(appName, port.Service.Name); err != nil {
			return err
		}
	}
	return nil
}
