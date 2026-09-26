package resource

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	hh "github.com/randy-girard/flynn/pkg/httphelper"
)

type Resource struct {
	ID  string            `json:"id"`
	Env map[string]string `json:"env"`
}

func Provision(uri string, config []byte) (*Resource, error) {
	res, err := hh.ProvisionClient.Post(uri, "application/json", bytes.NewBuffer(config))
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if res.StatusCode != 200 {
		return nil, provisionStatusError(res.StatusCode, body)
	}

	resource := &Resource{}
	if err := json.Unmarshal(body, resource); err != nil {
		return nil, err
	}
	return resource, nil
}

func provisionStatusError(status int, body []byte) error {
	var jsonErr hh.JSONError
	if json.Unmarshal(body, &jsonErr) == nil && jsonErr.Code != "" {
		return jsonErr
	}
	msg := strings.TrimSpace(string(body))
	if msg == "" {
		msg = http.StatusText(status)
	}
	if len(msg) > 200 {
		msg = msg[:200]
	}
	return fmt.Errorf("resource: unexpected status code %d: %s", status, msg)
}

func Deprovision(uri, id string) error {
	path := fmt.Sprintf("%s?id=%s", uri, url.QueryEscape(id))
	req, err := http.NewRequest("DELETE", path, nil)
	if err != nil {
		return err
	}
	res, err := hh.RetryClient.Do(req)
	if err != nil {
		return err
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("resource: unexpected status code %d", res.StatusCode)
	}
	return nil
}
