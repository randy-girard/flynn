package discoverd

import (
	"encoding/json"
	"testing"
)

func TestInstanceValidAndClone(t *testing.T) {
	inst := &Instance{Proto: "tcp", Addr: "10.0.0.1:8080"}
	inst.ID = inst.id()
	if err := inst.Valid(); err != nil {
		t.Fatal(err)
	}
	if inst.Host() != "10.0.0.1" || inst.Port() != "8080" {
		t.Fatalf("%s %s", inst.Host(), inst.Port())
	}

	if err := (&Instance{Addr: "10.0.0.1:8080"}).Valid(); err != ErrUnsetProto {
		t.Fatalf("unset proto: %v", err)
	}
	if err := (&Instance{Proto: "TCP", Addr: "10.0.0.1:8080"}).Valid(); err != ErrInvalidProto {
		t.Fatalf("uppercase proto: %v", err)
	}
	if err := (&Instance{Proto: "tcp", Addr: "no-port"}).Valid(); err == nil {
		t.Fatal("addr without port")
	}
	badID := &Instance{Proto: "tcp", Addr: "10.0.0.1:8080", ID: "wrong"}
	if err := badID.Valid(); err == nil {
		t.Fatal("incorrect id")
	}

	inst.Meta = map[string]string{"a": "1"}
	clone := inst.Clone()
	clone.Meta["a"] = "2"
	if inst.Meta["a"] != "1" {
		t.Fatal("clone must copy meta")
	}
	other := &Instance{Proto: "tcp", Addr: "10.0.0.1:8080", Meta: map[string]string{"a": "1"}}
	if !inst.Equal(other) {
		t.Fatal("equal")
	}
	other.Meta["a"] = "x"
	if inst.Equal(other) {
		t.Fatal("meta mismatch")
	}
}

func TestEventKindJSON(t *testing.T) {
	if EventKindUp.String() != "up" || EventKind(99).String() != "unknown" {
		t.Fatal("string")
	}
	if !EventKindUp.Any(EventKindDown, EventKindUp) || EventKindUp.Any(EventKindDown) {
		t.Fatal("any")
	}
	b, err := EventKindLeader.MarshalJSON()
	if err != nil || string(b) != `"leader"` {
		t.Fatalf("%s %v", b, err)
	}
	var k EventKind
	if err := json.Unmarshal([]byte(`"service_meta"`), &k); err != nil || k != EventKindServiceMeta {
		t.Fatalf("%v %v", k, err)
	}
	e := &Event{Service: "postgres", Kind: EventKindUp, Instance: &Instance{Addr: ":5432"}}
	if e.String() == "" {
		t.Fatal("event string")
	}
}
