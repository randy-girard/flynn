package data

import (
	"crypto/sha256"
	"crypto/tls"
	"time"

	ct "github.com/flynn/flynn/controller/types"
	"github.com/flynn/flynn/pkg/postgres"
	router "github.com/flynn/flynn/router/types"
	"github.com/jackc/pgx"
)

type ManagedCertificateRepo struct {
	db *postgres.DB
}

func NewManagedCertificateRepo(db *postgres.DB) *ManagedCertificateRepo {
	return &ManagedCertificateRepo{db: db}
}

func (r *ManagedCertificateRepo) Add(cert *ct.ManagedCertificate) error {
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	if cert.Status == "" {
		cert.Status = ct.ManagedCertificateStatusPending
	}
	err = tx.QueryRow("managed_certificate_insert",
		cert.Domain, cert.RouteID, cert.Status,
	).Scan(&cert.ID, &cert.CreatedAt, &cert.UpdatedAt)
	if err != nil {
		tx.Rollback()
		return err
	}

	if err := CreateEvent(tx.Exec, &ct.Event{
		ObjectID:   cert.ID,
		ObjectType: ct.EventTypeManagedCertificate,
		Op:         ct.EventOpCreate,
	}, cert); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (r *ManagedCertificateRepo) Get(id string) (*ct.ManagedCertificate, error) {
	return scanManagedCertificate(r.db.QueryRow("managed_certificate_select", id))
}

func (r *ManagedCertificateRepo) GetByDomain(domain string) (*ct.ManagedCertificate, error) {
	cert, err := scanManagedCertificate(r.db.QueryRow("managed_certificate_select_by_domain", domain))
	if err == pgx.ErrNoRows {
		return nil, ErrNotFound
	}
	return cert, err
}

func (r *ManagedCertificateRepo) GetByRouteID(routeID string) (*ct.ManagedCertificate, error) {
	cert, err := scanManagedCertificate(r.db.QueryRow("managed_certificate_select_by_route_id", routeID))
	if err == pgx.ErrNoRows {
		return nil, ErrNotFound
	}
	return cert, err
}

func (r *ManagedCertificateRepo) List() ([]*ct.ManagedCertificate, error) {
	rows, err := r.db.Query("managed_certificate_list")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanManagedCertificates(rows)
}

func (r *ManagedCertificateRepo) ListSince(since time.Time) ([]*ct.ManagedCertificate, error) {
	rows, err := r.db.Query("managed_certificate_list_since", since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanManagedCertificates(rows)
}

func (r *ManagedCertificateRepo) ListExpiring(before time.Time) ([]*ct.ManagedCertificate, error) {
	rows, err := r.db.Query("managed_certificate_list_expiring", before)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanManagedCertificates(rows)
}

func (r *ManagedCertificateRepo) Update(cert *ct.ManagedCertificate) error {
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}

	var certSHA256 []byte
	if cert.Cert != "" {
		h := sha256.Sum256([]byte(cert.Cert))
		certSHA256 = h[:]
	}

	err = tx.QueryRow("managed_certificate_update",
		cert.ID, cert.Status, cert.Cert, cert.Key, certSHA256,
		cert.ExpiresAt, cert.LastError, cert.LastErrorAt,
	).Scan(&cert.UpdatedAt)
	if err == pgx.ErrNoRows {
		tx.Rollback()
		return ErrNotFound
	}
	if err != nil {
		tx.Rollback()
		return err
	}

	// If the certificate was issued, update the route's certificate
	if cert.Status == ct.ManagedCertificateStatusIssued && cert.Cert != "" && cert.Key != "" && cert.RouteID != "" {
		if err := r.updateRouteCertificate(tx, cert); err != nil {
			tx.Rollback()
			return err
		}

		// Fetch the route with the newly-linked certificate and create an event
		route, err := scanHTTPRouteFromTx(tx, cert.RouteID)
		if err != nil {
			tx.Rollback()
			return err
		}

		// Create a route event so the router picks up the change
		if err := CreateEvent(tx.Exec, &ct.Event{
			ObjectID:   cert.RouteID,
			ObjectType: ct.EventTypeRoute,
			Op:         ct.EventOpUpdate,
		}, route); err != nil {
			tx.Rollback()
			return err
		}
	}

	if err := CreateEvent(tx.Exec, &ct.Event{
		ObjectID:   cert.ID,
		ObjectType: ct.EventTypeManagedCertificate,
		Op:         ct.EventOpUpdate,
	}, cert); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// updateRouteCertificate adds the managed certificate to the route's certificate table
func (r *ManagedCertificateRepo) updateRouteCertificate(tx *postgres.DBTx, cert *ct.ManagedCertificate) error {
	// Validate the certificate
	if _, err := tls.X509KeyPair([]byte(cert.Cert), []byte(cert.Key)); err != nil {
		return err
	}

	// Insert the certificate
	tlsCertSHA256 := sha256.Sum256([]byte(cert.Cert))
	var certID string
	var createdAt, updatedAt time.Time
	if err := tx.QueryRow(
		"certificate_insert",
		cert.Cert,
		cert.Key,
		tlsCertSHA256[:],
	).Scan(&certID, &createdAt, &updatedAt); err != nil {
		return err
	}

	// Delete any existing route certificate mapping
	if err := tx.Exec("route_certificate_delete_by_route_id", cert.RouteID); err != nil {
		return err
	}

	// Link the certificate to the route
	if err := tx.Exec("route_certificate_insert", cert.RouteID, certID); err != nil {
		return err
	}

	return nil
}

// scanHTTPRouteFromTx queries an HTTP route within a transaction
func scanHTTPRouteFromTx(tx *postgres.DBTx, id string) (*router.Route, error) {
	var (
		route                    router.Route
		managedCertificateDomain *string
		certID                   *string
		certCert                 *string
		certKey                  *string
		certCreatedAt            *time.Time
		certUpdatedAt            *time.Time
	)
	if err := tx.QueryRow("http_route_select", id).Scan(
		&route.ID,
		&route.ParentRef,
		&route.Service,
		&route.Port,
		&route.Leader,
		&route.DrainBackends,
		&route.Domain,
		&route.Sticky,
		&route.Path,
		&route.DisableKeepAlives,
		&managedCertificateDomain,
		&route.CreatedAt,
		&route.UpdatedAt,
		&certID,
		&certCert,
		&certKey,
		&certCreatedAt,
		&certUpdatedAt,
	); err != nil {
		return nil, err
	}
	route.ManagedCertificateDomain = managedCertificateDomain
	route.Type = "http"
	if certID != nil {
		route.Certificate = &router.Certificate{
			ID:        *certID,
			Cert:      *certCert,
			Key:       *certKey,
			CreatedAt: *certCreatedAt,
			UpdatedAt: *certUpdatedAt,
		}
	}
	return &route, nil
}

func (r *ManagedCertificateRepo) Delete(id string) error {
	return r.db.Exec("managed_certificate_delete", id)
}

func scanManagedCertificate(s postgres.Scanner) (*ct.ManagedCertificate, error) {
	var cert ct.ManagedCertificate
	var certSHA256 []byte
	// Use pointers for nullable string columns
	var certPEM, keyPEM *string
	err := s.Scan(
		&cert.ID, &cert.Domain, &cert.RouteID, &cert.Status,
		&certPEM, &keyPEM, &certSHA256, &cert.ExpiresAt,
		&cert.LastError, &cert.LastErrorAt, &cert.CreatedAt, &cert.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	if certPEM != nil {
		cert.Cert = *certPEM
	}
	if keyPEM != nil {
		cert.Key = *keyPEM
	}
	return &cert, nil
}

func scanManagedCertificates(rows *pgx.Rows) ([]*ct.ManagedCertificate, error) {
	var certs []*ct.ManagedCertificate
	for rows.Next() {
		cert, err := scanManagedCertificate(rows)
		if err != nil {
			return nil, err
		}
		certs = append(certs, cert)
	}
	return certs, rows.Err()
}
