package main

import (
	"encoding/json"
	"testing"
)

func TestProjectedAccountProcessesKeepsOmittedTypes(t *testing.T) {
	// usage already includes web 10. A partial worker=10 must count both.
	if got := projectedAccountProcesses(10, map[string]int{"web": 10}, map[string]int{"worker": 10}); got != 20 {
		t.Fatalf("partial overlay = %d, want 20", got)
	}
	if got := projectedAccountProcesses(6, map[string]int{"web": 1, "worker": 5}, map[string]int{"web": 11}); got != 16 {
		t.Fatalf("web replace = %d, want 16", got)
	}
	if got := projectedAccountProcesses(4, map[string]int{"web": 4}, map[string]int{"web": 0}); got != 0 {
		t.Fatalf("scale to zero = %d, want 0", got)
	}
	if got := projectedAccountProcesses(0, nil, map[string]int{"web": 11}); got != 11 {
		t.Fatalf("new formation = %d, want 11", got)
	}
}

func TestMaxProcessesFieldOmitsNullAndRejectsZero(t *testing.T) {
	var omitted struct {
		MaxProcesses maxProcessesField `json:"max_processes"`
	}
	if err := json.Unmarshal([]byte(`{"allow_custom_limits":false}`), &omitted); err != nil {
		t.Fatal(err)
	}
	if omitted.MaxProcesses.set {
		t.Fatal("omitted max_processes must keep the stored value")
	}

	var cleared struct {
		MaxProcesses maxProcessesField `json:"max_processes"`
	}
	if err := json.Unmarshal([]byte(`{"max_processes":null}`), &cleared); err != nil {
		t.Fatal(err)
	}
	if !cleared.MaxProcesses.set || !cleared.MaxProcesses.unlimited {
		t.Fatal("JSON null must be unlimited")
	}

	var number struct {
		MaxProcesses maxProcessesField `json:"max_processes"`
	}
	if err := json.Unmarshal([]byte(`{"max_processes":50}`), &number); err != nil {
		t.Fatal(err)
	}
	if !number.MaxProcesses.set || number.MaxProcesses.unlimited || number.MaxProcesses.n != 50 {
		t.Fatalf("number field = %+v", number.MaxProcesses)
	}

	var zero struct {
		MaxProcesses maxProcessesField `json:"max_processes"`
	}
	if err := json.Unmarshal([]byte(`{"max_processes":0}`), &zero); err == nil {
		t.Fatal("0 must be rejected")
	}
	if err := json.Unmarshal([]byte(`{"max_processes":-3}`), &zero); err == nil {
		t.Fatal("negative must be rejected")
	}
}
