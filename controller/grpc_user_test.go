package main

import (
	"context"
	"io"
	"strings"
	"time"

	. "github.com/flynn/go-check"
	"github.com/randy-girard/flynn/controller/api"
	"github.com/randy-girard/flynn/controller/tokensigner"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/random"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	emptypb "google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// grpcUser creates a users row for the gRPC suite.
func (s *GRPCSuite) grpcUser(c *C, admin bool) *ct.User {
	id := random.UUID()
	u := &ct.User{
		ID:            id,
		Handle:        "grpc-" + id[:8],
		Email:         "grpc-" + id[:8] + "@flynn.test",
		ClusterAdmin:  admin,
		EmailVerified: true,
	}
	c.Assert(s.api.tenancy.CreateUser(u), IsNil)
	return u
}

// grpcClientFor dials the suite server with a cluster:admin login token
// signed for u at issued.
func (s *GRPCSuite) grpcClientFor(c *C, u *ct.User, issued time.Time) api.ControllerClient {
	tok, err := tokensigner.New(s.tokenKey).Sign(&api.AccessToken{
		UserId:     u.ID,
		UserEmail:  u.Email,
		IssueTime:  timestamppb.New(issued),
		ExpireTime: timestamppb.New(issued.Add(30 * time.Minute)),
		Scopes:     []string{"cluster:admin"},
	})
	c.Assert(err, IsNil)
	return s.dial(grpc.WithPerRPCCredentials(&grpcBearerCreds{token: "flynn." + tok}))
}

// grpcCodes calls one unary RPC (Status) and one stream RPC (StreamApps) and
// returns both status codes and the stream error message.
func grpcCodes(c *C, client api.ControllerClient) (unary, stream codes.Code, streamMsg string) {
	ctx, cancel := context.WithTimeout(context.Background(), grpcStreamTestTimeout)
	defer cancel()
	_, err := client.Status(ctx, &emptypb.Empty{})
	unary = status.Code(err)
	st, err := client.StreamApps(ctx, &api.StreamAppsRequest{})
	c.Assert(err, IsNil)
	_, err = st.Recv()
	if err == io.EOF {
		err = nil
	}
	stream = status.Code(err)
	streamMsg = status.Convert(err).Message()
	return
}

// A login token keeps the cluster:admin claim it was signed with. gRPC must
// take admin from users.cluster_admin, as HTTP does, so a demoted admin loses
// every admin-only RPC at once instead of at token expiry.
func (s *GRPCSuite) TestGRPCDemotedAdminTokenIsNotAdmin(c *C) {
	u := s.grpcUser(c, true)
	client := s.grpcClientFor(c, u, time.Now())

	unary, stream, _ := grpcCodes(c, client)
	c.Assert(unary, Equals, codes.OK)
	c.Assert(stream, Equals, codes.OK)

	u.ClusterAdmin = false
	c.Assert(s.api.tenancy.UpdateUser(u), IsNil)

	unary, stream, _ = grpcCodes(c, client)
	c.Assert(unary, Equals, codes.PermissionDenied)
	c.Assert(stream, Equals, codes.PermissionDenied)
}

// A suspended or disabled user, or a user whose account is suspended, is
// refused on gRPC with the same message HTTP uses.
func (s *GRPCSuite) TestGRPCRejectsSuspendedUser(c *C) {
	for _, tc := range []struct {
		name  string
		block func(u *ct.User)
	}{
		{"suspended", func(u *ct.User) {
			u.Suspended = true
			c.Assert(s.api.tenancy.UpdateUser(u), IsNil)
		}},
		{"disabled", func(u *ct.User) {
			u.Disabled = true
			c.Assert(s.api.tenancy.UpdateUser(u), IsNil)
		}},
		{"account suspended", func(u *ct.User) {
			c.Assert(s.api.tenancy.SetAccountSuspended("user:"+u.ID, true), IsNil)
		}},
	} {
		u := s.grpcUser(c, true)
		client := s.grpcClientFor(c, u, time.Now())
		unary, stream, _ := grpcCodes(c, client)
		c.Assert(unary, Equals, codes.OK, Commentf(tc.name))
		c.Assert(stream, Equals, codes.OK, Commentf(tc.name))

		tc.block(u)

		unary, stream, msg := grpcCodes(c, client)
		c.Assert(unary, Equals, codes.PermissionDenied, Commentf(tc.name))
		c.Assert(stream, Equals, codes.PermissionDenied, Commentf(tc.name))
		c.Assert(strings.Contains(msg, "suspended"), Equals, true, Commentf("%s: %q", tc.name, msg))
	}
}

// A password change revokes login tokens signed before it on gRPC too.
func (s *GRPCSuite) TestGRPCRejectsTokenRevokedByPasswordChange(c *C) {
	u := s.grpcUser(c, true)
	client := s.grpcClientFor(c, u, time.Now().Add(-10*time.Minute))
	unary, _, _ := grpcCodes(c, client)
	c.Assert(unary, Equals, codes.OK)

	c.Assert(s.api.tenancy.UpdateUserRevokingCredentials(u, time.Now()), IsNil)

	unary, stream, _ := grpcCodes(c, client)
	c.Assert(unary, Equals, codes.Unauthenticated)
	c.Assert(stream, Equals, codes.Unauthenticated)
}

// The suite's own token names no user id, so expandToken leaves its signed
// scopes alone, as on HTTP.
func (s *GRPCSuite) TestGRPCTokenWithoutUserRowKeepsScopes(c *C) {
	unary, stream, _ := grpcCodes(c, s.grpc)
	c.Assert(unary, Equals, codes.OK)
	c.Assert(stream, Equals, codes.OK)
}
