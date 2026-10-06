package main

import (
	"net/http"
	"testing"
	"time"

	ct "github.com/randy-girard/flynn/controller/types"
)

func TestParseListPage(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "/apps/a/jobs?count=40&before=2026-05-22T10:00:00Z&before_id=job-1&state=active&exclude_internal=1", nil)
	if err != nil {
		t.Fatal(err)
	}
	p, err := parseListPage(req)
	if err != nil {
		t.Fatal(err)
	}
	if p.Count != 40 || p.BeforeID != "job-1" || p.State != "active" || !p.ExcludeInternal || !p.paged() {
		t.Fatalf("%+v", p)
	}
	want := time.Date(2026, 5, 22, 10, 0, 0, 0, time.UTC)
	if p.Before == nil || !p.Before.Equal(want) {
		t.Fatalf("before=%v want %v", p.Before, want)
	}
}

func TestParseListPageInvalidCount(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "/apps/a/jobs?count=0", nil)
	_, err := parseListPage(req)
	if _, ok := err.(ct.ValidationError); !ok {
		t.Fatalf("err=%v", err)
	}
}

func TestParseListPageInvalidBefore(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "/apps/a/jobs?before=not-a-time", nil)
	_, err := parseListPage(req)
	if _, ok := err.(ct.ValidationError); !ok {
		t.Fatalf("err=%v", err)
	}
}
