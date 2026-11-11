package cli

import (
	"strings"
	"testing"
)

func TestTenantDumpNotice(t *testing.T) {
	if !strings.Contains(tenantDumpNotice, "backups/") || !strings.Contains(tenantDumpNotice, "cluster restore") || !strings.Contains(tenantDumpNotice, "Tenant volumes") {
		t.Fatal(tenantDumpNotice)
	}
	if strings.Contains(strings.ToLower(tenantDumpNotice), "large object") {
		t.Fatal("the appliance dump already includes large objects")
	}
}
