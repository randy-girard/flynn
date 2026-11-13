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

func (r *TenancyRepo) getAvailabilityColumn(column string) (string, error) {
	if column != "mysql_continuous_availability" && column != "redis_continuous_availability" {
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
	if column != "mysql_continuous_availability" && column != "redis_continuous_availability" {
		return tenancy.ValidateAvailability("nope")
	}
	if err := tenancy.ValidateAvailability(mode); err != nil {
		return err
	}
	return r.db.Exec(`UPDATE cluster_settings SET `+column+` = $1 WHERE id = 1`, mode)
}
