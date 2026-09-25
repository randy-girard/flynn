// Package networkpolicy builds dry-run nftables rules for hosted tenants.
// self_hosted returns no rules. It does not install rules into a job namespace.
package networkpolicy

import (
	"fmt"
	"sort"
	"strings"
)

// Endpoint is a TCP destination. Owner is the account that may use it.
// System endpoints leave Owner empty and are always denied to tenants.
type Endpoint struct {
	Owner string
	CIDR  string
	Port  int
	Name  string
}

// SystemDeny is the hosted default: controller internal, postgres admin,
// discoverd write, host API :1113, and blobstore internal.
func SystemDeny() []Endpoint {
	return []Endpoint{
		{CIDR: "10.0.0.0/8", Port: 443, Name: "controller-internal"},
		{CIDR: "10.0.0.0/8", Port: 5432, Name: "postgres-admin"},
		{CIDR: "10.0.0.0/8", Port: 1111, Name: "discoverd-write"},
		{CIDR: "10.0.0.0/8", Port: 1113, Name: "host-api"},
		{CIDR: "10.0.0.0/8", Port: 80, Name: "blobstore-internal"},
	}
}

// Rules returns nftables text. mode self_hosted (or empty) yields an empty
// document. An allowed endpoint owned by another account is rejected so
// tenant B cannot include tenant A's database.
func Rules(mode, owner string, deny, allowed []Endpoint) (string, error) {
	if mode == "" || mode == "self_hosted" {
		return "", nil
	}
	if mode != "hosted" {
		return "", fmt.Errorf("tenancy mode must be self_hosted or hosted")
	}
	if strings.TrimSpace(owner) == "" {
		return "", fmt.Errorf("owner account is required")
	}
	for _, ep := range allowed {
		if ep.Owner != "" && ep.Owner != owner {
			return "", fmt.Errorf("endpoint %s belongs to %s, not %s", ep.Name, ep.Owner, owner)
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# flynn tenancy network-policy owner=%s mode=hosted\n", owner)
	b.WriteString("table inet flynn_tenant {\n")
	b.WriteString("  chain forward {\n")
	denies := append([]Endpoint(nil), deny...)
	sort.Slice(denies, func(i, j int) bool { return denies[i].Name < denies[j].Name })
	for _, ep := range denies {
		fmt.Fprintf(&b, "    # deny %s\n    ip daddr %s tcp dport %d drop\n", ep.Name, ep.CIDR, ep.Port)
	}
	allows := append([]Endpoint(nil), allowed...)
	sort.Slice(allows, func(i, j int) bool { return allows[i].Name < allows[j].Name })
	for _, ep := range allows {
		fmt.Fprintf(&b, "    # allow %s\n    ip daddr %s tcp dport %d accept\n", ep.Name, ep.CIDR, ep.Port)
	}
	b.WriteString("  }\n}\n")
	return b.String(), nil
}
