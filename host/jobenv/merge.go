// Package jobenv merges the environment written into a container config.
package jobenv

import "github.com/randy-girard/flynn/pkg/netpolicy"

// ContainerEnv is the environment written to /.containerconfig and inherited
// by the workload. User and build jobs must not carry DISCOVERD_AUTH_KEY:
// containerinit only hides it from the user child process, and build jobs
// keep the full environment. System and datastore jobs still register with
// discoverd themselves, so they keep the key.
func ContainerEnv(class netpolicy.Class, envs ...map[string]string) map[string]string {
	out := Merge(envs...)
	if class == netpolicy.ClassUser || class == netpolicy.ClassBuild {
		delete(out, "DISCOVERD_AUTH_KEY")
	}
	return out
}

// Merge applies env maps in order. A blank DISCOVERD address or auth key
// must not replace the host default: an isolated datastore release created
// without the cluster URL then dials 127.0.0.1:1111, the register fails, and
// the job crash-loops with no leader.
func Merge(envs ...map[string]string) map[string]string {
	out := make(map[string]string)
	for _, e := range envs {
		for k, v := range e {
			if v == "" && (k == "DISCOVERD" || k == "DISCOVERD_AUTH_KEY") {
				continue
			}
			out[k] = v
		}
	}
	return out
}
