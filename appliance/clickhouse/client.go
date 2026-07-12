package clickhouse

// Status represents the response to the /status endpoint.
type Status struct {
	Process *ProcessInfo `json:"process"`
}
