package data

import (
	"strings"

	"github.com/jackc/pgx"
	"github.com/randy-girard/flynn/pkg/joboutput"
	"github.com/randy-girard/flynn/pkg/postgres"
)

func (r *JobRepo) SetOutput(jobID, text string) error {
	return setOutput(r.db, "job_output_upsert", jobID, text)
}

func (r *JobRepo) Output(jobID string) (string, error) {
	return getOutput(r.db, "job_output_select", jobID)
}

func (r *ReleaseRepo) SetOutput(releaseID, text string) error {
	return setOutput(r.db, "release_output_upsert", releaseID, text)
}

func (r *ReleaseRepo) Output(releaseID string) (string, error) {
	return getOutput(r.db, "release_output_select", releaseID)
}

func setOutput(db *postgres.DB, query, id, text string) error {
	if db == nil || id == "" {
		return nil
	}
	text = joboutput.Tail(text, joboutput.MaxBytes)
	if strings.TrimSpace(text) == "" {
		return nil
	}
	body, err := joboutput.Compress([]byte(text))
	if err != nil {
		return err
	}
	return db.Exec(query, id, body, len(text))
}

func getOutput(db *postgres.DB, query, id string) (string, error) {
	if db == nil || id == "" {
		return "", ErrNotFound
	}
	var body []byte
	if err := db.QueryRow(query, id).Scan(&body); err != nil {
		if err == pgx.ErrNoRows {
			return "", ErrNotFound
		}
		return "", err
	}
	plain, err := joboutput.Decompress(body)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}
