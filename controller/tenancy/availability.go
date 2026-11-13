package tenancy

import "fmt"

const (
	// AvailabilitySystem keeps one platform Postgres replica per primary.
	AvailabilitySystem = "system"
	// AvailabilityManual creates no platform replica. Users add followers.
	AvailabilityManual = "manual"
)

// ValidatePostgresAvailability accepts only system and manual.
func ValidatePostgresAvailability(mode string) error {
	if err := ValidateAvailability(mode); err != nil {
		return fmt.Errorf("postgres availability must be system or manual")
	}
	return nil
}

// ValidateAvailability accepts only system and manual. The same values apply
// to Postgres, MySQL, Redis, MongoDB, Kafka, and ClickHouse. Tenancy mode
// is not a value.
func ValidateAvailability(mode string) error {
	switch mode {
	case AvailabilitySystem, AvailabilityManual:
		return nil
	default:
		return fmt.Errorf("availability must be system or manual")
	}
}

// PostgresAvailabilityDefault is the value migration 85 writes once.
// Hosted clusters start on system. Every other tenancy, including an
// unknown mode, starts on manual. Later tenancy changes do not call this.
func PostgresAvailabilityDefault(tenancyMode string) string {
	if tenancyMode == ModeHosted {
		return AvailabilitySystem
	}
	return AvailabilityManual
}

// HostedGroupAvailability reports whether plugin:install hosted should
// store system. A fresh cluster (no Postgres resources) moves from manual
// to system. A cluster that already has databases, a failed resource list
// (count < 0), or any value other than manual is left alone.
func HostedGroupAvailability(current string, postgresResources int) (string, bool) {
	if current != AvailabilityManual || postgresResources != 0 {
		return current, false
	}
	return AvailabilitySystem, true
}
