// Package jobenv merges the environment written into a container config.
package jobenv

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
