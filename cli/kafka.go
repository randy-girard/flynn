package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	controller "github.com/flynn/flynn/controller/client"
	"github.com/flynn/go-docopt"
)

// kafkaAdmin builds the argument list to run a kafka-*.sh tool through the
// flynn-kafka admin wrapper, which injects the bootstrap server and (when the
// cluster has TLS enabled) the SSL --command-config from the release env.
func kafkaAdmin(tool string, args ...string) []string {
	return append([]string{"/bin/flynn-kafka", "admin", tool}, args...)
}

func init() {
	register("kafka", runKafka, `
usage: flynn kafka topics
       flynn kafka topics create <topic> [--partitions=<n>] [--replication=<n>] [--retention=<duration>] [--config=<kv>]...
       flynn kafka topics info <topic>
       flynn kafka topics configure <topic> [--retention=<duration>] [--config=<kv>]...
       flynn kafka topics destroy <topic>
       flynn kafka consumer-groups
       flynn kafka consumer-groups create <group> <topic>
       flynn kafka consumer-groups info <group>
       flynn kafka consumer-groups destroy <group>

Manage a Flynn Kafka cluster: topics and consumer groups.

Topics must be created with this tool before they can be used; the brokers are
configured with auto topic creation disabled.

Options:
	--partitions=<n>         number of partitions for the topic
	--replication=<n>        replication factor for the topic
	--retention=<duration>   how long to retain messages, e.g. 168h, 7d (sets retention.ms)
	--config=<kv>            additional topic config as key=value (repeatable)

Commands:
	topics                       List all topics.
	topics create                Create a topic with the given settings.
	topics info                  Describe a topic (partitions, replicas, config).
	topics configure             Alter the config of an existing topic.
	topics destroy               Delete a topic and all of its data.
	consumer-groups              List all consumer groups.
	consumer-groups create       Register a consumer group against a topic.
	consumer-groups info         Describe a consumer group (offsets and lag).
	consumer-groups destroy      Delete a consumer group.

Examples:

    $ flynn kafka topics create events --partitions 12 --replication 3 --retention 7d

    $ flynn kafka topics create audit --config cleanup.policy=compact --config max.message.bytes=2000000

    $ flynn kafka topics info events

    $ flynn kafka consumer-groups create workers events
`)
}

func runKafka(args *docopt.Args, client controller.Client) error {
	config, err := getAppKafkaRunConfig(client)
	if err != nil {
		return err
	}

	switch {
	case args.Bool["topics"]:
		return runKafkaTopics(args, client, config)
	case args.Bool["consumer-groups"]:
		return runKafkaConsumerGroups(args, client, config)
	}
	return nil
}

func getAppKafkaRunConfig(client controller.Client) (*runConfig, error) {
	appRelease, err := client.GetAppRelease(mustApp())
	if err != nil {
		return nil, fmt.Errorf("error getting app release: %s", err)
	}

	kafkaApp := appRelease.Env["FLYNN_KAFKA"]
	if kafkaApp == "" {
		return nil, fmt.Errorf("No kafka cluster found. Provision one with `flynn resource add kafka`")
	}

	kafkaRelease, err := client.GetAppRelease(kafkaApp)
	if err != nil {
		return nil, fmt.Errorf("error getting kafka release: %s", err)
	}

	bootstrap := appRelease.Env["KAFKA_BOOTSTRAP_SERVERS"]
	if bootstrap == "" {
		bootstrap = "leader." + kafkaApp + ".discoverd:9092"
	}

	return &runConfig{
		App:     mustApp(),
		Release: kafkaRelease.ID,
		// Inherit the cluster release env so the admin wrapper can read the TLS
		// client materials; override the bootstrap server for the connection.
		ReleaseEnv: true,
		Env:        map[string]string{"KAFKA_BOOTSTRAP_SERVERS": bootstrap},
		DisableLog: true,
		Exit:       true,
	}, nil
}

func runKafkaTopics(args *docopt.Args, client controller.Client, config *runConfig) error {
	topic := args.String["<topic>"]

	switch {
	case args.Bool["create"]:
		configs, err := topicConfigs(args)
		if err != nil {
			return err
		}
		cmd := kafkaAdmin("kafka-topics.sh", "--create", "--topic", topic)
		if n := args.String["--partitions"]; n != "" {
			cmd = append(cmd, "--partitions", n)
		}
		if n := args.String["--replication"]; n != "" {
			cmd = append(cmd, "--replication-factor", n)
		}
		for _, kv := range configs {
			cmd = append(cmd, "--config", kv)
		}
		config.Args = cmd
	case args.Bool["info"]:
		config.Args = kafkaAdmin("kafka-topics.sh", "--describe", "--topic", topic)
	case args.Bool["configure"]:
		configs, err := topicConfigs(args)
		if err != nil {
			return err
		}
		if len(configs) == 0 {
			return fmt.Errorf("provide --retention and/or --config key=value to change")
		}
		config.Args = kafkaAdmin("kafka-configs.sh",
			"--alter", "--entity-type", "topics", "--entity-name", topic,
			"--add-config", strings.Join(configs, ","))
	case args.Bool["destroy"]:
		config.Args = kafkaAdmin("kafka-topics.sh", "--delete", "--topic", topic)
	default:
		config.Args = kafkaAdmin("kafka-topics.sh", "--list")
	}
	return runJob(client, *config)
}

func runKafkaConsumerGroups(args *docopt.Args, client controller.Client, config *runConfig) error {
	group := args.String["<group>"]

	switch {
	case args.Bool["create"]:
		// Kafka has no explicit group creation; register the group by seeding
		// the earliest committed offsets for the topic's partitions.
		config.Args = kafkaAdmin("kafka-consumer-groups.sh",
			"--group", group, "--topic", args.String["<topic>"],
			"--reset-offsets", "--to-earliest", "--execute")
	case args.Bool["info"]:
		config.Args = kafkaAdmin("kafka-consumer-groups.sh", "--describe", "--group", group)
	case args.Bool["destroy"]:
		config.Args = kafkaAdmin("kafka-consumer-groups.sh", "--delete", "--group", group)
	default:
		config.Args = kafkaAdmin("kafka-consumer-groups.sh", "--list")
	}
	return runJob(client, *config)
}

// topicConfigs collects the --config key=value pairs plus a --retention shortcut
// into a slice of key=value strings suitable for kafka-topics.sh/kafka-configs.sh.
func topicConfigs(args *docopt.Args) ([]string, error) {
	var configs []string
	if raw, ok := args.All["--config"].([]string); ok {
		for _, kv := range raw {
			if !strings.Contains(kv, "=") {
				return nil, fmt.Errorf("invalid --config %q, expected key=value", kv)
			}
			configs = append(configs, kv)
		}
	}
	if r := args.String["--retention"]; r != "" {
		ms, err := parseRetention(r)
		if err != nil {
			return nil, err
		}
		configs = append(configs, fmt.Sprintf("retention.ms=%d", ms))
	}
	return configs, nil
}

// parseRetention parses a retention duration, additionally supporting a "d"
// (day) suffix on top of Go's standard duration units, and returns milliseconds.
func parseRetention(s string) (int64, error) {
	if strings.HasSuffix(s, "d") {
		days, err := strconv.ParseFloat(strings.TrimSuffix(s, "d"), 64)
		if err != nil {
			return 0, fmt.Errorf("invalid retention %q: %s", s, err)
		}
		return int64(days * 24 * float64(time.Hour) / float64(time.Millisecond)), nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid retention %q: %s", s, err)
	}
	return int64(d / time.Millisecond), nil
}
