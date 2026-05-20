package postgres

import (
	"sync"
	"time"

	"github.com/inconshreveable/log15"
	"github.com/jackc/pgx"
)

// Listen creates a listener for the given channel, returning the listener
// and the first connection error (nil on successful connection).
func (db *DB) Listen(channel string, log log15.Logger) (*Listener, error) {
	var l *Listener
	var attempt int
	var lastWaitLog time.Time
	err := listenAttempts.RunWithValidator(func() error {
		attempt++
		conn, err := db.Acquire()
		if err != nil {
			if log != nil && (attempt == 1 || time.Since(lastWaitLog) > 15*time.Second) {
				lastWaitLog = time.Now()
				log.Info("waiting for postgres leader connectivity (LISTEN)", "channel", channel, "attempt", attempt, "err", err)
			}
			return err
		}
		listener := &Listener{
			Notify:  make(chan *pgx.Notification),
			channel: channel,
			log:     log,
			db:      db,
			conn:    conn,
		}
		if err := listener.conn.Listen(channel); err != nil {
			listener.Close()
			db.Release(listener.conn)
			if log != nil && (attempt == 1 || time.Since(lastWaitLog) > 15*time.Second) {
				lastWaitLog = time.Now()
				log.Info("waiting for postgres leader connectivity (LISTEN register)", "channel", channel, "attempt", attempt, "err", err)
			}
			return err
		}
		l = listener
		return nil
	}, isTransientLeaderDialErr)
	if err != nil {
		return nil, err
	}
	go l.listen()
	return l, nil
}

type Listener struct {
	Notify chan *pgx.Notification
	Err    error

	channel   string
	log       log15.Logger
	db        *DB
	conn      *pgx.Conn
	closeOnce sync.Once
}

func (l *Listener) Close() (err error) {
	l.closeOnce.Do(func() {
		err = l.conn.Close()
	})
	return
}

func (l *Listener) listen() {
	defer func() {
		l.Close()
		l.db.Release(l.conn)
		close(l.Notify)
	}()
	for {
		n, err := l.conn.WaitForNotification(10 * time.Second)
		if err == pgx.ErrNotificationTimeout {
			continue
		} else if err != nil {
			l.Err = err
			return
		}
		l.Notify <- n
	}
}
