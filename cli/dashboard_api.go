package main

import (
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	cfg "github.com/randy-girard/flynn/cli/config"
	"github.com/randy-girard/flynn/pkg/pinned"
)

func dashboardAPI() (*http.Client, string, string, error) {
	cluster, err := getCluster()
	if err != nil {
		return nil, "", "", err
	}
	base, err := dashboardBaseURL(cluster)
	if err != nil {
		return nil, "", "", err
	}
	key := strings.TrimSpace(cluster.Key)
	if key == "" {
		return nil, "", "", fmt.Errorf("cluster controller key is required to call the dashboard from the CLI (use flynn cluster:add)")
	}
	hc, err := dashboardHTTPClient(cluster)
	if err != nil {
		return nil, "", "", err
	}
	return hc, base, key, nil
}

func dashboardBaseURL(cluster *cfg.Cluster) (string, error) {
	if cluster == nil {
		return "", fmt.Errorf("no cluster configured")
	}
	for _, raw := range []string{cluster.DashboardURL, cluster.OAuthURL} {
		if base := strings.TrimRight(strings.TrimSpace(raw), "/"); base != "" {
			return base, nil
		}
	}
	if derived := dashboardURLFromController(cluster.ControllerURL); derived != "" {
		return derived, nil
	}
	return "", fmt.Errorf("dashboard URL is not configured; set DashboardURL in ~/.flynnrc")
}

func dashboardURLFromController(controllerURL string) string {
	u, err := url.Parse(strings.TrimSpace(controllerURL))
	if err != nil || u.Host == "" {
		return ""
	}
	host := u.Host
	switch {
	case strings.HasPrefix(host, "controller."):
		u.Host = "dashboard." + strings.TrimPrefix(host, "controller.")
	case strings.Contains(host, ".controller."):
		u.Host = strings.Replace(host, ".controller.", ".dashboard.", 1)
	default:
		return ""
	}
	u.Path = ""
	u.RawQuery = ""
	u.Fragment = ""
	return strings.TrimRight(u.String(), "/")
}

func dashboardHTTPClient(cluster *cfg.Cluster) (*http.Client, error) {
	if cluster == nil || strings.TrimSpace(cluster.TLSPin) == "" {
		return http.DefaultClient, nil
	}
	pin, err := base64.StdEncoding.DecodeString(cluster.TLSPin)
	if err != nil {
		return nil, fmt.Errorf("error decoding tls pin: %s", err)
	}
	d := &pinned.Config{Pin: pin}
	if host := dashboardTLSServerName(cluster); host != "" {
		d.Config = &tls.Config{ServerName: host}
	}
	return &http.Client{Transport: &http.Transport{DialTLS: d.Dial}}, nil
}

func dashboardTLSServerName(cluster *cfg.Cluster) string {
	for _, raw := range []string{cluster.DashboardURL, cluster.OAuthURL, cluster.ControllerURL} {
		u, err := url.Parse(strings.TrimSpace(raw))
		if err == nil && u.Hostname() != "" {
			return u.Hostname()
		}
	}
	return ""
}

func dashboardDoJSON(client *http.Client, method, rawURL, key string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, rawURL, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.SetBasicAuth("", key)
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 300 {
		msg := strings.TrimSpace(string(raw))
		if msg == "" {
			msg = res.Status
		}
		var wrapped struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(raw, &wrapped) == nil && wrapped.Error != "" {
			msg = wrapped.Error
		}
		return fmt.Errorf("dashboard: %s", msg)
	}
	if out == nil || res.StatusCode == http.StatusNoContent || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	return json.Unmarshal(raw, out)
}
