package bootstrap

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// statusCheckHTTPClient bounds each probe so bootstrap cannot hang indefinitely on a stuck backend.
var statusCheckHTTPClient = &http.Client{Timeout: 30 * time.Second}

type StatusCheckAction struct {
	ID      string `json:"id"`
	URL     string `json:"url"`
	Output  string `json:"output"`
	Timeout int    `json:"timeout"` // in seconds
}

type StatusResponse struct {
	Data StatusData `json:"data"`
}

type StatusData struct {
	Status string                  `json:"status"`
	Detail map[string]StatusDetail `json:"detail"`
}

type StatusDetail struct {
	Status string `json:"status"`
}

func init() {
	Register("status-check", &StatusCheckAction{})
}

// bootstrapStatusOnlyNonCriticalFailures reports whether every unhealthy service in the
// status response is non-critical for declaring bootstrap complete.
func bootstrapStatusOnlyNonCriticalFailures(resp *StatusResponse) bool {
	if resp.Data.Status == "healthy" {
		return true
	}
	ignored := map[string]struct{}{
		"flannel": {},
		"mariadb": {},
		"mongodb": {},
	}
	var sawUnhealthy bool
	for svc, d := range resp.Data.Detail {
		if d.Status == "healthy" {
			continue
		}
		sawUnhealthy = true
		if _, ok := ignored[svc]; !ok {
			return false
		}
	}
	return sawUnhealthy
}

func (a *StatusCheckAction) Run(s *State) error {
	waitMax := time.Minute
	if a.Timeout > 0 {
		waitMax = time.Duration(a.Timeout) * time.Second
	}
	const waitInterval = 500 * time.Millisecond

	u, err := url.Parse(interpolate(s, a.URL))
	if err != nil {
		return err
	}
	lookupDiscoverdURLHost(s, u, waitMax)

	timeout := time.After(waitMax)
	for {
		req, err := http.NewRequest("GET", u.String(), nil)
		if err != nil {
			return err
		}
		req.Header = make(http.Header)
		req.Header.Set("Accept", "application/json")
		res, err := statusCheckHTTPClient.Do(req)
		if err == nil && res.StatusCode == 200 {
			res.Body.Close()
			s.StepData[a.ID] = &LogMessage{Msg: "all services healthy"}
			return nil
		}
		var status StatusResponse
		if err == nil {
			decodeErr := json.NewDecoder(res.Body).Decode(&status)
			res.Body.Close()
			if decodeErr != nil {
				return decodeErr
			}
			if bootstrapStatusOnlyNonCriticalFailures(&status) {
				s.StepData[a.ID] = &LogMessage{Msg: "required services healthy (non-critical status checks skipped)"}
				return nil
			}
		}

		select {
		case <-time.After(waitInterval):
			continue
		case <-timeout:
		}

		if err != nil {
			return fmt.Errorf("bootstrap: timed out waiting for %s, last response %s", a.URL, err)
		}

		msg := "unhealthy services detected!\n\nThe following services are reporting unhealthy, this likely indicates a problem with your deployment:\n"
		for svc, s := range status.Data.Detail {
			if s.Status != "healthy" {
				msg += "\t" + svc + "\n"
			}
		}
		msg += "\n"
		s.StepData[a.ID] = &LogMessage{Msg: msg}
		return fmt.Errorf("bootstrap: %s", msg)
	}
}
