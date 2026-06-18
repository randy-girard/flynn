package client

import (
	"crypto/md5"
	"encoding/hex"
	"testing"

	discoverd "github.com/flynn/flynn/discoverd/client"
)

func mkInst(addr, id string) *discoverd.Instance {
	inst := &discoverd.Instance{
		Addr:  addr,
		Proto: "tcp",
		Meta:  map[string]string{"POSTGRES_ID": id},
	}
	sum := md5.Sum([]byte(inst.Proto + "-" + inst.Addr))
	inst.ID = hex.EncodeToString(sum[:])
	return inst
}

func TestSyncedWithUsesMetaIdentity(t *testing.T) {
	oldSync := mkInst("10.0.0.2:5432", "old-sync")
	newSync := mkInst("10.0.0.2:5432", "new-sync")
	if oldSync.ID != newSync.ID {
		t.Fatalf("test precondition: peers should share discoverd ID")
	}

	check := SyncedWith(newSync, "POSTGRES_ID")

	statusOldID := &Status{
		Database: &DatabaseInfo{
			SyncedDownstream: oldSync,
		},
	}
	if check(statusOldID) {
		t.Fatal("expected false when Meta identity differs despite same discoverd ID")
	}

	statusNewID := &Status{
		Database: &DatabaseInfo{
			SyncedDownstream: newSync,
		},
	}
	if !check(statusNewID) {
		t.Fatal("expected true when Meta identity matches")
	}

	statusNil := &Status{Database: &DatabaseInfo{}}
	if check(statusNil) {
		t.Fatal("expected false when SyncedDownstream is nil")
	}
}

func TestSyncedWithFallsBackToDiscoverdID(t *testing.T) {
	downstream := mkInst("10.0.0.3:5432", "async-1")
	check := SyncedWith(downstream, "")

	if !check(&Status{Database: &DatabaseInfo{SyncedDownstream: downstream}}) {
		t.Fatal("expected true when discoverd IDs match and idKey is empty")
	}
	other := mkInst("10.0.0.4:5432", "async-2")
	if check(&Status{Database: &DatabaseInfo{SyncedDownstream: other}}) {
		t.Fatal("expected false when discoverd IDs differ")
	}
}

func TestSyncedWithIgnoresEmptyMeta(t *testing.T) {
	expected := &discoverd.Instance{
		Addr:  "10.0.0.2:5432",
		Proto: "tcp",
		Meta:  map[string]string{"POSTGRES_ID": ""},
	}
	synced := mkInst("10.0.0.2:5432", "other")
	check := SyncedWith(expected, "POSTGRES_ID")
	if check(&Status{Database: &DatabaseInfo{SyncedDownstream: synced}}) {
		t.Fatal("expected false when expected Meta id is empty")
	}
}
