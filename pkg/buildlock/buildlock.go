// Package buildlock holds an app's build lease in the controller (EDGE-09)
// for as long as a push or deploy runs. gitreceive takes it around a push
// (repo cache download, receive-pack and build, repo cache upload);
// flynn-receiver takes it for a deploy nothing above it locked (a GitHub
// deploy through taffy, a preview push). A second push while one runs is
// refused with the first one's description.
package buildlock

import (
	"errors"
	"log"
	"sync"
	"time"

	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/httphelper"
	"github.com/randy-girard/flynn/pkg/random"
)

// HolderEnv names the lease a parent process already holds for the app.
// gitreceive sets it on the pre-receive hook so flynn-receiver does not try
// to take the lease its own push holds.
const HolderEnv = "FLYNN_BUILD_LOCK_HOLDER"

// DefaultTTL is how long a lease outlives its holder. The holder renews it
// every third of that.
const DefaultTTL = 2 * time.Minute

// ErrUnsupported means the controller has no lease API (it is older than
// this process, during a rolling update). Callers go on without the lease.
var ErrUnsupported = errors.New("the controller has no build lock API")

// Store is the controller client subset the lease uses.
type Store interface {
	AcquireBuildLock(appID string, lock *ct.BuildLock) (*ct.BuildLock, error)
	ReleaseBuildLock(appID, holder string) error
}

// KeyStore is the controller client subset for keyed leases (EDGE-40), such
// as "pipeline:shop" for a pipeline's preview remote.
type KeyStore interface {
	AcquireKeyedBuildLock(key string, lock *ct.BuildLock) (*ct.BuildLock, error)
	ReleaseKeyedBuildLock(key, holder string) error
}

// Keyed lets Acquire take a keyed lease: the id Acquire is given is the key.
// A controller from before keyed leases answers 404, which Acquire reports
// as ErrUnsupported like an old controller's missing app lease.
func Keyed(store KeyStore) Store {
	if store == nil {
		return nil
	}
	return keyedStore{store}
}

type keyedStore struct{ KeyStore }

func (k keyedStore) AcquireBuildLock(key string, lock *ct.BuildLock) (*ct.BuildLock, error) {
	got, err := k.AcquireKeyedBuildLock(key, lock)
	var held *ct.BuildLockHeldError
	if errors.As(err, &held) {
		// name the key so the refusal says what is locked
		if held.Lock == nil {
			held.Lock = &ct.BuildLock{}
		}
		if held.Lock.Key == "" {
			held.Lock.Key = key
		}
	}
	return got, err
}

func (k keyedStore) ReleaseBuildLock(key, holder string) error {
	return k.ReleaseKeyedBuildLock(key, holder)
}

// Lease is a held build lock. It renews itself until Release.
type Lease struct {
	store  Store
	appID  string
	holder string
	desc   string
	ttl    time.Duration

	once sync.Once
	stop chan struct{}
	done chan struct{}
}

// Acquire takes appID's lease. Another live holder is a
// *ct.BuildLockHeldError; an old controller is ErrUnsupported.
func Acquire(store Store, appID, description string, ttl time.Duration) (*Lease, error) {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	if ttl < time.Second {
		ttl = time.Second
	}
	l := &Lease{
		store:  store,
		appID:  appID,
		holder: random.Hex(16),
		desc:   description,
		ttl:    ttl,
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}
	if err := l.acquire(); err != nil {
		return nil, err
	}
	go l.renew()
	return l, nil
}

func (l *Lease) acquire() error {
	_, err := l.store.AcquireBuildLock(l.appID, &ct.BuildLock{
		Holder:      l.holder,
		Description: l.desc,
		TTLSeconds:  int(l.ttl / time.Second),
	})
	if err == nil {
		return nil
	}
	var held *ct.BuildLockHeldError
	if errors.As(err, &held) {
		return held
	}
	if errors.Is(err, ct.ErrNotFound) {
		return ErrUnsupported
	}
	var jsonErr httphelper.JSONError
	if errors.As(err, &jsonErr) && jsonErr.Code == httphelper.NotFoundErrorCode {
		return ErrUnsupported
	}
	return err
}

func (l *Lease) renew() {
	defer close(l.done)
	t := time.NewTicker(l.ttl / 3)
	defer t.Stop()
	for {
		select {
		case <-l.stop:
			return
		case <-t.C:
			if err := l.acquire(); err != nil {
				// A held error here means the lease ran out (the controller
				// was unreachable for a whole TTL) and someone else took it.
				log.Printf("buildlock: renewing the lease on %s: %v", l.appID, err)
			}
		}
	}
}

// Holder is the lease's holder id, "" for a nil lease.
func (l *Lease) Holder() string {
	if l == nil {
		return ""
	}
	return l.holder
}

// Release stops renewing and drops the lease. It is safe to call more than
// once and on a nil lease.
func (l *Lease) Release() {
	if l == nil {
		return
	}
	l.once.Do(func() {
		close(l.stop)
		<-l.done
		if err := l.store.ReleaseBuildLock(l.appID, l.holder); err != nil {
			log.Printf("buildlock: releasing the lease on %s: %v (it expires in %s)", l.appID, err, l.ttl)
		}
	})
}
