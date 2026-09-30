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
	"github.com/randy-girard/flynn/pkg/resname"
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
	code := hh.UnknownErrorCode
	switch status {
	case http.StatusBadRequest:
		code = hh.ValidationErrorCode
	case http.StatusNotFound:
		code = hh.ObjectNotFoundErrorCode
	case http.StatusForbidden:
		code = hh.ForbiddenErrorCode
	}
	return hh.JSONError{
		Code:    code,
		Message: fmt.Sprintf("resource: unexpected status code %d: %s", status, msg),
	}
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
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return err
	}
	if res.StatusCode != 200 {
		return provisionStatusError(res.StatusCode, body)
	}
	return nil
}

// DeprovisionIDs is ExternalID first, then the isolated app name from env
// (FLYNN_POSTGRES, FLYNN_MYSQL, …). After a plugin API restart the in-memory
// store is empty and Get(uuid) fails; Get(pg-orchid-xkhthp) hydrates from the
// live Flynn app.
func DeprovisionIDs(externalID string, env map[string]string) []string {
	seen := map[string]bool{}
	var ids []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		ids = append(ids, s)
	}
	add(externalID)
	if name, _ := resname.Identity(env); name != "" {
		add(name)
	}
	return ids
}

// DeprovisionAny DELETEs uri?id= for each id until one succeeds. A later id is
// tried only when the provider said the previous one was missing, not when
// followers still exist.
func DeprovisionAny(uri string, ids ...string) error {
	if len(ids) == 0 {
		return hh.JSONError{Code: hh.ValidationErrorCode, Message: "resource has no external id"}
	}
	var err error
	for i, id := range ids {
		err = Deprovision(uri, id)
		if err == nil {
			return nil
		}
		if i+1 < len(ids) && retryDeprovision(err) {
			continue
		}
		return err
	}
	return err
}

func retryDeprovision(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "follower") {
		return false
	}
	return strings.Contains(msg, "not found") || strings.Contains(msg, "404")
}
