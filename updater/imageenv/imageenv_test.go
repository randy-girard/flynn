package imageenv

import "testing"

func TestUpdateAddsMissingDockerBuilderID(t *testing.T) {
	env := map[string]string{
		"SLUGBUILDER_24_IMAGE_ID": "sb",
		"SLUGRUNNER_24_IMAGE_ID":  "sr",
	}
	ids := IDs{SlugBuilder: "sb", SlugRunner: "sr", DockerBuilder: "db"}
	if !Update(env, ids) {
		t.Fatal("expected env update")
	}
	if env["DOCKERBUILDER_24_IMAGE_ID"] != "db" {
		t.Fatalf("got %q", env["DOCKERBUILDER_24_IMAGE_ID"])
	}
}

func TestUpdateNoChangeWhenCurrent(t *testing.T) {
	env := map[string]string{"DOCKERBUILDER_24_IMAGE_ID": "db"}
	if Update(env, IDs{DockerBuilder: "db"}) {
		t.Fatal("expected no update")
	}
}
