package host

import (
	"fmt"
	"strconv"
)

// DefaultListenPort is assigned when a process port is 0 (unspecified).
// Index i becomes DefaultListenPort+i. Buildpack web processes set 8080
// explicitly, so PORT is 8080, not a random Heroku-style value.
const DefaultListenPort = 5000

// ApplyListenPorts fills Port==0 with DefaultListenPort+i in place.
func ApplyListenPorts(ports []Port) {
	for i := range ports {
		if ports[i].Port == 0 {
			ports[i].Port = DefaultListenPort + i
		}
	}
}

// ListenPortEnv is PORT / PORT_0 / PORT_1 / … for the job. The first port
// is PORT (what web processes bind).
func ListenPortEnv(ports []Port) map[string]string {
	if len(ports) == 0 {
		return nil
	}
	env := make(map[string]string, len(ports)+1)
	for i, p := range ports {
		if p.Port <= 0 {
			continue
		}
		s := strconv.Itoa(p.Port)
		if i == 0 {
			env["PORT"] = s
		}
		env[fmt.Sprintf("PORT_%d", i)] = s
	}
	return env
}
