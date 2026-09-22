package httpclient

import (
	"strings"
)

// IsUnauthorized reports whether err is an HTTP 401 from this client
// (including "httpclient: raw req: unexpected status 401"). Auth failures
// are not transient; callers must not retry them as blobstore/health blips.
func IsUnauthorized(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "unexpected status 401")
}
