package types

import "time"

// TenantBackup is the latest attempt for one Postgres primary. LastSuccessAt
// stays put when a later attempt fails. Dump bytes are not stored here.
type TenantBackup struct {
	ResourceID           string     `json:"resource_id"`
	AttemptState         string     `json:"attempt_state,omitempty"`
	AttemptStartedAt     *time.Time `json:"attempt_started_at,omitempty"`
	AttemptFinishedAt    *time.Time `json:"attempt_finished_at,omitempty"`
	AttemptError         string     `json:"attempt_error,omitempty"`
	AttemptTimestamp     string     `json:"attempt_timestamp,omitempty"`
	HeartbeatAt          *time.Time `json:"heartbeat_at,omitempty"`
	LastSuccessAt        *time.Time `json:"last_success_at,omitempty"`
	LastSuccessTimestamp string     `json:"last_success_timestamp,omitempty"`
	UpdatedAt            *time.Time `json:"updated_at,omitempty"`
}

// TenantBackupDump is one timestamp this job stored, with the databases in it.
type TenantBackupDump struct {
	Timestamp string   `json:"timestamp"`
	Databases []string `json:"databases"`
}

// TenantBackupBegin is the result of taking the running lock.
type TenantBackupBegin struct {
	Started bool         `json:"started"`
	Status  TenantBackup `json:"status"`
}
