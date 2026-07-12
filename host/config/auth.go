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
		if os.IsNotExist(err) {
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
