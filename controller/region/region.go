// Package region names this Flynn cluster and checks a link to another one.
// A region is a separate install. It is not a scheduler placement tag.
package region

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/pinned"
)

const (
	// DefaultName is the region stored when bootstrap omits --region.
	DefaultName = "US"
	// MaxLinks bounds cluster_region_links. region:list and the dashboard
	// read the rows and do not dial them. Only link dials, and only the one
	// peer in that command. 64 is enough for a large company and is not an
	// unbounded table.
	MaxLinks = 64
	// maxPeerBody is the region label plus one trailing newline.
	maxPeerBody = 64
)

// ErrPeerClusterKey means the peer sent a cluster key. The key is not stored
// and is not included in this error.
var ErrPeerClusterKey = errors.New("peer offered a cluster key")

var (
	errLabel  = errors.New("region must be a short label such as US, not a URL")
	errDomain = errors.New("domain must be the peer cluster domain, not a URL path")
	errPin    = errors.New("tls pin must be the base64 sha256 of the peer leaf certificate")
	errCap    = errors.New("at most 64 linked regions")
	errSelf   = errors.New("region is already this cluster")
	errMatch  = errors.New("peer region does not match")
	errBody   = errors.New("peer response must contain only the region name")
)

var (
	labelRe  = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9-]{0,31}$`)
	domainRe = regexp.MustCompile(`^(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
)

// Normalize rejects an empty string, a URL, and a hostname. The stored label
// keeps the operator's casing. US is the usual example.
func Normalize(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" || strings.Contains(s, "://") || strings.ContainsAny(s, "/. :") || !labelRe.MatchString(s) {
		return "", errLabel
	}
	return s, nil
}

// NormalizeOrDefault maps an omitted or empty flag to US.
func NormalizeOrDefault(s string) (string, error) {
	if strings.TrimSpace(s) == "" {
		return DefaultName, nil
	}
	return Normalize(s)
}

// BootstrapApply reports whether a full bootstrap should store a region.
// --from-backup keeps the region in the dump. A partial --steps run does not
// write the flag. Omitted and empty store US.
func BootstrapApply(fromBackup, partial bool, flag string) (apply bool, name string, err error) {
	if fromBackup || partial {
		return false, "", nil
	}
	name, err = NormalizeOrDefault(flag)
	if err != nil {
		return false, "", err
	}
	return true, name, nil
}

// LinkInput is the operator's link command plus what the peer dial returned.
// Existing is the region names already stored. PeerErr is the dial error.
type LinkInput struct {
	Region     string
	Domain     string
	TLSPin     string
	Current    string
	Existing   []string
	PeerRegion string
	PeerErr    error
}

// PrepareLink checks the label, the cap, and the peer name. The result has
// no cluster key. Controller and dashboard URLs are derived from the domain.
func PrepareLink(in LinkInput) (ct.RegionLink, error) {
	name, err := Normalize(in.Region)
	if err != nil {
		return ct.RegionLink{}, err
	}
	domain, err := ParseDomain(in.Domain)
	if err != nil {
		return ct.RegionLink{}, err
	}
	if _, err := ParsePin(in.TLSPin); err != nil {
		return ct.RegionLink{}, err
	}
	current, err := Normalize(in.Current)
	if err != nil {
		return ct.RegionLink{}, err
	}
	if name == current {
		return ct.RegionLink{}, errSelf
	}
	replacing := false
	for _, have := range in.Existing {
		if have == name {
			replacing = true
			break
		}
	}
	if !replacing && len(in.Existing) >= MaxLinks {
		return ct.RegionLink{}, errCap
	}
	if in.PeerErr != nil {
		return ct.RegionLink{}, in.PeerErr
	}
	if in.PeerRegion != name {
		return ct.RegionLink{}, errMatch
	}
	return ct.RegionLink{
		Region:        name,
		Domain:        domain,
		ControllerURL: "https://controller." + domain,
		DashboardURL:  "https://dashboard." + domain,
		TLSPin:        strings.TrimSpace(in.TLSPin),
	}, nil
}

// ParseDomain accepts a cluster domain. A controller URL is reduced to the
// domain. A path is rejected so the stored URLs are only the ones we dial.
func ParseDomain(raw string) (string, error) {
	s := strings.TrimSpace(strings.ToLower(raw))
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "http://")
	s = strings.TrimPrefix(s, "controller.")
	s = strings.TrimSuffix(s, "/")
	if s == "" || strings.ContainsAny(s, "/ :") || !domainRe.MatchString(s) {
		return "", errDomain
	}
	return s, nil
}

// ParsePin decodes the same base64 sha256 leaf pin flynn cluster:add stores.
func ParsePin(s string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil || len(raw) != sha256.Size {
		return nil, errPin
	}
	return raw, nil
}

// WriteName is the public peer response: the region label and a newline.
func WriteName(w http.ResponseWriter, name string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, name+"\n")
}

// FetchPeerRegion dials GET /cluster/region on the peer. It does not send
// CONTROLLER_KEY. The TLS pin must match the leaf. A cluster key in the body
// or headers is refused and is not returned.
func FetchPeerRegion(controllerURL, pinB64 string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(controllerURL))
	if err != nil || u.Host == "" || u.Scheme != "https" {
		return "", errors.New("peer controller URL must be https")
	}
	u.Path = "/cluster/region"
	u.Fragment = ""
	pin, err := ParsePin(pinB64)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	d := &pinned.Config{
		Pin:    pin,
		Config: &tls.Config{ServerName: u.Hostname(), MinVersion: tls.VersionTLS12},
	}
	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			DialTLS: d.DialOnce,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	res, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if offersClusterKey("", res.Header) {
		return "", ErrPeerClusterKey
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, maxPeerBody+1))
	if err != nil {
		return "", err
	}
	if res.StatusCode != http.StatusOK {
		return "", errBody
	}
	return readPeerBody(body)
}

func readPeerBody(body []byte) (string, error) {
	if len(body) > maxPeerBody {
		return "", errBody
	}
	text := string(body)
	if offersClusterKey(text, nil) {
		return "", ErrPeerClusterKey
	}
	if strings.Contains(strings.ToLower(text), "{") {
		return "", errBody
	}
	text = strings.TrimSuffix(text, "\n")
	text = strings.TrimSuffix(text, "\r")
	name, err := Normalize(text)
	if err != nil {
		return "", errBody
	}
	return name, nil
}

func offersClusterKey(body string, header http.Header) bool {
	low := strings.ToLower(body)
	for _, needle := range []string{"controller_key", "cluster_key", "controller-key", "auth_key", `"key"`} {
		if strings.Contains(low, needle) {
			return true
		}
	}
	for k, vals := range header {
		lk := strings.ToLower(k)
		if strings.Contains(lk, "controller-key") || strings.Contains(lk, "cluster-key") || lk == "x-auth-key" {
			return true
		}
		for _, v := range vals {
			if offersClusterKey(v, nil) {
				return true
			}
		}
	}
	return false
}
