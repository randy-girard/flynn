package data

import (
	"strings"

	"github.com/jackc/pgx"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/postgres"
	"github.com/randy-girard/flynn/pkg/random"
)

type RuntimeProfileRepo struct {
	db *postgres.DB
}

func NewRuntimeProfileRepo(db *postgres.DB) *RuntimeProfileRepo {
	return &RuntimeProfileRepo{db: db}
}

func scanRuntimeProfile(s postgres.Scanner) (*ct.RuntimeProfile, error) {
	p := &ct.RuntimeProfile{}
	err := s.Scan(&p.ID, &p.Name, &p.Memory, &p.CPU, &p.Builtin, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			err = ErrNotFound
		}
		return nil, err
	}
	return p, nil
}

func (r *RuntimeProfileRepo) List() ([]*ct.RuntimeProfile, error) {
	rows, err := r.db.Query("runtime_profile_list")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*ct.RuntimeProfile
	for rows.Next() {
		p, err := scanRuntimeProfile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (r *RuntimeProfileRepo) Get(id string) (*ct.RuntimeProfile, error) {
	return scanRuntimeProfile(r.db.QueryRow("runtime_profile_select", id))
}

func (r *RuntimeProfileRepo) GetByName(name string) (*ct.RuntimeProfile, error) {
	return scanRuntimeProfile(r.db.QueryRow("runtime_profile_select_by_name", strings.ToLower(strings.TrimSpace(name))))
}

func (r *RuntimeProfileRepo) Add(p *ct.RuntimeProfile) error {
	if p.ID == "" {
		p.ID = random.UUID()
	}
	p.Name = strings.ToLower(strings.TrimSpace(p.Name))
	if err := r.db.QueryRow("runtime_profile_insert", p.ID, p.Name, p.Memory, p.CPU, p.Builtin).Scan(&p.CreatedAt, &p.UpdatedAt); err != nil {
		return err
	}
	return CreateEvent(r.db.Exec, &ct.Event{
		ObjectID:   p.ID,
		ObjectType: ct.EventTypeRuntimeProfile,
		Op:         ct.EventOpCreate,
	}, p)
}

func (r *RuntimeProfileRepo) Update(p *ct.RuntimeProfile) error {
	p.Name = strings.ToLower(strings.TrimSpace(p.Name))
	if err := r.db.QueryRow("runtime_profile_update", p.ID, p.Name, p.Memory, p.CPU).Scan(&p.Builtin, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return err
	}
	return CreateEvent(r.db.Exec, &ct.Event{
		ObjectID:   p.ID,
		ObjectType: ct.EventTypeRuntimeProfile,
		Op:         ct.EventOpUpdate,
	}, p)
}

func (r *RuntimeProfileRepo) Delete(id string) error {
	p, err := r.Get(id)
	if err != nil {
		return err
	}
	if p.Builtin {
		return ct.ValidationError{Field: "profile", Message: "cannot delete a builtin runtime"}
	}
	if err := r.db.Exec("runtime_profile_delete", id); err != nil {
		return err
	}
	return CreateEvent(r.db.Exec, &ct.Event{
		ObjectID:   p.ID,
		ObjectType: ct.EventTypeRuntimeProfile,
	}, p)
}

func (r *RuntimeProfileRepo) Settings() (*ct.RuntimeSettings, error) {
	s := &ct.RuntimeSettings{}
	err := r.db.QueryRow("runtime_settings_select").Scan(&s.AllowCustomLimits, &s.MaxProcesses, &s.ReserveResources, &s.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return s, nil
}

func (r *RuntimeProfileRepo) UpdateSettings(s *ct.RuntimeSettings) error {
	cur, err := r.Settings()
	if err != nil {
		return err
	}
	if s.MaxProcesses < 1 {
		s.MaxProcesses = cur.MaxProcessesOrDefault()
	}
	if err := r.db.QueryRow("runtime_settings_update", s.AllowCustomLimits, s.MaxProcesses, s.ReserveResources).Scan(&s.UpdatedAt); err != nil {
		return err
	}
	return CreateEvent(r.db.Exec, &ct.Event{
		ObjectID:   "runtime-settings",
		ObjectType: ct.EventTypeRuntimeSettings,
		Op:         ct.EventOpUpdate,
	}, s)
}
