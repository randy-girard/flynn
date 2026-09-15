package mysqlurl

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

// DSN is a URL-formatted MySQL/MariaDB data source name. Integration tests use
// this instead of importing the MariaDB plugin.
type DSN struct {
	Host     string
	User     string
	Password string
	Database string
	Timeout  time.Duration
}

// String encodes dsn to the go-sql-driver/mysql URL format.
func (dsn *DSN) String() string {
	u := url.URL{
		Host: fmt.Sprintf("tcp(%s)", dsn.Host),
		Path: "/" + dsn.Database,
		RawQuery: url.Values{
			"timeout": {dsn.Timeout.String()},
		}.Encode(),
	}
	if dsn.Password == "" {
		u.User = url.User(dsn.User)
	} else {
		u.User = url.UserPassword(dsn.User, dsn.Password)
	}
	return strings.TrimPrefix(u.String(), "//")
}
