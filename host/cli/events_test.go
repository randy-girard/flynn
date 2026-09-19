package cli

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestDecodeEventChartSettings(t *testing.T) {
	res := &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(`{"codes":["H18","H19"],"all":false,"known":[{"code":"H19","label":"Scale down","visible":true}]}`)),
	}
	got, err := decodeEventChartSettings(res)
	if err != nil {
		t.Fatal(err)
	}
	if got.All || len(got.Codes) != 2 || got.Known[0].Code != "H19" {
		t.Fatalf("%+v", got)
	}
}

func TestDashboardPluginBase(t *testing.T) {
	if got := dashboardPluginBase(nil); got != "http://dashboard.discoverd" {
		t.Fatalf("got %s", got)
	}
}
