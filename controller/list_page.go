package main

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	ct "github.com/randy-girard/flynn/controller/types"
)

const maxListPageCount = 1000

type listPage struct {
	Count           int
	Before          *time.Time
	BeforeID        string
	State           string
	ExcludeInternal bool
}

func (p *listPage) paged() bool {
	return p != nil && p.Count > 0
}

func parseListPage(req *http.Request) (*listPage, error) {
	q := req.URL.Query()
	p := &listPage{State: q.Get("state"), BeforeID: q.Get("before_id")}
	switch strings.ToLower(strings.TrimSpace(q.Get("exclude_internal"))) {
	case "1", "true", "yes":
		p.ExcludeInternal = true
	}
	if v := q.Get("count"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return nil, ct.ValidationError{Field: "count", Message: "must be a positive integer"}
		}
		if n > maxListPageCount {
			n = maxListPageCount
		}
		p.Count = n
	}
	if v := q.Get("before"); v != "" {
		t, err := parseListBefore(v)
		if err != nil {
			return nil, ct.ValidationError{Field: "before", Message: "must be an RFC3339 timestamp"}
		}
		p.Before = &t
	}
	return p, nil
}

func parseListBefore(v string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
		return t.UTC(), nil
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Time{}, err
	}
	return t.UTC(), nil
}
