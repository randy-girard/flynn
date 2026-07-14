package kafka

import (
	"fmt"
	"sort"
	"strings"
)

// TopicSpec describes the desired configuration of a topic.
type TopicSpec struct {
	Name              string            `json:"name"`
	Partitions        int               `json:"partitions"`
	ReplicationFactor int               `json:"replication_factor"`
	Configs           map[string]string `json:"configs,omitempty"`
}

// TopicList returns the names of all user topics (internal topics excluded).
func (p *Process) TopicList() ([]string, error) {
	out, err := p.run(p.OpTimeout, "kafka-topics.sh", p.adminArgs("--list")...)
	if err != nil {
		return nil, err
	}

	var topics []string
	for _, line := range strings.Split(string(out), "\n") {
		name := strings.TrimSpace(line)
		if name == "" || strings.HasPrefix(name, "__") {
			continue
		}
		topics = append(topics, name)
	}
	sort.Strings(topics)
	return topics, nil
}

// CreateTopic provisions a new topic. A topic must exist before producers or
// consumers may use it because auto topic creation is disabled on the broker.
func (p *Process) CreateTopic(spec TopicSpec) error {
	if spec.Name == "" {
		return fmt.Errorf("topic name is required")
	}

	partitions := spec.Partitions
	if partitions <= 0 {
		partitions = p.NumPartitions
	}
	replication := spec.ReplicationFactor
	if replication <= 0 {
		replication = p.ReplicationFactor
	}

	args := p.adminArgs(
		"--create",
		"--topic", spec.Name,
		"--partitions", itoa(partitions),
		"--replication-factor", itoa(replication),
	)
	for _, kv := range sortedConfigArgs(spec.Configs) {
		args = append(args, "--config", kv)
	}

	if _, err := p.run(p.OpTimeout, "kafka-topics.sh", args...); err != nil {
		return err
	}
	return nil
}

// DeleteTopic permanently removes a topic and all of its data.
func (p *Process) DeleteTopic(name string) error {
	_, err := p.run(p.OpTimeout, "kafka-topics.sh", p.adminArgs("--delete", "--topic", name)...)
	return err
}

// DescribeTopic returns the human readable description of a topic.
func (p *Process) DescribeTopic(name string) (string, error) {
	out, err := p.run(p.OpTimeout, "kafka-topics.sh", p.adminArgs("--describe", "--topic", name)...)
	return string(out), err
}

// ConfigureTopic alters the dynamic configuration of an existing topic.
func (p *Process) ConfigureTopic(name string, configs map[string]string) error {
	if len(configs) == 0 {
		return fmt.Errorf("at least one --config key=value is required")
	}
	args := p.adminArgs(
		"--alter",
		"--entity-type", "topics",
		"--entity-name", name,
		"--add-config", strings.Join(sortedConfigArgs(configs), ","),
	)
	_, err := p.run(p.OpTimeout, "kafka-configs.sh", args...)
	return err
}

// GroupList returns the names of all consumer groups.
func (p *Process) GroupList() ([]string, error) {
	out, err := p.run(p.OpTimeout, "kafka-consumer-groups.sh", p.adminArgs("--list")...)
	if err != nil {
		return nil, err
	}

	var groups []string
	for _, line := range strings.Split(string(out), "\n") {
		name := strings.TrimSpace(line)
		if name == "" {
			continue
		}
		groups = append(groups, name)
	}
	sort.Strings(groups)
	return groups, nil
}

// DescribeGroup returns the human readable description of a consumer group,
// including per-partition offsets and lag.
func (p *Process) DescribeGroup(name string) (string, error) {
	out, err := p.run(p.OpTimeout, "kafka-consumer-groups.sh", p.adminArgs("--describe", "--group", name)...)
	return string(out), err
}

// CreateGroup registers a consumer group against a topic by seeding committed
// offsets. Kafka has no explicit "create group" operation, so we establish the
// group by committing the earliest offsets for the topic's partitions.
func (p *Process) CreateGroup(name, topic string) error {
	if name == "" || topic == "" {
		return fmt.Errorf("both group and topic are required")
	}
	_, err := p.run(p.OpTimeout, "kafka-consumer-groups.sh", p.adminArgs(
		"--group", name,
		"--topic", topic,
		"--reset-offsets",
		"--to-earliest",
		"--execute",
	)...)
	return err
}

// DeleteGroup removes a consumer group.
func (p *Process) DeleteGroup(name string) error {
	_, err := p.run(p.OpTimeout, "kafka-consumer-groups.sh", p.adminArgs("--delete", "--group", name)...)
	return err
}

// sortedConfigArgs returns key=value pairs sorted by key for deterministic output.
func sortedConfigArgs(configs map[string]string) []string {
	keys := make([]string, 0, len(configs))
	for k := range configs {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, fmt.Sprintf("%s=%s", k, configs[k]))
	}
	return out
}
