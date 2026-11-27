package cli

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"

	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/overlay"
)

// applyFlannelRestoreEnv copies install-time flannel settings into the
// restored release. Empty variables leave the release alone. Values are
// checked and then written as quoted SQL literals (releaseEnvKeySQL). These variables do not
// change discoverd metadata that already exists.
func applyFlannelRestoreEnv(app *ct.ExpandedFormation, sqlBuf *bytes.Buffer) error {
	updates, err := flannelRestoreUpdates()
	if err != nil {
		return err
	}
	if len(updates) == 0 {
		return nil
	}
	if app == nil || app.Release == nil || app.Release.Env == nil {
		return errors.New("flannel release env is missing")
	}
	for _, u := range updates {
		app.Release.Env[u.key] = u.val
		sqlBuf.WriteString(releaseEnvKeySQL("flannel", u.key, u.val))
	}
	return nil
}

type flannelEnvUpdate struct {
	key string
	val string
}

func flannelRestoreUpdates() ([]flannelEnvUpdate, error) {
	var out []flannelEnvUpdate
	network := strings.TrimSpace(os.Getenv("FLANNEL_NETWORK"))
	if network != "" {
		if err := safeSQLValue("FLANNEL_NETWORK", network); err != nil {
			return nil, err
		}
		if _, ipnet, err := net.ParseCIDR(network); err != nil || ipnet == nil || ipnet.IP.To4() == nil {
			return nil, fmt.Errorf("invalid FLANNEL_NETWORK %q", network)
		}
		out = append(out, flannelEnvUpdate{key: "NETWORK", val: network})
	}
	for _, item := range []struct{ env, key string }{
		{"FLANNEL_SUBNET_LEN", "SUBNET_LEN"},
		{"FLANNEL_SUBNET_MIN", "SUBNET_MIN"},
		{"FLANNEL_SUBNET_MAX", "SUBNET_MAX"},
	} {
		raw := strings.TrimSpace(os.Getenv(item.env))
		if raw == "" {
			continue
		}
		if err := safeSQLValue(item.env, raw); err != nil {
			return nil, err
		}
		if item.key == "SUBNET_LEN" {
			if _, err := overlay.ParseSubnetLen(raw); err != nil {
				return nil, fmt.Errorf("invalid %s %q", item.env, raw)
			}
		} else if ip := net.ParseIP(raw); ip == nil || ip.To4() == nil {
			return nil, fmt.Errorf("invalid %s %q", item.env, raw)
		}
		out = append(out, flannelEnvUpdate{key: item.key, val: raw})
	}
	return out, nil
}

func safeSQLValue(name, val string) error {
	if strings.ContainsAny(val, "'\\;\n\r") {
		return fmt.Errorf("invalid %s %q", name, val)
	}
	return nil
}
