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

func TestCLIMatchActionLongestWins(t *testing.T) {
	cli := &CLI{Actions: []CLIAction{
		{Name: "topics"},
		{Name: "topics create"},
		{Name: ""},
	}}
	cases := []struct {
		bools map[string]bool
		want  string
	}{
		{map[string]bool{"topics": true}, "topics"},
		{map[string]bool{"topics": true, "create": true}, "topics create"},
		{map[string]bool{"create": true}, ""},
		{nil, ""},
	}
	for _, tc := range cases {
		got := cli.MatchAction(tc.bools)
		name := ""
		if got != nil {
			name = got.Name
		}
		if name != tc.want {
			t.Fatalf("bools=%v got %q want %q", tc.bools, name, tc.want)
		}
	}
	if (*CLI)(nil).MatchAction(map[string]bool{"topics": true}) != nil {
		t.Fatal("nil CLI")
	}
}

func TestInterpolateRejectsInvalidIdentAndInterpolateAll(t *testing.T) {
	in := Interp{App: map[string]string{"OK": "1", "REDIS_PASSWORD": "p${resource}w"}}
	for _, bad := range []string{"${app.FOO-BAR}", "${app.9X}", "${app.}", "${app.OK;rm}"} {
		if _, err := Interpolate(bad, in); err == nil {
			t.Fatalf("must reject %s", bad)
		}
	}
	got, err := InterpolateAll([]string{"${app.OK}", "literal", "${app.REDIS_PASSWORD}"}, in)
	if err != nil || got[0] != "1" || got[1] != "literal" || got[2] != "p${resource}w" {
		t.Fatalf("%v %v", got, err)
	}
	if _, err := InterpolateAll([]string{"${bad}"}, in); err == nil {
		t.Fatal("must propagate unknown placeholder")
	}
}

func TestInterpolateResourceEnvNilAppAndUnclosed(t *testing.T) {
	in := Interp{Resource: "redis-abc", ResourceEnv: map[string]string{"PASS": "s3cret"}}
	got, err := Interpolate("${resource} ${resource.PASS}", in)
	if err != nil || got != "redis-abc s3cret" {
		t.Fatalf("got %q err=%v", got, err)
	}
	got, err = Interpolate("${app.MISSING}", Interp{})
	if err != nil || got != "" {
		t.Fatalf("empty app env: %q %v", got, err)
	}
	got, err = Interpolate("${resource.PASS}", Interp{})
	if err != nil || got != "" {
		t.Fatalf("nil resource env: %q %v", got, err)
	}
	if _, err := Interpolate("${unterminated", in); err == nil {
		t.Fatal("unclosed placeholder")
	}
	if _, err := Interpolate("${resource.BAD-KEY}", in); err == nil {
		t.Fatal("invalid resource ident")
	}
	if _, err := Interpolate("${app.HOST|leader.${unterminated}", in); err == nil {
		t.Fatal("unclosed fallback")
	}
}
