// Package redact removes secret values from JSON before the controller
// writes it to core_audit or to the AUDIT_LOG request log.
//
// Secrets are found by field name. A field whose name looks like a password,
// key, token, secret, or credential has its value replaced with Placeholder,
// at any depth. Booleans are kept (password_changed is not a secret). Inside
// an env map ("env" or "*_env"), names that hold connection strings (url,
// dsn, pass, pwd, auth) are replaced as well.
package redact

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"
)

// Placeholder replaces every redacted value.
const Placeholder = "[redacted]"

var secretParts = []string{"password", "passwd", "secret", "token", "private", "credential", "hmac", "cookie", "key"}

var envParts = []string{"pass", "pwd", "url", "dsn", "auth"}

// Key reports whether a field name names a secret.
func Key(name string) bool {
	n := strings.ToLower(name)
	for _, s := range secretParts {
		if strings.Contains(n, s) {
			return true
		}
	}
	return false
}

// EnvKey reports whether an environment variable name holds a secret. It is
// wider than Key: DATABASE_URL and REDIS_PASS carry credentials.
func EnvKey(name string) bool {
	if Key(name) {
		return true
	}
	n := strings.ToLower(name)
	for _, s := range envParts {
		if strings.Contains(n, s) {
			return true
		}
	}
	return false
}

// Env replaces the secret values of an env map in place.
func Env(env map[string]string) {
	for k, v := range env {
		if v != "" && EnvKey(k) {
			env[k] = Placeholder
		}
	}
}

func isEnvField(name string) bool {
	n := strings.ToLower(name)
	return n == "env" || strings.HasSuffix(n, "_env")
}

func walk(v interface{}, inEnv bool) interface{} {
	switch t := v.(type) {
	case map[string]interface{}:
		for k, val := range t {
			if _, isBool := val.(bool); isBool || val == nil || val == "" {
				// A flag such as password_changed or has_account_key
				// says nothing about the secret itself.
				continue
			}
			if Key(k) || (inEnv && EnvKey(k)) {
				t[k] = Placeholder
				continue
			}
			t[k] = walk(val, isEnvField(k))
		}
	case []interface{}:
		for i, val := range t {
			t[i] = walk(val, inEnv)
		}
	}
	return v
}

// JSON returns raw with every secret value replaced. It fails if raw is not
// JSON; callers must then drop the body rather than store it.
func JSON(raw []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v interface{}
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return json.Marshal(walk(v, false))
}

// Value marshals v to JSON with every secret value replaced.
func Value(v interface{}) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return JSON(raw)
}

// Fields returns the sorted top-level field names of a JSON object, or nil
// when raw is not an object. It lets a log say what a request changed
// without any values.
func Fields(raw []byte) []string {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
