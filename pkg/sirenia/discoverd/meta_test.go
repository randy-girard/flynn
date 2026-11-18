package discoverd

import (
	"testing"

	"github.com/inconshreveable/log15"
	"github.com/randy-girard/flynn/pkg/sirenia/state"

	discoverd "github.com/randy-girard/flynn/discoverd/client"
	hh "github.com/randy-girard/flynn/pkg/httphelper"
)

func TestMetaAbsent(t *testing.T) {
	gone := hh.JSONError{Code: hh.ObjectNotFoundErrorCode, Message: "service meta not found"}
	if !metaAbsent(gone, nil) {
		t.Fatal("missing meta should be republished")
	}
	if metaAbsent(hh.JSONError{Code: hh.ServiceUnavailableErrorCode, Message: "down"}, nil) {
		t.Fatal("a discoverd outage is not missing meta")
	}
	if metaAbsent(nil, &discoverd.ServiceMeta{Data: []byte(`{"generation":1}`)}) {
		t.Fatal("existing meta must be left in place")
	}
	if !metaAbsent(nil, &discoverd.ServiceMeta{}) {
		t.Fatal("empty meta should be republished")
	}
}

type metaService struct {
	discoverd.Service
	cur  *discoverd.ServiceMeta
	sets []uint64
}

func (m *metaService) GetMeta() (*discoverd.ServiceMeta, error) { return m.cur, nil }

func (m *metaService) SetMeta(meta *discoverd.ServiceMeta) error {
	m.sets = append(m.sets, meta.Index)
	if m.cur != nil && meta.Index != m.cur.Index {
		return hh.JSONError{Code: hh.ObjectExistsErrorCode, Message: `Service metadata for "postgres" already exists, use index=n to set`}
	}
	meta.Index = m.cur.Index + 1
	return nil
}

// metaAbsent treats an existing empty meta as missing, so the primary
// republishes at index 0. The write must still land on that empty meta.
func TestSetStateRepublishOverEmptyMeta(t *testing.T) {
	svc := &metaService{cur: &discoverd.ServiceMeta{Index: 7}}
	d := NewDiscoverd(svc, log15.New()).(*Discoverd)
	st := &state.DiscoverdState{State: &state.State{Generation: 2}}
	if err := d.SetState(st); err != nil {
		t.Fatalf("republish over an empty meta failed: %v", err)
	}
	if st.Index != 8 {
		t.Fatalf("index = %d, want 8", st.Index)
	}
}
