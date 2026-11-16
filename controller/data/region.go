package data

import (
	"strings"

	"github.com/jackc/pgx"
	ct "github.com/randy-girard/flynn/controller/types"
)

func (r *TenancyRepo) GetRegion() (string, error) {
	if r == nil || r.db == nil {
		return "", ErrNotFound
	}
	var name string
	err := r.db.QueryRow(`SELECT region FROM cluster_settings WHERE id = 1`).Scan(&name)
	if err == pgx.ErrNoRows {
		return "", ErrNotFound
	}
	return strings.TrimSpace(name), err
}

func (r *TenancyRepo) SetRegion(name string) error {
	if r == nil || r.db == nil {
		return ErrNotFound
	}
	return r.db.Exec(`UPDATE cluster_settings SET region = $1 WHERE id = 1`, name)
}

func (r *TenancyRepo) ListRegionLinks() ([]ct.RegionLink, error) {
	if r == nil || r.db == nil {
		return nil, ErrNotFound
	}
	rows, err := r.db.Query(`SELECT region, domain, controller_url, dashboard_url, tls_pin FROM cluster_region_links ORDER BY region`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ct.RegionLink{}
	for rows.Next() {
		var link ct.RegionLink
		if err := rows.Scan(&link.Region, &link.Domain, &link.ControllerURL, &link.DashboardURL, &link.TLSPin); err != nil {
			return nil, err
		}
		out = append(out, link)
	}
	return out, rows.Err()
}

func (r *TenancyRepo) UpsertRegionLink(link ct.RegionLink) error {
	if r == nil || r.db == nil {
		return ErrNotFound
	}
	return r.db.Exec(`INSERT INTO cluster_region_links (region, domain, controller_url, dashboard_url, tls_pin)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (region) DO UPDATE SET
			domain = EXCLUDED.domain,
			controller_url = EXCLUDED.controller_url,
			dashboard_url = EXCLUDED.dashboard_url,
			tls_pin = EXCLUDED.tls_pin`,
		link.Region, link.Domain, link.ControllerURL, link.DashboardURL, link.TLSPin)
}

func (r *TenancyRepo) DeleteRegionLink(name string) error {
	if r == nil || r.db == nil {
		return ErrNotFound
	}
	var got string
	err := r.db.QueryRow(`DELETE FROM cluster_region_links WHERE region = $1 RETURNING region`, name).Scan(&got)
	if err == pgx.ErrNoRows {
		return ErrNotFound
	}
	return err
}
