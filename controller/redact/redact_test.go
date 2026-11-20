package redact

import (
	"reflect"
	"strings"
	"testing"
)

func TestKey(t *testing.T) {
	for _, k := range []string{"password", "Password", "private_key", "tls_key", "key", "webhook_secret", "client_secret", "token", "api_token", "account_key", "credentials", "eab_hmac_key", "session_cookie"} {
		if !Key(k) {
			t.Errorf("Key(%q) = false", k)
		}
	}
	for _, k := range []string{"email", "name", "handle", "mode", "domain", "cert", "type", "disabled", "id"} {
		if Key(k) {
			t.Errorf("Key(%q) = true", k)
		}
	}
	for _, k := range []string{"DATABASE_URL", "REDIS_PASS", "MYSQL_PWD", "SENTRY_DSN", "BASIC_AUTH"} {
		if !EnvKey(k) {
			t.Errorf("EnvKey(%q) = false", k)
		}
	}
	if EnvKey("PORT") {
		t.Error("EnvKey(PORT) = true")
	}
}

func TestJSON(t *testing.T) {
	in := `{"password":"p1","password_changed":true,"has_account_key":false,"email":"e","n":12345678901234567890,"empty_token":"",
		"certificate":{"key":"p2","cert":"pub"},
		"env":{"DATABASE_URL":"p3","PORT":"80"},
		"processes":{"web":{"env":{"REDIS_PASSWORD":"p4","URL_NOT_IN_ENV":"x"}}},
		"meta":{"git_url":"kept"},
		"list":[{"api_token":"p5"}]}`
	out, err := JSON([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, secret := range []string{"p1", "p2", "p3", "p4", "p5"} {
		if strings.Contains(s, `"`+secret+`"`) {
			t.Errorf("%s kept in %s", secret, s)
		}
	}
	for _, kept := range []string{`"email":"e"`, `"cert":"pub"`, `"PORT":"80"`, `"git_url":"kept"`, `12345678901234567890`, `"empty_token":""`, `"password_changed":true`, `"has_account_key":false`} {
		if !strings.Contains(s, kept) {
			t.Errorf("%s missing from %s", kept, s)
		}
	}
	if _, err := JSON([]byte("not json")); err == nil {
		t.Error("JSON accepted a non-JSON body")
	}
	if out, err := Value(nil); err != nil || string(out) != "null" {
		t.Errorf("Value(nil) = %s, %v", out, err)
	}
}

func TestFieldsAndEnv(t *testing.T) {
	if got := Fields([]byte(`{"password":"x","email":"y"}`)); !reflect.DeepEqual(got, []string{"email", "password"}) {
		t.Errorf("Fields = %v", got)
	}
	if got := Fields([]byte(`[1]`)); got != nil {
		t.Errorf("Fields(array) = %v", got)
	}
	env := map[string]string{"SECRET_KEY": "s", "PORT": "80", "EMPTY_TOKEN": ""}
	Env(env)
	if env["SECRET_KEY"] != Placeholder || env["PORT"] != "80" || env["EMPTY_TOKEN"] != "" {
		t.Errorf("Env = %v", env)
	}
}
