package config

import (
	"os"
)

const DefaultPath = "/etc/flynn/host.json"

var cliSecretEnv = []string{
	"FLYNN_HOST_AUTH_KEY",
	"DISCOVERD_AUTH_KEY",
	"AUTH_KEY",
	"CONTROLLER_KEY",
}

// LoadAuthKey returns FLYNN_HOST_AUTH_KEY from the host config file, if set.
func LoadAuthKey(file string) (string, error) {
	return LoadEnvKey(file, "FLYNN_HOST_AUTH_KEY")
}

// LoadEnvKey returns name from the host config file env map, if set.
func LoadEnvKey(file, name string) (string, error) {
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
	return c.Env[name], nil
}

// ApplySecretsToEnv copies cluster secrets from the host config file into the
// process environment when they are not already set, so flynn-host CLI
// subcommands can authenticate to the host API, discoverd, and controller.
func ApplySecretsToEnv(file string) error {
	if file == "" {
		file = DefaultPath
	}
	c, err := Open(file)
	if err != nil {
		if os.IsNotExist(err) || os.IsPermission(err) {
			return nil
		}
		return err
	}
	if c.Env == nil {
		return nil
	}
	for _, k := range cliSecretEnv {
		if os.Getenv(k) == "" && c.Env[k] != "" {
			os.Setenv(k, c.Env[k])
		}
	}
	return nil
}

// SetAuthKey persists key in the host config file env map.
func SetAuthKey(file, key string) error {
	return SetEnv(file, map[string]string{"FLYNN_HOST_AUTH_KEY": key})
}

// SetEnv merges kv into the host config file env map and chmod 0600.
func SetEnv(file string, kv map[string]string) error {
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
	for k, v := range kv {
		if k == "" {
			continue
		}
		if v == "" {
			delete(conf.Env, k)
			continue
		}
		conf.Env[k] = v
	}
	if err := conf.WriteTo(file); err != nil {
		return err
	}
	return os.Chmod(file, 0600)
}
