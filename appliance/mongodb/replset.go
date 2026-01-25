package mongodb

import (
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// Config structures

type replSetMember struct {
	ID       int    `bson:"_id"`
	Host     string `bson:"host"`
	Priority int    `bson:"priority"`
	Hidden   bool   `bson:"hidden"`
}

type replSetConfig struct {
	ID      string          `bson:"_id"`
	Members []replSetMember `bson:"members"`
	Version int             `bson:"version"`
}

// Status structures

type replicaState int

// MongoDB replica set member states
// See: https://www.mongodb.com/docs/manual/reference/replica-states/
const (
	Startup    replicaState = 0
	Primary    replicaState = 1
	Secondary  replicaState = 2
	Recovering replicaState = 3
	// Note: State 4 is reserved and not used
	Startup2 replicaState = 5
	Unknown  replicaState = 6
	Arbiter  replicaState = 7
	Down     replicaState = 8
	Rollback replicaState = 9
	Removed  replicaState = 10
)

type replSetOptime struct {
	Timestamp primitive.Timestamp `bson:"ts"`
	Term      int64               `bson:"t"`
}

type replSetStatusMember struct {
	Name      string        `bson:"name"`
	Optime    replSetOptime `bson:"optime"`
	SyncingTo string        `bson:"syncingTo"`
	State     replicaState  `bson:"state"`
}

type replSetStatus struct {
	MyState replicaState          `bson:"myState"`
	Members []replSetStatusMember `bson:"members"`
}
