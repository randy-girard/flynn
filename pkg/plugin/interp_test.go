package plugin

import "testing"

func TestInterpolateInsertsEnvOnce(t *testing.T) {
	in := Interp{
		App: map[string]string{
			"REDIS_HOST":     "leader.redis-abc.discoverd",
			"REDIS_PASSWORD": "p${resource}w",
		},
		Resource: "redis-abc",
	}
	got, err := Interpolate("${app.REDIS_PASSWORD}", in)
	if err != nil {
		t.Fatal(err)
	}
	if got != "p${resource}w" {
		t.Fatalf("password must not be re-expanded: %q", got)
	}
	got, err = Interpolate("${app.REDIS_HOST|leader.${resource}.discoverd}", in)
	if err != nil || got != "leader.redis-abc.discoverd" {
		t.Fatalf("host=%q err=%v", got, err)
	}
}

func TestInterpolateFallbackWhenEnvEmpty(t *testing.T) {
	in := Interp{App: map[string]string{}, Resource: "redis-xyz"}
	got, err := Interpolate("${app.REDIS_HOST|leader.${resource}.discoverd}", in)
	if err != nil || got != "leader.redis-xyz.discoverd" {
		t.Fatalf("fallback=%q err=%v", got, err)
	}
	if _, err := Interpolate("${app.REDIS_HOST|${app.REDIS_PASSWORD}}", in); err == nil {
		t.Fatal("fallback must not expand app env")
	}
	if _, err := Interpolate("${unknown}", in); err == nil {
		t.Fatal("unknown placeholder")
	}
}

func TestCLIRunnableAndAction(t *testing.T) {
	c := &CLI{Command: "redis", Doc: "usage: flynn redis dump", Actions: []CLIAction{{Name: "dump", Args: []string{"/bin/dump"}}}}
	if !c.Runnable() || c.Action("dump") == nil || c.Action("missing") != nil {
		t.Fatalf("%+v", c)
	}
	if (*CLI)(nil).Runnable() {
		t.Fatal("nil")
	}
}
