package cliutil

import (
	"reflect"
	"testing"

	"github.com/flynn/go-docopt"
)

func TestListAndStringAcceptStringOrSlice(t *testing.T) {
	single := &docopt.Args{
		All:    map[string]interface{}{"<var>": "FOO"},
		String: map[string]string{"<var>": "FOO"},
	}
	if got := List(single, "<var>"); !reflect.DeepEqual(got, []string{"FOO"}) {
		t.Fatalf("single List: %q", got)
	}
	if got := String(single, "<var>"); got != "FOO" {
		t.Fatalf("single String: %q", got)
	}

	many := &docopt.Args{All: map[string]interface{}{"<var>": []string{"A", "B"}}}
	if got := List(many, "<var>"); !reflect.DeepEqual(got, []string{"A", "B"}) {
		t.Fatalf("ellipsis List: %q", got)
	}

	empty := &docopt.Args{All: map[string]interface{}{"<argument>": nil}}
	if got := List(empty, "<argument>"); len(got) != 0 {
		t.Fatalf("nil List: %q", got)
	}
}
