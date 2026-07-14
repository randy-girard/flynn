package kafka

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/flynn/flynn/pkg/random"
)

const (
	// NodeIDFileName is persisted on the broker data volume so KRaft node.id
	// survives overlay IP changes across job restarts.
	NodeIDFileName = "node.id"

	// BootstrapIDFileName identifies a broker on first cluster formation before
	// node.id is assigned. It is written once and never changed.
	BootstrapIDFileName = "bootstrap.id"

	// DirectoryIDFileName is persisted on the broker data volume so the KRaft
	// directory.id (the voter's replica identity) survives overlay IP changes.
	// A stable directory.id lets a rescheduled broker rejoin the dynamic quorum
	// under the same voter identity instead of being rejected as a new replica.
	DirectoryIDFileName = "directory.id"
)

// LoadOrCreateBootstrapID returns the stable bootstrap identifier for this
// broker's data volume, creating one on first boot.
func LoadOrCreateBootstrapID(dataDir string) (string, error) {
	path := filepath.Join(dataDir, BootstrapIDFileName)
	if data, err := os.ReadFile(path); err == nil {
		if id := strings.TrimSpace(string(data)); id != "" {
			return id, nil
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}

	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return "", err
	}
	id := random.UUID()
	if err := os.WriteFile(path, []byte(id), 0644); err != nil {
		return "", err
	}
	return id, nil
}

// LoadOrCreateDirectoryID returns the stable KRaft directory.id for this data
// volume, creating one via gen on first boot. The value must be a Kafka Uuid
// (22-char URL-safe base64), so gen must produce one (see GenerateUUID); the
// generic random.UUID format is not accepted by KRaft.
func LoadOrCreateDirectoryID(dataDir string, gen func() (string, error)) (string, error) {
	path := filepath.Join(dataDir, DirectoryIDFileName)
	if data, err := os.ReadFile(path); err == nil {
		if id := strings.TrimSpace(string(data)); id != "" {
			return id, nil
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}

	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return "", err
	}
	id, err := gen()
	if err != nil {
		return "", err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return "", fmt.Errorf("generated empty directory id")
	}
	if err := os.WriteFile(path, []byte(id), 0644); err != nil {
		return "", err
	}
	return id, nil
}

// GenerateUUID runs kafka-storage.sh random-uuid and returns a Kafka Uuid
// string suitable for use as a KRaft directory.id.
func GenerateUUID(binDir string) (string, error) {
	out, err := exec.Command(filepath.Join(binDir, "kafka-storage.sh"), "random-uuid").Output()
	if err != nil {
		return "", fmt.Errorf("generate uuid: %s", err)
	}
	id := strings.TrimSpace(string(out))
	if id == "" {
		return "", fmt.Errorf("kafka-storage.sh random-uuid returned empty output")
	}
	return id, nil
}

// ReadStoredNodeID returns the node id persisted on the data volume.
func ReadStoredNodeID(dataDir string) (int, bool) {
	data, err := os.ReadFile(filepath.Join(dataDir, NodeIDFileName))
	if err != nil {
		return 0, false
	}
	id, err := parseNodeID(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, false
	}
	return id, true
}

// WriteStoredNodeID persists the broker's KRaft node id on the data volume.
func WriteStoredNodeID(dataDir string, id int) error {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dataDir, NodeIDFileName), []byte(fmt.Sprintf("%d\n", id)), 0644)
}

// ReadNodeIDFromMeta reads node.id from an already-formatted KRaft log directory.
func ReadNodeIDFromMeta(dataDir string) (int, bool) {
	path := filepath.Join(dataDir, "kraft-logs", "meta.properties")
	f, err := os.Open(path)
	if err != nil {
		return 0, false
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "node.id=") {
			continue
		}
		id, err := parseNodeID(strings.TrimPrefix(line, "node.id="))
		if err != nil {
			return 0, false
		}
		return id, true
	}
	return 0, false
}

// AssignNodeIDFromBootstrapIDs returns a deterministic node id in the range
// 1..n based on the lexicographic order of bootstrap ids.
func AssignNodeIDFromBootstrapIDs(myBootstrapID string, bootstrapIDs []string) int {
	ids := uniqueSortedStrings(bootstrapIDs)
	for i, id := range ids {
		if id == myBootstrapID {
			return i + 1
		}
	}
	return len(ids) + 1
}

// ResolveNodeID chooses the KRaft node id for this broker. Existing formatted
// storage and previously persisted ids take precedence over bootstrap assignment.
func ResolveNodeID(dataDir, bootstrapID string, bootstrapIDs []string) (int, error) {
	if id, ok := ReadStoredNodeID(dataDir); ok {
		return id, nil
	}
	if id, ok := ReadNodeIDFromMeta(dataDir); ok {
		if err := WriteStoredNodeID(dataDir, id); err != nil {
			return 0, err
		}
		return id, nil
	}
	id := AssignNodeIDFromBootstrapIDs(bootstrapID, bootstrapIDs)
	if err := WriteStoredNodeID(dataDir, id); err != nil {
		return 0, err
	}
	return id, nil
}

// ValidateNodeID ensures the configured node id matches formatted storage.
func ValidateNodeID(dataDir string, nodeID int) error {
	stored, ok := ReadNodeIDFromMeta(dataDir)
	if !ok {
		return nil
	}
	if stored != nodeID {
		return fmt.Errorf("configured node.id %d does not match formatted storage node.id %d", nodeID, stored)
	}
	return nil
}

func parseNodeID(s string) (int, error) {
	var id int
	if _, err := fmt.Sscanf(s, "%d", &id); err != nil {
		return 0, err
	}
	if id <= 0 {
		return 0, fmt.Errorf("invalid node id %q", s)
	}
	return id, nil
}

func uniqueSortedStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	for _, v := range values {
		if v == "" {
			continue
		}
		seen[v] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for v := range seen {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}
