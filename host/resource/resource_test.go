package resource

import (
	"reflect"
	"testing"

	"github.com/docker/go-units"
	. "github.com/flynn/go-check"
	"github.com/randy-girard/flynn/pkg/typeconv"
)

// Hook gocheck up to the "go test" runner
func Test(t *testing.T) { TestingT(t) }

type S struct{}

var _ = Suite(S{})

func assertDefault(c *C, r Resources, types ...Type) {
	for _, typ := range types {
		actual, ok := r[typ]
		if !ok {
			c.Fatalf("%s resource not set", typ)
		}
		expected := defaults[typ]
		if !reflect.DeepEqual(actual, expected) {
			c.Fatalf("%s resource is not default, expected: %+v, actual: %+v", typ, expected, actual)
		}
	}
}

func (S) TestSetDefaultsNil(c *C) {
	var r Resources
	SetDefaults(&r)
	c.Assert(r, NotNil)
}

func (S) TestSetDefaultsEmpty(c *C) {
	r := make(Resources)
	SetDefaults(&r)
	assertDefault(c, r, TypeMemory, TypeMaxFD, TypeMaxProcs)
}

func (S) TestDefaultsIncludeMaxProcs(c *C) {
	r := Defaults()
	spec, ok := r[TypeMaxProcs]
	if !ok || spec.Limit == nil || spec.Request == nil {
		c.Fatal("max_procs default not set")
	}
	c.Assert(*spec.Limit, Equals, DefaultPidsLimit)
	c.Assert(*spec.Request, Equals, DefaultPidsLimit)
	c.Assert(DefaultPidsLimit, Equals, int64(4096))
}

func (S) TestSetDefaultsPreservesMaxProcs(c *C) {
	r := Resources{TypeMaxProcs: Spec{Limit: typeconv.Int64Ptr(128)}}
	SetDefaults(&r)
	c.Assert(*r[TypeMaxProcs].Limit, Equals, int64(128))
	c.Assert(*r[TypeMaxProcs].Request, Equals, int64(128))
	assertDefault(c, r, TypeMemory, TypeMaxFD)
}

func (S) TestToTypeMaxProcs(c *C) {
	typ, ok := ToType("max_procs")
	c.Assert(ok, Equals, true)
	c.Assert(typ, Equals, TypeMaxProcs)
}

func (S) TestParseMaxProcs(c *C) {
	r, err := Parse([]string{"max_procs=8192"})
	c.Assert(err, IsNil)
	c.Assert(*r[TypeMaxProcs].Limit, Equals, int64(8192))
}

func (S) TestPidsLimit(c *C) {
	c.Assert(PidsLimit(nil), Equals, DefaultPidsLimit)
	c.Assert(PidsLimit(Resources{}), Equals, DefaultPidsLimit)
	c.Assert(PidsLimit(Resources{TypeMaxProcs: Spec{}}), Equals, DefaultPidsLimit)
	c.Assert(PidsLimit(Resources{TypeMaxProcs: Spec{Limit: typeconv.Int64Ptr(0)}}), Equals, DefaultPidsLimit)
	c.Assert(PidsLimit(Resources{TypeMaxProcs: Spec{Limit: typeconv.Int64Ptr(-1)}}), Equals, DefaultPidsLimit)
	c.Assert(PidsLimit(Resources{TypeMaxProcs: Spec{Limit: typeconv.Int64Ptr(128)}}), Equals, int64(128))
	c.Assert(PidsLimit(Defaults()), Equals, DefaultPidsLimit)
}

func (S) TestSetDefaultsRequest(c *C) {
	// not specifying Request should default it to the value of Limit
	r := Resources{TypeMemory: Spec{Limit: typeconv.Int64Ptr(512 * units.MiB)}}
	SetDefaults(&r)
	assertDefault(c, r, TypeMaxFD, TypeMaxProcs)
	mem, ok := r[TypeMemory]
	if !ok {
		c.Fatal("memory resource not set")
	}
	c.Assert(*mem.Request, Equals, *mem.Limit)
}
