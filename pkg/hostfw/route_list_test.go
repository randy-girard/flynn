package hostfw

import "testing"

func TestRouteListFailureKeepsExposeRules(t *testing.T) {
	current := []Rule{{Kind: KindExpose, Port: 5000, Comment: CommentExpose}}
	ports, ok := FirewallExposePorts(nil, false, ExposePorts(current), true)
	if !ok {
		t.Fatal("reconcile should keep running peers when the current exposes are known")
	}
	desired := MergeDesired("10.0.0.1", Extra{}, []string{"10.0.0.2"}, ports)
	_, remove := Diff(current, Plan(desired))
	for _, rule := range remove {
		if rule.Kind == KindExpose {
			t.Fatalf("deleted expose %v", rule)
		}
	}
	foundPeer := false
	for _, rule := range Plan(desired) {
		if rule.Kind == KindPeer && rule.From == "10.0.0.2" {
			foundPeer = true
		}
	}
	if !foundPeer {
		t.Fatal("peer update was dropped")
	}
}

func TestRouteListFailureWithoutCurrentRulesSkipsReconcile(t *testing.T) {
	_, ok := FirewallExposePorts(nil, false, nil, false)
	if ok {
		t.Fatal("empty desired exposes would delete rules; skip reconcile")
	}
}

func TestSuccessfulEmptyRouteListRemovesExposes(t *testing.T) {
	current := []Rule{{Kind: KindExpose, Port: 5000, Comment: CommentExpose}}
	ports, ok := FirewallExposePorts(nil, true, ExposePorts(current), true)
	if !ok {
		t.Fatal("a successful empty list is a real empty set")
	}
	_, remove := Diff(current, Plan(MergeDesired("10.0.0.1", Extra{}, nil, ports)))
	if len(remove) != 1 || remove[0].Port != 5000 {
		t.Fatalf("remove = %v", remove)
	}
}
