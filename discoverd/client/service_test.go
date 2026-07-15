package discoverd

import (
	"errors"
	"testing"

	"github.com/flynn/flynn/pkg/httphelper"
	"github.com/flynn/flynn/pkg/stream"
)

type stubService struct {
	instances []*Instance
	err       error
}

func (s *stubService) Leader() (*Instance, error) { return nil, nil }
func (s *stubService) Instances() ([]*Instance, error) {
	return s.instances, s.err
}
func (s *stubService) Addrs() ([]string, error) { return nil, nil }
func (s *stubService) Leaders(chan *Instance) (stream.Stream, error) {
	return nil, nil
}
func (s *stubService) Watch(chan *Event) (stream.Stream, error) { return nil, nil }
func (s *stubService) GetMeta() (*ServiceMeta, error)           { return nil, nil }
func (s *stubService) SetMeta(*ServiceMeta) error               { return nil }
func (s *stubService) SetLeader(string) error                   { return nil }

func TestInstancesOrEmpty(t *testing.T) {
	want := []*Instance{{Addr: ":3306"}}
	instances, err := InstancesOrEmpty(&stubService{instances: want})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(instances) != 1 || instances[0].Addr != want[0].Addr {
		t.Fatalf("got %#v want %#v", instances, want)
	}

	instances, err = InstancesOrEmpty(&stubService{err: httphelper.JSONError{
		Code:    httphelper.ObjectNotFoundErrorCode,
		Message: `service not found: "mariadb"`,
	}})
	if err != nil {
		t.Fatalf("unexpected error for not found: %v", err)
	}
	if len(instances) != 0 {
		t.Fatalf("expected empty slice, got %#v", instances)
	}

	other := errors.New("connection refused")
	if _, err := InstancesOrEmpty(&stubService{err: other}); err != other {
		t.Fatalf("expected %v, got %v", other, err)
	}
}
