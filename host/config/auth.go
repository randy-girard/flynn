package config

import (
	"os"
)

const DefaultPath = "/etc/flynn/host.json"

// LoadAuthKey returns FLYNN_HOST_AUTH_KEY from the host config file, if set.
func LoadAuthKey(file string) (string, error) {
	if file == "" {
		file = DefaultPath
	}
	c, err := Open(file)
	if err != nil {
		// A missing config file is normal. A permission-denied error is also
		// expected when a non-root user runs a flynn-host CLI subcommand (the
		// config is root-only 0600); in that case fall through unauthenticated
		// and let the daemon return a clear 401 if auth is required, rather
		// than making the CLI fatal.
		if os.IsNotExist(err) || os.IsPermission(err) {
			return "", nil
		}
		return "", err
	}
	if c.Env == nil {
		return "", nil
	}
	return c.Env["FLYNN_HOST_AUTH_KEY"], nil
}

// SetAuthKey persists key in the host config file env map.
func SetAuthKey(file, key string) error {
	if file == "" {
		file = DefaultPath
	}
	conf := New()
	if existing, err := Open(file); err == nil {
		conf = existing
	}
	if conf.Env == nil {
		conf.Env = make(map[string]string)
	}
	conf.Env["FLYNN_HOST_AUTH_KEY"] = key
	if err := conf.WriteTo(file); err != nil {
		return err
	}
	return os.Chmod(file, 0600)
}
