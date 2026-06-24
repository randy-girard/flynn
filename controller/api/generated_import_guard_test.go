package api

import (
	_ "embed"
	"strings"
	"testing"
)

// Fail fast if generated stubs drift back to non-vendored WKT / genproto paths (-mod=vendor breaks).

//go:embed controller.pb.go
var controllerPBSource []byte

//go:embed controller_grpc.pb.go
var controllerGRPCSource []byte

func TestGeneratedProtobufImportsVendoredModulesOnly(t *testing.T) {
	check := func(name string, src []byte) {
		t.Helper()
		s := string(src)
		for _, bad := range []string{
			`google.golang.org/genproto/protobuf/field_mask`,
			`github.com/golang/protobuf/ptypes/duration`,
			`github.com/golang/protobuf/ptypes/empty`,
			`github.com/golang/protobuf/ptypes/timestamp`,
		} {
			if strings.Contains(s, bad) {
				t.Fatalf("%s imports %q — regenerate with google.golang.org/protobuf/cmd/protoc-gen-go and grpc/cmd/protoc-gen-go-grpc (see builder/img/protoc.sh)", name, bad)
			}
		}
	}
	check("controller.pb.go", controllerPBSource)
	check("controller_grpc.pb.go", controllerGRPCSource)
}
