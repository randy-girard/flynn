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

func TestUpdateAddsResourceImageIDs(t *testing.T) {
	env := map[string]string{}
	ids := IDs{Kafka: "kafka-id", ClickHouse: "clickhouse-id"}
	if !Update(env, ids) {
		t.Fatal("expected env update")
	}
	if env["KAFKA_IMAGE_ID"] != "kafka-id" {
		t.Fatalf("kafka: got %q", env["KAFKA_IMAGE_ID"])
	}
	if env["CLICKHOUSE_IMAGE_ID"] != "clickhouse-id" {
		t.Fatalf("clickhouse: got %q", env["CLICKHOUSE_IMAGE_ID"])
	}
}

func TestUpdateNoChangeWhenCurrent(t *testing.T) {
	env := map[string]string{"DOCKERBUILDER_24_IMAGE_ID": "db"}
	if Update(env, IDs{DockerBuilder: "db"}) {
		t.Fatal("expected no update")
	}
}
