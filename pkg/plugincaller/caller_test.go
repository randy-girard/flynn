package plugincaller

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/randy-girard/flynn/controller/authorizer"
)

const testKey = "controller-key"

func signedRequest(t *testing.T, key string, c Caller, method, uri string, at time.Time) *http.Request {
	t.Helper()
	v, err := Sign(key, c, method, uri, at)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(method, uri, nil)
	r.Header.Set(Header, v)
	return r
}

func TestRoundTrip(t *testing.T) {
	in := Caller{UserID: "u1", Account: "user:u1", AppGrants: []AppGrant{{AppID: "app-1", Permissions: []string{"app:github:write"}}}}
	r := signedRequest(t, testKey, in, http.MethodGet, "/github/installations?x=1", time.Now())
	got, err := FromRequest(r, testKey)
	if err != nil {
		t.Fatal(err)
	}
	if got.Admin || got.UserID != "u1" || got.Account != "user:u1" || len(got.AppGrants) != 1 {
		t.Fatalf("%+v", got)
	}
}

func TestNoHeader(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	if _, err := FromRequest(r, testKey); !errors.Is(err, ErrNoCaller) {
		t.Fatalf("err = %v", err)
	}
}

// A client that sets the header itself has no controller key, so whatever it
// claims does not verify.
func TestForgedHeaderIsRefused(t *testing.T) {
	now := time.Now()
	raw, _ := json.Marshal(envelope{Caller: Caller{Admin: true}, Method: http.MethodGet, URI: "/github/installations", Exp: now.Add(time.Minute).Unix()})
	payload := base64.RawURLEncoding.EncodeToString(raw)
	cases := map[string]string{
		"unsigned":      "v1." + payload + ".",
		"wrong key":     mustSign(t, "other-key", Caller{Admin: true}, http.MethodGet, "/github/installations", now),
		"bad signature": "v1." + payload + "." + base64.RawURLEncoding.EncodeToString([]byte("nope")),
		"garbage":       "admin",
		"bad version":   strings.Replace(mustSign(t, testKey, Caller{Admin: true}, http.MethodGet, "/github/installations", now), "v1.", "v0.", 1),
	}
	for name, v := range cases {
		r := httptest.NewRequest(http.MethodGet, "/github/installations", nil)
		r.Header.Set(Header, v)
		if c, err := FromRequest(r, testKey); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: caller %+v err %v", name, c, err)
		}
	}
}

func TestTamperedPayloadIsRefused(t *testing.T) {
	v := mustSign(t, testKey, Caller{UserID: "u1"}, http.MethodGet, "/github/installations", time.Now())
	parts := strings.Split(v, ".")
	raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
	raw = []byte(strings.Replace(string(raw), `"admin":false`, `"admin":true`, 1))
	parts[1] = base64.RawURLEncoding.EncodeToString(raw)
	r := httptest.NewRequest(http.MethodGet, "/github/installations", nil)
	r.Header.Set(Header, strings.Join(parts, "."))
	if _, err := FromRequest(r, testKey); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v", err)
	}
}

// A value is bound to the request it was signed for.
func TestReplayOnOtherRequestIsRefused(t *testing.T) {
	v := mustSign(t, testKey, Caller{UserID: "u1"}, http.MethodGet, "/github/installations", time.Now())
	for _, tc := range []struct{ method, uri string }{
		{http.MethodPut, "/github/installations"},
		{http.MethodGet, "/github/installations/7/repos"},
		{http.MethodGet, "/github/installations?all=1"},
	} {
		r := httptest.NewRequest(tc.method, tc.uri, nil)
		r.Header.Set(Header, v)
		if _, err := FromRequest(r, testKey); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s %s: err = %v", tc.method, tc.uri, err)
		}
	}
}

func TestExpired(t *testing.T) {
	r := signedRequest(t, testKey, Caller{UserID: "u1"}, http.MethodGet, "/x", time.Now().Add(-TTL-clockLeeway-time.Second))
	if _, err := FromRequest(r, testKey); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v", err)
	}
	r = signedRequest(t, testKey, Caller{UserID: "u1"}, http.MethodGet, "/x", time.Now().Add(-TTL+time.Second))
	if _, err := FromRequest(r, testKey); err != nil {
		t.Fatalf("within ttl: %v", err)
	}
}

func TestDuplicateHeaderIsRefused(t *testing.T) {
	r := signedRequest(t, testKey, Caller{UserID: "u1"}, http.MethodGet, "/x", time.Now())
	r.Header.Add(Header, mustSign(t, testKey, Caller{Admin: true}, http.MethodGet, "/x", time.Now()))
	if _, err := FromRequest(r, testKey); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v", err)
	}
}

func TestEmptyKeyRefuses(t *testing.T) {
	if _, err := Sign("", Caller{}, http.MethodGet, "/x", time.Now()); err == nil {
		t.Fatal("signed with no key")
	}
	r := signedRequest(t, testKey, Caller{}, http.MethodGet, "/x", time.Now())
	if _, err := FromRequest(r, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v", err)
	}
}

func TestSetHeaderDropsClientValue(t *testing.T) {
	h := http.Header{}
	h.Add(Header, "forged")
	h.Add(Header, "forged2")
	SetHeader(h, "")
	if len(h.Values(Header)) != 0 {
		t.Fatalf("%v", h)
	}
	h.Add(Header, "forged")
	SetHeader(h, "real")
	if got := h.Values(Header); len(got) != 1 || got[0] != "real" {
		t.Fatalf("%v", got)
	}
}

func TestCanAndAppIDs(t *testing.T) {
	c := &Caller{AppGrants: []AppGrant{
		{AppID: "w", Permissions: []string{"app:write"}},
		{AppID: "g", Permissions: []string{"app:github:write"}},
		{AppID: "r", Permissions: []string{"app:read"}},
	}}
	if !c.Can("w", "app:github:write") || !c.Can("g", "app:github:write") || c.Can("r", "app:github:write") || c.Can("x", "app:read") {
		t.Fatal("Can")
	}
	ids := c.AppIDs("app:github:write")
	if strings.Join(ids, ",") != "w,g" {
		t.Fatalf("%v", ids)
	}
	if !(&Caller{Admin: true}).Can("any", "app:admin") || (&Caller{Admin: true}).AppIDs("app:read") != nil {
		t.Fatal("admin")
	}
	var none *Caller
	if none.Can("w", "app:read") {
		t.Fatal("nil caller")
	}
}

func TestFromToken(t *testing.T) {
	if FromToken(nil) != nil {
		t.Fatal("nil token")
	}
	c := FromToken(&authorizer.Token{UserID: "u1", AppGrants: []authorizer.AppGrant{{AppID: "a", Permissions: []string{"app:read"}}, {AppID: "b"}}})
	if c.Admin || c.UserID != "u1" || c.Account != "user:u1" || len(c.AppGrants) != 1 || c.AppGrants[0].AppID != "a" {
		t.Fatalf("%+v", c)
	}
	if c := FromToken(&authorizer.Token{ClusterKey: true}); !c.Admin || c.Account != "" {
		t.Fatalf("%+v", c)
	}
	if c := FromToken(&authorizer.Token{UserID: "admin", Scopes: []string{"cluster:admin"}, AppGrants: []authorizer.AppGrant{{AppID: "a", Permissions: []string{"app:read"}}}}); !c.Admin || len(c.AppGrants) != 0 {
		t.Fatalf("%+v", c)
	}
}

func mustSign(t *testing.T, key string, c Caller, method, uri string, at time.Time) string {
	t.Helper()
	v, err := Sign(key, c, method, uri, at)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
