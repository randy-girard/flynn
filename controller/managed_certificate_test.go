package main

import (
	"net/http/httptest"
	"testing"
)

func TestListManagedCertificatesQueryValidation(t *testing.T) {
	c := &controllerAPI{}

	_, err := c.listManagedCertificates(httptest.NewRequest("GET", "/managed-certificates?expiring_before=nope", nil))
	ve, ok := err.(listManagedCertificatesError)
	if !ok || ve.field != "expiring_before" {
		t.Fatalf("expiring_before: %v", err)
	}

	_, err = c.listManagedCertificates(httptest.NewRequest("GET", "/managed-certificates?status=bogus", nil))
	ve, ok = err.(listManagedCertificatesError)
	if !ok || ve.field != "status" {
		t.Fatalf("status: %v", err)
	}

	_, err = c.listManagedCertificates(httptest.NewRequest("GET", "/managed-certificates?since=nope", nil))
	ve, ok = err.(listManagedCertificatesError)
	if !ok || ve.field != "since" {
		t.Fatalf("since: %v", err)
	}
}
