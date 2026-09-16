package schema

import "testing"

func TestValidateUnknownResource(t *testing.T) {
	schemaCache = nil
	if err := Load(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if err := Load("/does-not-matter"); err != nil {
		t.Fatal("second Load is a no-op")
	}
	if err := Validate(struct{ X int }{1}); err == nil {
		t.Fatal("unknown type")
	}
}
