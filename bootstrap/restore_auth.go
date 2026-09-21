package bootstrap

import (
	"fmt"
	"os"

	"github.com/randy-girard/flynn/pkg/random"
)

// ConfigureRestoreAuthAction pushes the backup's discoverd and controller
// keys onto each host before discoverd is started. A restored discoverd
// already requires DISCOVERD_AUTH_KEY, and a fresh layer-0 host.json does
// not have it, so the daemon never registers and wait-hosts times out.
type ConfigureRestoreAuthAction struct {
	DiscoverdKey  string
	ControllerKey string
}

func init() {
	Register("configure-restore-auth", &ConfigureRestoreAuthAction{})
}

// RestoreHostEnv is the host.json env a from-backup bootstrap must apply
// before starting discoverd.
func RestoreHostEnv(discoverdKey, controllerKey string) map[string]string {
	out := map[string]string{}
	if discoverdKey != "" {
		out["DISCOVERD_AUTH_KEY"] = discoverdKey
	}
	if controllerKey != "" {
		out["AUTH_KEY"] = controllerKey
		out["CONTROLLER_KEY"] = controllerKey
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (a *ConfigureRestoreAuthAction) Run(s *State) error {
	extra := RestoreHostEnv(a.DiscoverdKey, a.ControllerKey)
	if len(extra) == 0 {
		return nil
	}
	key := s.HostAuthKey()
	if key == "" {
		key = random.Hex(16)
	}
	clientKey := os.Getenv("FLYNN_HOST_AUTH_KEY")
	for _, h := range s.Hosts {
		if err := configureHostAuthOn(h, key, clientKey, extra); err != nil {
			return fmt.Errorf("bootstrap: error applying restore host secrets on %s: %s", h.Addr(), err)
		}
	}
	s.SetHostAuthKey(key)
	os.Setenv("FLYNN_HOST_AUTH_KEY", key)
	if dkey := extra["DISCOVERD_AUTH_KEY"]; dkey != "" {
		s.SetDiscoverdAuthKey(dkey)
	}
	if ck := extra["CONTROLLER_KEY"]; ck != "" {
		s.SetControllerKey(ck)
		// Sirenia wait polls appliance /status from this process. The
		// client reads CONTROLLER_KEY from the environment, not from state.
		os.Setenv("AUTH_KEY", ck)
		os.Setenv("CONTROLLER_KEY", ck)
	}
	if err := waitForHostAuth(s); err != nil {
		return err
	}
	s.refreshHostClients()
	return nil
}
