package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/flynn/go-docopt"
	controller "github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
)

type domainCall struct {
	method, path, query, body string
}

func domainTestServer(t *testing.T, reply func(r *http.Request) (int, interface{})) (controller.Client, *[]domainCall) {
	t.Helper()
	var calls []domainCall
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		calls = append(calls, domainCall{r.Method, r.URL.Path, r.URL.RawQuery, string(raw)})
		code, out := reply(r)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		if out != nil {
			_ = json.NewEncoder(w).Encode(out)
		}
	}))
	t.Cleanup(srv.Close)
	c, err := controller.NewClient(srv.URL, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	return c, &calls
}

func TestDomainListUsesOwnerAccount(t *testing.T) {
	now := time.Now()
	c, calls := domainTestServer(t, func(r *http.Request) (int, interface{}) {
		return 200, []ct.DomainVerification{
			{Hostname: "example.com", OwnerAccount: "user:u1", Token: "tok1", TXTName: "_flynn-verify.example.com", VerifiedAt: &now, ClaimedAt: &now},
			{Hostname: "shop.example.org", OwnerAccount: "user:u1", Token: "tok2", TXTName: "_flynn-verify.shop.example.org", ClaimedAt: &now},
		}
	})
	var out bytes.Buffer
	args := &docopt.Args{String: map[string]string{"--owner": "user:u1"}, Bool: map[string]bool{}}
	if err := domainList(args, c, &out); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 || (*calls)[0].method != "GET" || (*calls)[0].path != "/domains" || (*calls)[0].query != "owner_account=user%3Au1" {
		t.Fatalf("calls = %+v", *calls)
	}
	got := out.String()
	for _, want := range []string{"HOSTNAME", "example.com", "verified", "shop.example.org", "pending", "_flynn-verify.shop.example.org", "tok2"} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
}

func TestDomainListAllSendsNoOwner(t *testing.T) {
	c, calls := domainTestServer(t, func(r *http.Request) (int, interface{}) { return 200, []ct.DomainVerification{} })
	args := &docopt.Args{String: map[string]string{}, Bool: map[string]bool{"--all": true}}
	if err := domainList(args, c, io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 || (*calls)[0].path != "/domains" || (*calls)[0].query != "" {
		t.Fatalf("calls = %+v", *calls)
	}
}

func TestDomainClaimPrintsTXTRecord(t *testing.T) {
	c, calls := domainTestServer(t, func(r *http.Request) (int, interface{}) {
		return 200, ct.DomainVerification{Hostname: "example.com", OwnerAccount: "org:o1", Token: "abc123", TXTName: "_flynn-verify.example.com"}
	})
	var out bytes.Buffer
	args := &docopt.Args{String: map[string]string{"<hostname>": "Example.com", "--owner": "org:o1"}, Bool: map[string]bool{}}
	if err := domainClaim(args, c, &out); err != nil {
		t.Fatal(err)
	}
	call := (*calls)[0]
	if call.method != "POST" || call.path != "/domains/verify" {
		t.Fatalf("call = %+v", call)
	}
	var body map[string]string
	if err := json.Unmarshal([]byte(call.body), &body); err != nil || body["hostname"] != "Example.com" || body["owner_account"] != "org:o1" {
		t.Fatalf("body = %s", call.body)
	}
	for _, want := range []string{"_flynn-verify.example.com", "abc123", "flynn domain:check example.com"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, out.String())
		}
	}
}

func TestDomainCheckAndRemovePaths(t *testing.T) {
	now := time.Now()
	c, calls := domainTestServer(t, func(r *http.Request) (int, interface{}) {
		if r.Method == "DELETE" {
			return 200, nil
		}
		return 200, ct.DomainVerification{Hostname: "example.com", VerifiedAt: &now}
	})
	var out bytes.Buffer
	args := &docopt.Args{String: map[string]string{"<hostname>": "example.com"}, Bool: map[string]bool{}}
	if err := domainCheck(args, c, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "example.com is verified") {
		t.Fatalf("output = %q", out.String())
	}
	if err := domainRemove(args, c); err != nil {
		t.Fatal(err)
	}
	want := []domainCall{
		{"POST", "/domains/verify/example.com/check", "", "{}\n"},
		{"DELETE", "/domains/example.com", "", ""},
	}
	if len(*calls) != 2 {
		t.Fatalf("calls = %+v", *calls)
	}
	for i, w := range want {
		g := (*calls)[i]
		if g.method != w.method || g.path != w.path {
			t.Fatalf("call %d = %+v, want %+v", i, g, w)
		}
	}
}

func TestDomainCommandsRegistered(t *testing.T) {
	for _, name := range []string{"domain", "domain:list", "domain:claim", "domain:check", "domain:remove"} {
		if commands[name] == nil {
			t.Errorf("missing %s", name)
		}
	}
	if !strings.Contains(formatHelp("domain:claim"), "Examples:") {
		t.Fatalf("domain:claim help needs examples:\n%s", formatHelp("domain:claim"))
	}
}

// TestDomainListShowsLastCheck: CORE-56. A verified name is re-checked when
// its last TXT check is old; the list shows when that was.
func TestDomainListShowsLastCheck(t *testing.T) {
	checked := time.Date(2026, 11, 20, 9, 30, 0, 0, time.UTC)
	c, _ := domainTestServer(t, func(r *http.Request) (int, interface{}) {
		return 200, []ct.DomainVerification{
			{Hostname: "example.com", OwnerAccount: "user:u1", Token: "tok1", TXTName: "_flynn-verify.example.com", VerifiedAt: &checked, CheckedAt: &checked},
			{Hostname: "pending.example.com", OwnerAccount: "user:u1", Token: "tok2", TXTName: "_flynn-verify.pending.example.com"},
		}
	})
	var out bytes.Buffer
	args := &docopt.Args{String: map[string]string{"--owner": "user:u1"}, Bool: map[string]bool{}}
	if err := domainList(args, c, &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 || !strings.Contains(lines[0], "CHECKED") || !strings.Contains(lines[1], "2026-11-20T09:30:00Z") {
		t.Fatalf("output:\n%s", out.String())
	}
	if !strings.Contains(lines[2], " - ") {
		t.Fatalf("pending row needs a - for CHECKED:\n%s", out.String())
	}
}
