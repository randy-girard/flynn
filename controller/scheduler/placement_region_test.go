package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestPlacementRequestHasNoRegionFilter(t *testing.T) {
	rt := reflect.TypeOf(PlacementRequest{})
	for i := 0; i < rt.NumField(); i++ {
		name := strings.ToLower(rt.Field(i).Name)
		if strings.Contains(name, "region") {
			t.Fatalf("scheduler placement must not filter by region: %s", rt.Field(i).Name)
		}
	}
}
