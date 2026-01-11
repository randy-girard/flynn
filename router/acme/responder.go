package acme

import (
	"net/http"
	"strings"
	"sync"

	"github.com/inconshreveable/log15"
)

// Responder handles HTTP-01 ACME challenges
type Responder struct {
	challenges map[string]string
	mtx        sync.RWMutex
	log        log15.Logger
}

// NewResponder creates a new Responder
func NewResponder(log log15.Logger) *Responder {
	return &Responder{
		challenges: make(map[string]string),
		log:        log.New("component", "acme-responder"),
	}
}

// SetChallenge sets a challenge token and key authorization
func (r *Responder) SetChallenge(token, keyAuth string) {
	r.mtx.Lock()
	defer r.mtx.Unlock()
	r.challenges[token] = keyAuth
	r.log.Info("challenge set", "token", token)
}

// RemoveChallenge removes a challenge token
func (r *Responder) RemoveChallenge(token string) {
	r.mtx.Lock()
	defer r.mtx.Unlock()
	delete(r.challenges, token)
	r.log.Info("challenge removed", "token", token)
}

// GetChallenge returns the key authorization for a token
func (r *Responder) GetChallenge(token string) (string, bool) {
	r.mtx.RLock()
	defer r.mtx.RUnlock()
	keyAuth, ok := r.challenges[token]
	return keyAuth, ok
}

// ServeHTTP handles HTTP-01 challenge requests
func (r *Responder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	// ACME HTTP-01 challenges are at /.well-known/acme-challenge/<token>
	if !strings.HasPrefix(req.URL.Path, "/.well-known/acme-challenge/") {
		http.NotFound(w, req)
		return
	}

	token := strings.TrimPrefix(req.URL.Path, "/.well-known/acme-challenge/")
	keyAuth, ok := r.GetChallenge(token)
	if !ok {
		r.log.Warn("challenge not found", "token", token)
		http.NotFound(w, req)
		return
	}

	r.log.Info("serving challenge", "token", token)
	w.Header().Set("Content-Type", "text/plain")
	w.Write([]byte(keyAuth))
}

