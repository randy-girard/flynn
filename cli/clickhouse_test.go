package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestClickhouseAdmin(t *testing.T) {
	got := clickhouseAdmin("--query", "SELECT 1")
	want := []string{"/bin/flynn-clickhouse", "admin", "clickhouse-client", "--query", "SELECT 1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestCreateDatabaseQuery(t *testing.T) {
	got := createDatabaseQuery("analytics", "flynn")
	want := "CREATE DATABASE IF NOT EXISTS `analytics` ON CLUSTER `flynn` ENGINE = Atomic"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestDropDatabaseQuery(t *testing.T) {
	got := dropDatabaseQuery("analytics", "flynn")
	want := "DROP DATABASE IF EXISTS `analytics` ON CLUSTER `flynn` SYNC"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestEscapeClickhouseIdentifier(t *testing.T) {
	if got, want := escapeClickhouseIdentifier("analytics"), "analytics"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got, want := escapeClickhouseIdentifier("a`b"), "a``b"; got != want {
		t.Fatalf("backtick escape: got %q, want %q", got, want)
	}
	got := createDatabaseQuery("a`b", "flynn")
	if !strings.Contains(got, "`a``b`") || !strings.Contains(got, "ON CLUSTER `flynn`") {
		t.Fatalf("ON CLUSTER query must escape identifiers: %q", got)
	}
}
