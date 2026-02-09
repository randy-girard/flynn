package data

import (
	"crypto/md5"
	"crypto/sha256"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	ct "github.com/flynn/flynn/controller/types"
	"github.com/flynn/flynn/pkg/httphelper"
	"github.com/flynn/flynn/pkg/postgres"
	router "github.com/flynn/flynn/router/types"
	"github.com/jackc/pgx"
)

var (
	ErrRouteNotFound        = errors.New("controller: route not found")
	ErrRouteConflict        = errors.New("controller: duplicate route")
	ErrRouteReserved        = errors.New("controller: cannot bind TCP to a reserved port")
	ErrRouteUnreservedHTTP  = errors.New("controller: cannot route HTTP to a non-HTTP port")
	ErrRouteUnreservedHTTPS = errors.New("controller: cannot route HTTPS to a non-HTTPS port")
	ErrRouteInvalid         = errors.New("controller: invalid route")
)

type RouteRepo struct {
	db *postgres.DB
}

func NewRouteRepo(db *postgres.DB) *RouteRepo {
	return &RouteRepo{db: db}
}

func (r *RouteRepo) Add(route *router.Route) error {
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	switch route.Type {
	case "http":
		err = r.addHTTP(tx, route)
	case "tcp":
		err = r.addTCP(tx, route)
	default:
		return ErrRouteInvalid
	}
	if postgres.IsUniquenessError(err, "") {
		err = ErrRouteConflict
	} else if postgres.IsPostgresCode(err, postgres.RaiseException) {
		err = ErrRouteInvalid
	}
	if err != nil {
		tx.Rollback()
		return err
	}

	if err := r.createEvent(tx, route, ct.EventTypeRoute); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (r *RouteRepo) addHTTP(tx *postgres.DBTx, route *router.Route) error {
	// TODO: support non-default HTTP ports
	if route.Port > 0 {
		return ErrRouteInvalid
	}
	if err := tx.QueryRow(
		"http_route_insert",
		route.ParentRef,
		route.Service,
		route.Port,
		route.Leader,
		route.DrainBackends,
		route.Domain,
		route.Sticky,
		route.Path,
		route.DisableKeepAlives,
		route.ManagedCertificateDomain,
	).Scan(&route.ID, &route.Path, &route.CreatedAt, &route.UpdatedAt); err != nil {
		return err
	}

	// Create managed certificate if ManagedCertificateDomain is set
	if route.ManagedCertificateDomain != nil && *route.ManagedCertificateDomain != "" {
		if err := r.createManagedCertificate(tx, route); err != nil {
			return err
		}
	}

	return r.addRouteCertWithTx(tx, route)
}

func (r *RouteRepo) createManagedCertificate(tx *postgres.DBTx, route *router.Route) error {
	var certID string
	var createdAt, updatedAt time.Time
	if err := tx.QueryRow(
		"managed_certificate_insert",
		*route.ManagedCertificateDomain,
		route.ID,
		ct.ManagedCertificateStatusPending,
	).Scan(&certID, &createdAt, &updatedAt); err != nil {
		return err
	}

	// Create event for the new managed certificate
	cert := &ct.ManagedCertificate{
		ID:        certID,
		Domain:    *route.ManagedCertificateDomain,
		RouteID:   route.ID,
		Status:    ct.ManagedCertificateStatusPending,
		CreatedAt: &createdAt,
		UpdatedAt: &updatedAt,
	}
	return CreateEvent(tx.Exec, &ct.Event{
		ObjectID:   certID,
		ObjectType: ct.EventTypeManagedCertificate,
		Op:         ct.EventOpCreate,
	}, cert)
}

// ensureManagedCertificate creates a managed certificate if one doesn't already exist for the route
func (r *RouteRepo) ensureManagedCertificate(tx *postgres.DBTx, route *router.Route) error {
	// Check if a managed certificate already exists for this route
	var existingID string
	err := tx.QueryRow("managed_certificate_select_by_route_id", route.ID).Scan(
		&existingID,
		new(string),     // domain
		new(*string),    // route_id
		new(string),     // status
		new(*string),    // cert
		new(*string),    // key
		new([]byte),     // cert_sha256
		new(*time.Time), // expires_at
		new(*string),    // last_error
		new(*time.Time), // last_error_at
		new(time.Time),  // created_at
		new(time.Time),  // updated_at
	)
	if err == nil {
		// Certificate already exists
		return nil
	}
	if err != pgx.ErrNoRows {
		return err
	}

	// No certificate exists, create one
	return r.createManagedCertificate(tx, route)
}

func (r *RouteRepo) addTCP(tx *postgres.DBTx, route *router.Route) error {
	// TODO: check non-default HTTP ports if set
	if route.Port == 80 || route.Port == 443 {
		return ErrRouteReserved
	}
	return tx.QueryRow(
		"tcp_route_insert",
		route.ParentRef,
		route.Service,
		route.Port,
		route.Leader,
		route.DrainBackends,
	).Scan(&route.ID, &route.Port, &route.CreatedAt, &route.UpdatedAt)
}

func (r *RouteRepo) addCertWithTx(tx *postgres.DBTx, cert *router.Certificate) error {
	cert.Cert = strings.Trim(cert.Cert, " \n")
	cert.Key = strings.Trim(cert.Key, " \n")

	if _, err := tls.X509KeyPair([]byte(cert.Cert), []byte(cert.Key)); err != nil {
		return httphelper.JSONError{
			Code:    httphelper.ValidationErrorCode,
			Message: "Certificate invalid: " + err.Error(),
		}
	}

	tlsCertSHA256 := sha256.Sum256([]byte(cert.Cert))
	if err := tx.QueryRow(
		"certificate_insert",
		cert.Cert,
		cert.Key,
		tlsCertSHA256[:],
	).Scan(&cert.ID, &cert.CreatedAt, &cert.UpdatedAt); err != nil {
		return err
	}
	for _, rid := range cert.Routes {
		if err := tx.Exec("route_certificate_delete_by_route_id", rid); err != nil {
			return err
		}
		if err := tx.Exec("route_certificate_insert", rid, cert.ID); err != nil {
			return err
		}
	}
	return nil
}

func (r *RouteRepo) addRouteCertWithTx(tx *postgres.DBTx, route *router.Route) error {
	var cert *router.Certificate
	if route.LegacyTLSCert != "" || route.LegacyTLSKey != "" {
		cert = &router.Certificate{
			Cert: route.LegacyTLSCert,
			Key:  route.LegacyTLSKey,
		}
	} else {
		cert = route.Certificate
	}
	if cert == nil || (len(cert.Cert) == 0 && len(cert.Key) == 0) {
		return nil
	}
	cert.Routes = []string{route.ID}
	if err := r.addCertWithTx(tx, cert); err != nil {
		return err
	}
	route.Certificate = &router.Certificate{
		ID:        cert.ID,
		Cert:      cert.Cert,
		Key:       cert.Key,
		CreatedAt: cert.CreatedAt,
		UpdatedAt: cert.UpdatedAt,
	}
	return nil
}

func (r *RouteRepo) Get(typ, id string) (*router.Route, error) {
	if id == "" {
		return nil, ErrRouteNotFound
	}
	var (
		route *router.Route
		err   error
	)
	switch typ {
	case "http":
		route, err = r.getHTTP(id)
	case "tcp":
		route, err = r.getTCP(id)
	default:
		err = ErrRouteNotFound
	}
	if err == pgx.ErrNoRows {
		err = ErrRouteNotFound
	}
	return route, err
}

func (r *RouteRepo) getHTTP(id string) (*router.Route, error) {
	return scanHTTPRoute(r.db.QueryRow("http_route_select", id))
}

func scanHTTPRoute(s postgres.Scanner) (*router.Route, error) {
	var (
		route                    router.Route
		managedCertificateDomain *string
		certID                   *string
		certCert                 *string
		certKey                  *string
		certCreatedAt            *time.Time
		certUpdatedAt            *time.Time
	)
	if err := s.Scan(
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

func (r *RouteRepo) getTCP(id string) (*router.Route, error) {
	return scanTCPRoute(r.db.QueryRow("tcp_route_select", id))
}

func scanTCPRoute(s postgres.Scanner) (*router.Route, error) {
	var route router.Route
	if err := s.Scan(
		&route.ID,
		&route.ParentRef,
		&route.Service,
		&route.Port,
		&route.Leader,
		&route.DrainBackends,
		&route.CreatedAt,
		&route.UpdatedAt,
	); err != nil {
		return nil, err
	}
	route.Type = "tcp"
	return &route, nil
}

func (r *RouteRepo) List(parentRef string) ([]*router.Route, error) {
	httpRoutes, err := r.listHTTP(parentRef)
	if err != nil {
		return nil, err
	}
	tcpRoutes, err := r.listTCP(parentRef)
	if err != nil {
		return nil, err
	}
	return append(httpRoutes, tcpRoutes...), nil
}

func (r *RouteRepo) listHTTP(parentRef string) ([]*router.Route, error) {
	var (
		rows *pgx.Rows
		err  error
	)
	if parentRef != "" {
		rows, err = r.db.Query("http_route_list_by_parent_ref", parentRef)
	} else {
		rows, err = r.db.Query("http_route_list")
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var routes []*router.Route
	for rows.Next() {
		route, err := scanHTTPRoute(rows)
		if err != nil {
			return nil, err
		}
		routes = append(routes, route)
	}
	return routes, rows.Err()
}

func (r *RouteRepo) listTCP(parentRef string) ([]*router.Route, error) {
	var (
		rows *pgx.Rows
		err  error
	)
	if parentRef != "" {
		rows, err = r.db.Query("tcp_route_list_by_parent_ref", parentRef)
	} else {
		rows, err = r.db.Query("tcp_route_list")
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var routes []*router.Route
	for rows.Next() {
		route, err := scanTCPRoute(rows)
		if err != nil {
			return nil, err
		}
		routes = append(routes, route)
	}
	return routes, rows.Err()
}

func (r *RouteRepo) Update(route *router.Route) error {
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	switch route.Type {
	case "http":
		err = r.updateHTTP(tx, route)
	case "tcp":
		err = r.updateTCP(tx, route)
	default:
		err = ErrRouteNotFound
	}
	if err == pgx.ErrNoRows {
		err = ErrRouteNotFound
	}
	if err != nil {
		tx.Rollback()
		return err
	}

	if err := r.createEvent(tx, route, ct.EventTypeRoute); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (r *RouteRepo) updateHTTP(tx *postgres.DBTx, route *router.Route) error {
	if err := tx.QueryRow(
		"http_route_update",
		route.ParentRef,
		route.Service,
		route.Port,
		route.Leader,
		route.Sticky,
		route.Path,
		route.DisableKeepAlives,
		route.ManagedCertificateDomain,
		route.ID,
		route.Domain,
	).Scan(
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
		&route.ManagedCertificateDomain,
		&route.CreatedAt,
		&route.UpdatedAt,
	); err != nil {
		return err
	}

	// Create managed certificate if ManagedCertificateDomain is set and doesn't already exist
	if route.ManagedCertificateDomain != nil && *route.ManagedCertificateDomain != "" {
		if err := r.ensureManagedCertificate(tx, route); err != nil {
			return err
		}
	}

	// Check if we need to remove the certificate (when Certificate is nil and no legacy cert)
	hasCert := route.Certificate != nil && (route.Certificate.Cert != "" || route.Certificate.Key != "")
	hasLegacyCert := route.LegacyTLSCert != "" || route.LegacyTLSKey != ""
	if !hasCert && !hasLegacyCert {
		// Remove any existing route-certificate mapping
		if err := tx.Exec("route_certificate_delete_by_route_id", route.ID); err != nil {
			return err
		}
		return nil
	}

	return r.addRouteCertWithTx(tx, route)
}

func (r *RouteRepo) updateTCP(tx *postgres.DBTx, route *router.Route) error {
	return tx.QueryRow(
		"tcp_route_update",
		route.ParentRef,
		route.Service,
		route.Port,
		route.Leader,
		route.ID,
	).Scan(
		&route.ID,
		&route.ParentRef,
		&route.Service,
		&route.Port,
		&route.Leader,
		&route.DrainBackends,
		&route.CreatedAt,
		&route.UpdatedAt,
	)
}

func (r *RouteRepo) Delete(route *router.Route) error {
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	switch route.Type {
	case "http":
		err = tx.Exec("http_route_delete", route.ID)
	case "tcp":
		err = tx.Exec("tcp_route_delete", route.ID)
	default:
		err = ErrRouteNotFound
	}
	if postgres.IsPostgresCode(err, postgres.RaiseException) {
		err = ErrRouteInvalid
	}
	if err != nil {
		tx.Rollback()
		return err
	}
	if err := r.createEvent(tx, route, ct.EventTypeRouteDeletion); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (r *RouteRepo) createEvent(tx *postgres.DBTx, route *router.Route, typ ct.EventType) error {
	var appID string
	if strings.HasPrefix(route.ParentRef, ct.RouteParentRefPrefix) {
		appID = strings.TrimPrefix(route.ParentRef, ct.RouteParentRefPrefix)
	}
	hash := md5.New()
	io.WriteString(hash, appID)
	io.WriteString(hash, string(typ))
	io.WriteString(hash, route.ID)
	io.WriteString(hash, route.CreatedAt.String())
	io.WriteString(hash, route.UpdatedAt.String())
	uniqueID := fmt.Sprintf("%x", hash.Sum(nil))
	return CreateEvent(tx.Exec, &ct.Event{
		AppID:      appID,
		ObjectID:   route.ID,
		ObjectType: typ,
		UniqueID:   uniqueID,
	}, route)
}
