package data

import (
	"time"

	"github.com/jackc/pgx"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/postgres"
	"github.com/randy-girard/flynn/pkg/random"
)

type SinkRepo struct {
	db *postgres.DB
}

func NewSinkRepo(db *postgres.DB) *SinkRepo {
	return &SinkRepo{
		db: db,
	}
}

func (r *SinkRepo) Add(s *ct.Sink) error {
	if s.ID == "" {
		s.ID = random.UUID()
	}
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	var config []byte
	if s.Config != nil {
		config = []byte(*s.Config)
	}
	var appID *string
	if s.AppID != "" {
		appID = &s.AppID
	}
	err = tx.QueryRow("sink_insert", s.ID, s.Kind, config, appID).Scan(&s.CreatedAt, &s.UpdatedAt)
	if err != nil {
		tx.Rollback()
		return err
	}
	// create sink event
	if err := CreateEvent(tx.Exec, &ct.Event{
		AppID:      s.AppID,
		ObjectID:   s.ID,
		ObjectType: ct.EventTypeSink,
	}, s); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

func scanSinks(rows *pgx.Rows) ([]*ct.Sink, error) {
	var sinks []*ct.Sink
	for rows.Next() {
		sink, err := scanSink(rows)
		if err != nil {
			return nil, err
		}
		sinks = append(sinks, sink)
	}
	return sinks, rows.Err()
}

func scanSink(s postgres.Scanner) (*ct.Sink, error) {
	sink := &ct.Sink{}
	var appID *string
	err := s.Scan(&sink.ID, &sink.Kind, &sink.Config, &appID, &sink.CreatedAt, &sink.UpdatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			err = ErrNotFound
		}
		return nil, err
	}
	if appID != nil {
		sink.AppID = *appID
	}
	return sink, err
}

func (r *SinkRepo) Get(id string) (*ct.Sink, error) {
	row := r.db.QueryRow("sink_select", id)
	return scanSink(row)
}

func (r *SinkRepo) List() ([]*ct.Sink, error) {
	rows, err := r.db.Query("sink_list")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSinks(rows)
}

func (r *SinkRepo) ListSince(since time.Time) ([]*ct.Sink, error) {
	rows, err := r.db.Query("sink_list_since", since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSinks(rows)
}

func (r *SinkRepo) Remove(id string) error {
	sink, err := r.Get(id)
	if err != nil {
		return err
	}
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	err = tx.Exec("sink_delete", sink.ID)
	if err != nil {
		tx.Rollback()
		return err
	}
	// create sink remove event
	if err := CreateEvent(tx.Exec, &ct.Event{
		AppID:      sink.AppID,
		ObjectID:   sink.ID,
		ObjectType: ct.EventTypeSinkDeletion,
	}, sink); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}
