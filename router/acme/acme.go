package acme

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	acmelib "github.com/eggsampler/acme/v3"
	controller "github.com/flynn/flynn/controller/client"
	ct "github.com/flynn/flynn/controller/types"
	discoverd "github.com/flynn/flynn/discoverd/client"
	"github.com/flynn/flynn/pkg/attempt"
	"github.com/flynn/flynn/pkg/stream"
	router "github.com/flynn/flynn/router/types"
	"github.com/inconshreveable/log15"
)

// DefaultDirectoryURL is the default ACME directory URL (Let's Encrypt production)
const DefaultDirectoryURL = "https://acme-v02.api.letsencrypt.org/directory"

// StagingDirectoryURL is the Let's Encrypt staging URL for testing
const StagingDirectoryURL = "https://acme-staging-v02.api.letsencrypt.org/directory"

// ControllerClient is an interface that provides streaming and updating of managed
// certificates, and the creation and deletion of routes
type ControllerClient interface {
	StreamManagedCertificates(since *time.Time, output chan *ct.ManagedCertificate) (stream.Stream, error)
	UpdateManagedCertificate(cert *ct.ManagedCertificate) error
	CreateRoute(appID string, route *router.Route) error
	DeleteRoute(appID string, routeID string) error
}

// controllerClientWrapper wraps a controller.Client to implement ControllerClient
type controllerClientWrapper struct {
	client controller.Client
}

func (w *controllerClientWrapper) StreamManagedCertificates(since *time.Time, output chan *ct.ManagedCertificate) (stream.Stream, error) {
	return w.client.StreamManagedCertificates(since, output)
}

func (w *controllerClientWrapper) UpdateManagedCertificate(cert *ct.ManagedCertificate) error {
	return w.client.UpdateManagedCertificate(cert)
}

func (w *controllerClientWrapper) CreateRoute(appID string, route *router.Route) error {
	return w.client.CreateRoute(appID, route)
}

func (w *controllerClientWrapper) DeleteRoute(appID string, routeID string) error {
	return w.client.DeleteRoute(appID, routeID)
}

// ACME manages ACME accounts and starts services for handling certificate issuance
type ACME struct {
	client *acmelib.Client
	log    log15.Logger
}

// New returns an ACME object that uses the given directoryURL
func New(directoryURL string, log log15.Logger, opts ...acmelib.OptionFunc) (*ACME, error) {
	if directoryURL == "" {
		directoryURL = DefaultDirectoryURL
	}
	client, err := acmelib.NewClient(directoryURL, opts...)
	if err != nil {
		return nil, fmt.Errorf("error initializing ACME client: %s", err)
	}
	return &ACME{
		client: &client,
		log:    log,
	}, nil
}

// ACMEAccount returns the given account's existing acme.Account
func (a *ACME) ACMEAccount(account *Account) (acmelib.Account, error) {
	privKey, err := account.PrivateKey()
	if err != nil {
		return acmelib.Account{}, err
	}
	return a.client.NewAccount(privKey, true, account.TermsOfServiceAgreed, account.Contacts...)
}

// CheckExistingAccount checks that the given ACME account exists
func (a *ACME) CheckExistingAccount(account *Account) error {
	_, err := a.ACMEAccount(account)
	if err != nil {
		return fmt.Errorf("error getting existing ACME account: %s", err)
	}
	return nil
}

// CreateAccount creates the given ACME account and generates a new key
func (a *ACME) CreateAccount(account *Account) error {
	privKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("error generating ACME account key: %s", err)
	}
	if _, err := a.client.NewAccount(privKey, false, account.TermsOfServiceAgreed, account.Contacts...); err != nil {
		return fmt.Errorf("error creating ACME account: %s", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(privKey)
	if err != nil {
		return fmt.Errorf("error encoding private key: %s", err)
	}
	account.Key = string(pem.EncodeToMemory(&pem.Block{
		Type:  "EC PRIVATE KEY",
		Bytes: keyDER,
	}))
	return nil
}

// Service orders certificates for pending managed certificates using the ACME protocol
type Service struct {
	client      *acmelib.Client
	account     acmelib.Account
	controller  ControllerClient
	responder   *Responder
	handling    map[string]struct{}
	handlingMtx sync.Mutex
	stop        chan struct{}
	done        chan struct{}
	log         log15.Logger
}

// NewService returns a Service that uses the given account, controller client and responder
func (a *ACME) NewService(account *Account, controllerClient ControllerClient, responder *Responder) (*Service, error) {
	log := a.log.New("account", account.KeyID())
	log.Info("initializing ACME service")
	acmeAccount, err := a.ACMEAccount(account)
	if err != nil {
		log.Error("error initializing ACME service", "err", err)
		return nil, err
	}
	return &Service{
		client:     a.client,
		account:    acmeAccount,
		controller: controllerClient,
		responder:  responder,
		handling:   make(map[string]struct{}),
		stop:       make(chan struct{}),
		done:       make(chan struct{}),
		log:        log,
	}, nil
}

// RunService runs an ACME service with configuration from environment variables
func RunService(ctx context.Context) error {
	log := log15.New("component", "acme")

	log.Info("getting ACME account from environment variables")
	account, err := NewAccountFromEnv()
	if err != nil {
		log.Error("error getting ACME account from environment variables", "err", err)
		return err
	}

	log.Info("initializing controller client")
	instances, err := discoverd.NewService("controller").Instances()
	if err != nil {
		log.Error("error initializing controller client", "err", err)
		return err
	}
	inst := instances[0]
	client, err := controller.NewClient("http://"+inst.Addr, inst.Meta["AUTH_KEY"])
	if err != nil {
		log.Error("error initializing controller client", "err", err)
		return err
	}

	directoryURL := os.Getenv("ACME_DIRECTORY_URL")
	if directoryURL == "" {
		directoryURL = DefaultDirectoryURL
	}
	log.Info("initializing ACME client", "directory", directoryURL)
	acme, err := New(directoryURL, log)
	if err != nil {
		log.Error("error initializing ACME client", "err", err)
		return err
	}

	log.Info("initializing ACME responder")
	responder := NewResponder(log)

	log.Info("initializing ACME service")
	controllerWrapper := &controllerClientWrapper{client: client}
	service, err := acme.NewService(account, controllerWrapper, responder)
	if err != nil {
		log.Error("error initializing ACME service", "err", err)
		return err
	}

	// Start HTTP server for ACME challenge responses
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	addr := ":" + port
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		log.Error("error starting HTTP listener", "addr", addr, "err", err)
		return err
	}
	defer listener.Close()

	// Register with discoverd so the router can find us
	log.Info("registering with discoverd", "service", "acme-challenge", "addr", addr)
	hb, err := discoverd.AddServiceAndRegister("acme-challenge", addr)
	if err != nil {
		log.Error("error registering with discoverd", "err", err)
		return err
	}
	defer hb.Close()

	// Start HTTP server in a goroutine
	server := &http.Server{Handler: responder}
	go func() {
		log.Info("starting HTTP server for ACME challenges", "addr", addr)
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Error("HTTP server error", "err", err)
		}
	}()

	log.Info("starting ACME service")
	go service.Run()

	<-ctx.Done()
	log.Info("stopping ACME service")
	service.Stop()

	log.Info("shutting down HTTP server")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	server.Shutdown(shutdownCtx)

	return nil
}

// Run starts the service and handles pending managed certificates
func (s *Service) Run() {
	defer close(s.done)
	s.log.Info("starting ACME service")

	certs := make(chan *ct.ManagedCertificate)
	stream, err := s.controller.StreamManagedCertificates(nil, certs)
	if err != nil {
		s.log.Error("error streaming managed certificates", "err", err)
		return
	}
	defer stream.Close()

	for {
		select {
		case cert := <-certs:
			if cert == nil {
				continue
			}
			if cert.Status != ct.ManagedCertificateStatusPending {
				continue
			}
			s.handlingMtx.Lock()
			if _, ok := s.handling[cert.Domain]; ok {
				s.handlingMtx.Unlock()
				continue
			}
			s.handling[cert.Domain] = struct{}{}
			s.handlingMtx.Unlock()
			go s.handleCertificate(cert)
		case <-s.stop:
			return
		}
	}
}

// Stop stops the service
func (s *Service) Stop() {
	close(s.stop)
	<-s.done
}

// handleCertificate handles a pending managed certificate
func (s *Service) handleCertificate(cert *ct.ManagedCertificate) {
	defer func() {
		s.handlingMtx.Lock()
		delete(s.handling, cert.Domain)
		s.handlingMtx.Unlock()
	}()

	log := s.log.New("domain", cert.Domain)
	log.Info("handling managed certificate")

	// Create a new order
	order, err := s.client.NewOrder(s.account, []acmelib.Identifier{{Type: "dns", Value: cert.Domain}})
	if err != nil {
		log.Error("error creating ACME order", "err", err)
		cert.Status = ct.ManagedCertificateStatusFailed
		cert.AddError("order_error", err.Error())
		s.controller.UpdateManagedCertificate(cert)
		return
	}
	cert.OrderURL = order.URL
	s.controller.UpdateManagedCertificate(cert)

	// Process authorizations
	for _, authURL := range order.Authorizations {
		auth, err := s.client.FetchAuthorization(s.account, authURL)
		if err != nil {
			log.Error("error fetching authorization", "err", err)
			cert.Status = ct.ManagedCertificateStatusFailed
			cert.AddError("auth_error", err.Error())
			s.controller.UpdateManagedCertificate(cert)
			return
		}

		// Find HTTP-01 challenge
		var challenge acmelib.Challenge
		for _, c := range auth.Challenges {
			if c.Type == acmelib.ChallengeTypeHTTP01 {
				challenge = c
				break
			}
		}
		if challenge.URL == "" {
			log.Error("no HTTP-01 challenge found")
			cert.Status = ct.ManagedCertificateStatusFailed
			cert.AddError("challenge_error", "no HTTP-01 challenge found")
			s.controller.UpdateManagedCertificate(cert)
			return
		}

		// Set up the challenge response using the key authorization from the challenge
		keyAuth := challenge.Token + "." + s.account.Thumbprint
		s.responder.SetChallenge(challenge.Token, keyAuth)
		defer s.responder.RemoveChallenge(challenge.Token)

		// Update the challenge
		if _, err := s.client.UpdateChallenge(s.account, challenge); err != nil {
			log.Error("error updating challenge", "err", err)
			cert.Status = ct.ManagedCertificateStatusFailed
			cert.AddError("challenge_error", err.Error())
			s.controller.UpdateManagedCertificate(cert)
			return
		}
	}

	// Wait for order to be ready
	order, err = s.waitForOrder(order)
	if err != nil {
		log.Error("error waiting for order", "err", err)
		cert.Status = ct.ManagedCertificateStatusFailed
		cert.AddError("order_error", err.Error())
		s.controller.UpdateManagedCertificate(cert)
		return
	}

	// Generate a new key and CSR
	privKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		log.Error("error generating private key", "err", err)
		cert.Status = ct.ManagedCertificateStatusFailed
		cert.AddError("key_error", err.Error())
		s.controller.UpdateManagedCertificate(cert)
		return
	}
	csrTemplate := &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: cert.Domain},
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, csrTemplate, privKey)
	if err != nil {
		log.Error("error creating CSR", "err", err)
		cert.Status = ct.ManagedCertificateStatusFailed
		cert.AddError("csr_error", err.Error())
		s.controller.UpdateManagedCertificate(cert)
		return
	}
	csr, err := x509.ParseCertificateRequest(csrDER)
	if err != nil {
		log.Error("error parsing CSR", "err", err)
		cert.Status = ct.ManagedCertificateStatusFailed
		cert.AddError("csr_error", err.Error())
		s.controller.UpdateManagedCertificate(cert)
		return
	}

	// Finalize the order
	order, err = s.client.FinalizeOrder(s.account, order, csr)
	if err != nil {
		log.Error("error finalizing order", "err", err)
		cert.Status = ct.ManagedCertificateStatusFailed
		cert.AddError("finalize_error", err.Error())
		s.controller.UpdateManagedCertificate(cert)
		return
	}

	// Fetch the certificate
	certs, err := s.client.FetchCertificates(s.account, order.Certificate)
	if err != nil {
		log.Error("error fetching certificate", "err", err)
		cert.Status = ct.ManagedCertificateStatusFailed
		cert.AddError("fetch_error", err.Error())
		s.controller.UpdateManagedCertificate(cert)
		return
	}

	// Encode the private key
	keyDER, err := x509.MarshalECPrivateKey(privKey)
	if err != nil {
		log.Error("error encoding private key", "err", err)
		cert.Status = ct.ManagedCertificateStatusFailed
		cert.AddError("key_error", err.Error())
		s.controller.UpdateManagedCertificate(cert)
		return
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "EC PRIVATE KEY",
		Bytes: keyDER,
	})

	// Build the PEM-encoded certificate chain
	var certPEM []byte
	for _, c := range certs {
		certPEM = append(certPEM, pem.EncodeToMemory(&pem.Block{
			Type:  "CERTIFICATE",
			Bytes: c.Raw,
		})...)
	}

	// Set expiry from the leaf certificate
	if len(certs) > 0 {
		cert.ExpiresAt = &certs[0].NotAfter
	}

	// Update the managed certificate with the issued cert
	// The controller will also update the route's certificate automatically
	cert.Status = ct.ManagedCertificateStatusIssued
	cert.Cert = string(certPEM)
	cert.Key = string(keyPEM)
	if err := s.controller.UpdateManagedCertificate(cert); err != nil {
		log.Error("error updating managed certificate", "err", err)
		return
	}
	log.Info("certificate issued and route updated successfully")
}

// waitForOrder waits for an order to be ready
func (s *Service) waitForOrder(order acmelib.Order) (acmelib.Order, error) {
	strategy := attempt.Strategy{
		Total: 5 * time.Minute,
		Delay: 5 * time.Second,
	}
	var err error
	for a := strategy.Start(); a.Next(); {
		order, err = s.client.FetchOrder(s.account, order.URL)
		if err != nil {
			return order, err
		}
		if order.Status == "ready" || order.Status == "valid" {
			return order, nil
		}
		if order.Status == "invalid" {
			return order, fmt.Errorf("order is invalid")
		}
	}
	return order, fmt.Errorf("timed out waiting for order")
}
