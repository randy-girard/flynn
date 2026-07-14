package httpclient

import (
	"io"
	"net/http"
	"os"
)

// PostWithHostAuth sends a POST request, using HTTP basic auth when
// FLYNN_HOST_AUTH_KEY is set in the environment.
func PostWithHostAuth(url, contentType string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequest("POST", url, body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if key := os.Getenv("FLYNN_HOST_AUTH_KEY"); key != "" {
		req.SetBasicAuth("", key)
	}
	return http.DefaultClient.Do(req)
}
