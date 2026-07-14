package main

import (
	"reflect"
	"testing"

	"github.com/flynn/go-docopt"
)

func TestKafkaAdmin(t *testing.T) {
	got := kafkaAdmin("kafka-topics.sh", "--list")
	want := []string{"/bin/flynn-kafka", "admin", "kafka-topics.sh", "--list"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestParseRetention(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"1000ms", 1000},
		{"5s", 5000},
		{"2h", 2 * 60 * 60 * 1000},
		{"7d", 7 * 24 * 60 * 60 * 1000},
		{"0.5d", 12 * 60 * 60 * 1000},
	}
	for _, c := range cases {
		got, err := parseRetention(c.in)
		if err != nil {
			t.Fatalf("%s: unexpected error: %s", c.in, err)
		}
		if got != c.want {
			t.Fatalf("%s: got %d, want %d", c.in, got, c.want)
		}
	}

	if _, err := parseRetention("nonsense"); err == nil {
		t.Fatal("expected error for invalid retention")
	}
}

func TestTopicConfigs(t *testing.T) {
	args := &docopt.Args{
		All:    map[string]interface{}{"--config": []string{"cleanup.policy=compact", "max.message.bytes=2000000"}},
		String: map[string]string{"--retention": "7d"},
	}
	got, err := topicConfigs(args)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"cleanup.policy=compact",
		"max.message.bytes=2000000",
		"retention.ms=604800000",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestTopicConfigs_InvalidConfig(t *testing.T) {
	args := &docopt.Args{
		All:    map[string]interface{}{"--config": []string{"missing-equals"}},
		String: map[string]string{},
	}
	if _, err := topicConfigs(args); err == nil {
		t.Fatal("expected error for config without '='")
	}
}
