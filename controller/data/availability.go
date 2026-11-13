package data

import (
	"github.com/jackc/pgx"
	"github.com/randy-girard/flynn/controller/tenancy"
)

// GetPostgresAvailability reads cluster_settings.postgres_continuous_availability.
// A missing row is manual. It is not inferred from tenancy_mode.
func (r *TenancyRepo) GetPostgresAvailability() (string, error) {
	var mode string
	err := r.db.QueryRow(`SELECT postgres_continuous_availability FROM cluster_settings WHERE id = 1`).Scan(&mode)
	if err == pgx.ErrNoRows {
		return tenancy.AvailabilityManual, nil
	}
	if err != nil {
		return "", err
	}
	if err := tenancy.ValidatePostgresAvailability(mode); err != nil {
		return "", err
	}
	return mode, nil
}

// SetPostgresAvailability stores system or manual. It does not change tenancy_mode.
func (r *TenancyRepo) SetPostgresAvailability(mode string) error {
	if err := tenancy.ValidatePostgresAvailability(mode); err != nil {
		return err
	}
	return r.db.Exec(`UPDATE cluster_settings SET postgres_continuous_availability = $1 WHERE id = 1`, mode)
}

// GetMySQLAvailability reads cluster_settings.mysql_continuous_availability.
// A missing row is manual. It is not inferred from tenancy_mode.
func (r *TenancyRepo) GetMySQLAvailability() (string, error) {
	return r.getAvailabilityColumn("mysql_continuous_availability")
}

// SetMySQLAvailability stores system or manual. It does not change tenancy_mode.
func (r *TenancyRepo) SetMySQLAvailability(mode string) error {
	return r.setAvailabilityColumn("mysql_continuous_availability", mode)
}

// GetRedisAvailability reads cluster_settings.redis_continuous_availability.
// A missing row is manual. It is not inferred from tenancy_mode.
func (r *TenancyRepo) GetRedisAvailability() (string, error) {
	return r.getAvailabilityColumn("redis_continuous_availability")
}

// SetRedisAvailability stores system or manual. It does not change tenancy_mode.
func (r *TenancyRepo) SetRedisAvailability(mode string) error {
	return r.setAvailabilityColumn("redis_continuous_availability", mode)
}

// GetMongoDBAvailability reads cluster_settings.mongodb_continuous_availability.
// A missing row is manual. It is not inferred from tenancy_mode.
func (r *TenancyRepo) GetMongoDBAvailability() (string, error) {
	return r.getAvailabilityColumn("mongodb_continuous_availability")
}

// SetMongoDBAvailability stores system or manual. It does not change tenancy_mode.
func (r *TenancyRepo) SetMongoDBAvailability(mode string) error {
	return r.setAvailabilityColumn("mongodb_continuous_availability", mode)
}

// GetKafkaAvailability reads cluster_settings.kafka_continuous_availability.
// A missing row is manual. It is not inferred from tenancy_mode.
func (r *TenancyRepo) GetKafkaAvailability() (string, error) {
	return r.getAvailabilityColumn("kafka_continuous_availability")
}

// SetKafkaAvailability stores system or manual. It does not change tenancy_mode.
func (r *TenancyRepo) SetKafkaAvailability(mode string) error {
	return r.setAvailabilityColumn("kafka_continuous_availability", mode)
}

// GetClickHouseAvailability reads cluster_settings.clickhouse_continuous_availability.
// A missing row is manual. It is not inferred from tenancy_mode.
func (r *TenancyRepo) GetClickHouseAvailability() (string, error) {
	return r.getAvailabilityColumn("clickhouse_continuous_availability")
}

// SetClickHouseAvailability stores system or manual. It does not change tenancy_mode.
func (r *TenancyRepo) SetClickHouseAvailability(mode string) error {
	return r.setAvailabilityColumn("clickhouse_continuous_availability", mode)
}

func availabilityColumn(column string) bool {
	switch column {
	case "mysql_continuous_availability", "redis_continuous_availability",
		"mongodb_continuous_availability", "kafka_continuous_availability", "clickhouse_continuous_availability":
		return true
	default:
		return false
	}
}

func (r *TenancyRepo) getAvailabilityColumn(column string) (string, error) {
	if !availabilityColumn(column) {
		return "", tenancy.ValidateAvailability("nope")
	}
	var mode string
	err := r.db.QueryRow(`SELECT ` + column + ` FROM cluster_settings WHERE id = 1`).Scan(&mode)
	if err == pgx.ErrNoRows {
		return tenancy.AvailabilityManual, nil
	}
	if err != nil {
		return "", err
	}
	if err := tenancy.ValidateAvailability(mode); err != nil {
		return "", err
	}
	return mode, nil
}

func (r *TenancyRepo) setAvailabilityColumn(column, mode string) error {
	if !availabilityColumn(column) {
		return tenancy.ValidateAvailability("nope")
	}
	if err := tenancy.ValidateAvailability(mode); err != nil {
		return err
	}
	return r.db.Exec(`UPDATE cluster_settings SET `+column+` = $1 WHERE id = 1`, mode)
}
