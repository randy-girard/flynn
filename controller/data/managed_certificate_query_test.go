package data

import (
	"strings"
	"testing"
)

func TestManagedCertificateExpiringQueryIsIssuedOnly(t *testing.T) {
	if !strings.Contains(managedCertificateListExpiringQuery, "status = 'issued'") {
		t.Fatal("ListExpiring must only return issued certificates")
	}
	if !strings.Contains(managedCertificateListExpiringQuery, "expires_at <= $1") {
		t.Fatal("ListExpiring must filter by expiry")
	}
	if !strings.Contains(managedCertificateListByStatusQuery, "status = $1") {
		t.Fatal("ListByStatus must filter by status")
	}
}
