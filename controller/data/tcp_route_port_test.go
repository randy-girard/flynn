package data

import (
	"sync"

	. "github.com/flynn/go-check"
	router "github.com/randy-girard/flynn/router/types"
)

func (s *S) deleteTCPParent(c *C, parent string) {
	// A BEFORE DELETE trigger on tcp_routes returns NULL and cancels DELETE.
	// Soft-delete matches production and frees the port in the partial unique index.
	c.Assert(s.db.Exec(`UPDATE tcp_routes SET deleted_at = now() WHERE parent_ref = $1 AND deleted_at IS NULL`, parent), IsNil)
}

func (s *S) TestTCPRoutePortAutoAssignsWindow(c *C) {
	const parent = "e4-auto"
	defer s.deleteTCPParent(c, parent)
	var port int
	c.Assert(s.db.QueryRow(
		`INSERT INTO tcp_routes (parent_ref, service, port) VALUES ($1, 'e4', 0) RETURNING port`,
		parent,
	).Scan(&port), IsNil)
	c.Assert(port >= 10000 && port <= 29999, Equals, true, Commentf("assigned %d", port))
}

func (s *S) TestTCPRoutePortPicksLowestFree(c *C) {
	const parent = "e4-lowest"
	defer s.deleteTCPParent(c, parent)
	c.Assert(s.db.Exec(
		`INSERT INTO tcp_routes (parent_ref, service, port) VALUES ($1, 'e4', 10001)`,
		parent,
	), IsNil)
	var port int
	c.Assert(s.db.QueryRow(
		`INSERT INTO tcp_routes (parent_ref, service, port) VALUES ($1, 'e4', 0) RETURNING port`,
		parent,
	).Scan(&port), IsNil)
	c.Assert(port, Equals, 10000)
}

func (s *S) TestTCPRoutePortReusesSoftDeleted(c *C) {
	const parent = "e4-reuse"
	defer s.deleteTCPParent(c, parent)
	var id string
	var first int
	c.Assert(s.db.QueryRow(
		`INSERT INTO tcp_routes (parent_ref, service, port) VALUES ($1, 'e4', 0) RETURNING id, port`,
		parent,
	).Scan(&id, &first), IsNil)
	c.Assert(s.db.Exec(`UPDATE tcp_routes SET deleted_at = now() WHERE id = $1`, id), IsNil)
	var second int
	c.Assert(s.db.QueryRow(
		`INSERT INTO tcp_routes (parent_ref, service, port) VALUES ($1, 'e4', 0) RETURNING port`,
		parent,
	).Scan(&second), IsNil)
	c.Assert(second, Equals, first)
}

func (s *S) TestTCPRoutePortExplicitSkipsTrigger(c *C) {
	const parent = "e4-explicit"
	defer s.deleteTCPParent(c, parent)
	var port int
	c.Assert(s.db.QueryRow(
		`INSERT INTO tcp_routes (parent_ref, service, port) VALUES ($1, 'e4', 15000) RETURNING port`,
		parent,
	).Scan(&port), IsNil)
	c.Assert(port, Equals, 15000)
}

func (s *S) TestTCPRoutePortRejectsOutsideWindow(c *C) {
	const parent = "e4-reject"
	defer s.deleteTCPParent(c, parent)
	repo := NewRouteRepo(s.db)
	for _, port := range []int{22, 53, 80, 443, 1111, 1113, 3000, 3500, 5000, 5002, 9999, 30000, 32768, 65534} {
		err := repo.Add((&router.TCPRoute{ParentRef: parent, Service: "e4", Port: port}).ToRoute())
		c.Assert(err, NotNil, Commentf("port %d", port))
		c.Assert(err.Error(), Matches, ".*10000-29999.*", Commentf("port %d: %v", port, err))
	}
}

func (s *S) TestTCPRoutePortExhausted(c *C) {
	tx, err := s.db.Begin()
	c.Assert(err, IsNil)
	defer tx.Rollback()
	c.Assert(tx.Exec(`
		INSERT INTO tcp_routes (parent_ref, service, port)
		SELECT 'e4-full', 'e4', port FROM generate_series(10000, 29999) AS port
	`), IsNil)
	var port int
	err = tx.QueryRow(
		`INSERT INTO tcp_routes (parent_ref, service, port) VALUES ('e4-full', 'e4', 0) RETURNING port`,
	).Scan(&port)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Matches, ".*10000-29999 are exhausted.*")
	c.Assert(port, Equals, 0)
}

func (s *S) TestTCPRoutePortUpdateKeepsLegacyPrebind(c *C) {
	const parent = "e4-legacy"
	defer s.deleteTCPParent(c, parent)
	var id string
	c.Assert(s.db.QueryRow(
		`INSERT INTO tcp_routes (parent_ref, service, port) VALUES ($1, 'e4', 3001) RETURNING id`,
		parent,
	).Scan(&id), IsNil)
	repo := NewRouteRepo(s.db)
	route, err := repo.Get("tcp", id)
	c.Assert(err, IsNil)
	route.Service = "e4-renamed"
	c.Assert(repo.Update(route), IsNil)
	got, err := repo.Get("tcp", id)
	c.Assert(err, IsNil)
	c.Assert(got.Port, Equals, int32(3001))
	c.Assert(got.Service, Equals, "e4-renamed")
}

func (s *S) TestTCPRoutePortUpdateRejectsMoveOutsideWindow(c *C) {
	const parent = "e4-move"
	defer s.deleteTCPParent(c, parent)
	var id string
	c.Assert(s.db.QueryRow(
		`INSERT INTO tcp_routes (parent_ref, service, port) VALUES ($1, 'e4', 3001) RETURNING id`,
		parent,
	).Scan(&id), IsNil)
	repo := NewRouteRepo(s.db)
	route, err := repo.Get("tcp", id)
	c.Assert(err, IsNil)
	route.Port = 22
	err = repo.Update(route)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Matches, ".*10000-29999.*")
	got, err := repo.Get("tcp", id)
	c.Assert(err, IsNil)
	c.Assert(got.Port, Equals, int32(3001))
}

func (s *S) TestTCPRoutePortConcurrentAssignsDistinct(c *C) {
	const parent = "e4-conc"
	defer s.deleteTCPParent(c, parent)
	type result struct {
		port int
		err  error
	}
	ch := make(chan result, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func() {
			defer wg.Done()
			var port int
			err := s.db.QueryRow(
				`INSERT INTO tcp_routes (parent_ref, service, port) VALUES ($1, 'e4', 0) RETURNING port`,
				parent,
			).Scan(&port)
			ch <- result{port: port, err: err}
		}()
	}
	wg.Wait()
	close(ch)
	var ports []int
	for res := range ch {
		c.Assert(res.err, IsNil)
		c.Assert(res.port >= 10000 && res.port <= 29999, Equals, true, Commentf("assigned %d", res.port))
		ports = append(ports, res.port)
	}
	c.Assert(ports[0], Not(Equals), ports[1])
}
