package data

import (
	. "github.com/flynn/go-check"
)

// TestAuditRedactsSecretFields checks the central redaction in
// TenancyRepo.Audit: a caller that passes a secret-named field still cannot
// write its value to core_audit.
func (s *S) TestAuditRedactsSecretFields(c *C) {
	r := NewTenancyRepo(s.db)
	var start int64
	c.Assert(s.db.QueryRow(`SELECT COALESCE(max(id), 0) FROM core_audit`).Scan(&start), IsNil)
	c.Assert(r.Audit("cluster-key", "core21.test", "", "", map[string]interface{}{
		"password": "core21-pw-value",
		"nested":   map[string]interface{}{"private_key": "core21-pem-value", "name": "kept-value"},
		"env":      map[string]string{"DATABASE_URL": "postgres://u:core21-url@h/db", "PORT": "8080"},
		"list":     []interface{}{map[string]interface{}{"api_token": "core21-tok-value"}},
	}), IsNil)
	var data string
	c.Assert(s.db.QueryRow(`SELECT data::text FROM core_audit WHERE id > $1 AND action = 'core21.test'`, start).Scan(&data), IsNil)
	for _, secret := range []string{"core21-pw-value", "core21-pem-value", "core21-url", "core21-tok-value"} {
		c.Assert(data, Not(Matches), ".*"+secret+".*")
	}
	c.Assert(data, Matches, ".*kept-value.*")
	c.Assert(data, Matches, ".*8080.*")
	c.Assert(r.Audit("cluster-key", "core21.nil", "", "", nil), IsNil)
}
