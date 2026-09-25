package access

import "testing"

func TestResolveUnionAndOwner(t *testing.T) {
	got := Resolve(Input{
		CallerUserID: "u1",
		AppID:        "app-1",
		OwnerAccount: "user:u1",
		AppRole:      "view",
	})
	if !got.ImplicitOwner || !Has(got.Permissions, PermAppAdmin) {
		t.Fatalf("owner: %+v", got)
	}
	if !CanTransfer(got, got) {
		t.Fatal("owner can transfer to their own account")
	}
}

func TestNegativeMatrix(t *testing.T) {
	owner := "org:org-1"
	app := "app-1"
	cases := []struct {
		name       string
		in         Input
		want       []string
		deny       []string
		transfer   bool
		transferTo Result
	}{
		{
			name: "stranger",
			in:   Input{CallerUserID: "u-stranger", AppID: app, OwnerAccount: owner},
			deny: []string{PermAppRead, PermAppDeploy, PermAppWrite, PermAppAdmin},
		},
		{
			name: "other tenant collaborator",
			in: Input{
				CallerUserID: "u2", AppID: app, OwnerAccount: owner,
				AccountRole: "admin",
				Ledger:      []LedgerRole{{Subject: "org:other", Role: "owner"}},
			},
			want: []string{PermAppAdmin},
			deny: []string{PermOrgAppsCreate},
		},
		{
			name: "suspended user",
			in: Input{
				CallerUserID: "u1", AppID: app, OwnerAccount: "user:u1", CallerSuspended: true,
			},
			deny: []string{PermAppRead, PermAppAdmin},
		},
		{
			name: "suspended owner",
			in: Input{
				CallerUserID: "u2", AppID: app, OwnerAccount: owner, OwnerSuspended: true,
				Ledger: []LedgerRole{{Subject: owner, Role: "admin"}},
			},
			deny: []string{PermAppRead, PermAppAdmin},
		},
		{
			name: "cluster admin reads suspended records",
			in: Input{
				CallerUserID: "op", AppID: app, OwnerAccount: owner,
				OwnerSuspended: true, ClusterAdmin: true,
			},
			want: []string{PermAppRead},
			deny: []string{PermAppAdmin, PermAppWrite},
		},
		{
			name: "billing cannot deploy",
			in: Input{
				CallerUserID: "bill", AppID: app, OwnerAccount: owner,
				Ledger: []LedgerRole{{Subject: owner, Role: "billing"}},
			},
			want: []string{PermOrgBilling, PermAppRead},
			deny: []string{PermAppDeploy, PermAppWrite, PermAppAdmin},
		},
		{
			name: "member cannot delete",
			in: Input{
				CallerUserID: "mem", AppID: app, OwnerAccount: owner,
				Ledger: []LedgerRole{{Subject: owner, Role: "member"}},
			},
			want: []string{PermAppRead, PermAppDeploy},
			deny: []string{PermAppAdmin, PermAppWrite, PermOrgMembersWrite},
		},
		{
			name: "collaborator cannot transfer",
			in: Input{
				CallerUserID: "c1", AppID: app, OwnerAccount: "user:owner",
				AccountRole: "admin",
			},
			want: []string{PermAppAdmin},
			deny: []string{PermOrgBilling},
		},
		{
			name: "app collaborator manage",
			in: Input{
				CallerUserID: "c1", AppID: app, OwnerAccount: "user:owner",
				AppRole: "manage",
			},
			want: []string{PermAppWrite},
			deny: []string{PermAppAdmin},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Resolve(tc.in)
			for _, p := range tc.want {
				if !Has(got.Permissions, p) {
					t.Fatalf("missing %s in %v", p, got.Permissions)
				}
			}
			for _, p := range tc.deny {
				if Has(got.Permissions, p) {
					t.Fatalf("must not have %s in %v", p, got.Permissions)
				}
			}
			target := tc.transferTo
			if tc.name == "collaborator cannot transfer" {
				target = Result{ImplicitOwner: true, Permissions: adminPerms()}
				if CanTransfer(got, target) {
					t.Fatal("collaborator must not transfer")
				}
			}
		})
	}
}

func TestOrgAdminCanTransferToPersonalOwner(t *testing.T) {
	source := Resolve(Input{
		CallerUserID: "u1",
		AppID:        "app-1",
		OwnerAccount: "org:o1",
		Ledger:       []LedgerRole{{Subject: "org:o1", Role: "admin"}},
	})
	target := Resolve(Input{
		CallerUserID: "u1",
		OwnerAccount: "user:u1",
	})
	if !source.OrgManager || !target.ImplicitOwner {
		t.Fatalf("source %+v target %+v", source, target)
	}
	if !CanTransfer(source, target) {
		t.Fatal("org admin who owns the personal target may transfer")
	}
	view := Resolve(Input{
		CallerUserID: "u1",
		OwnerAccount: "user:other",
		AccountRole:  "admin",
	})
	if CanTransfer(source, view) {
		t.Fatal("admin collaborator on the target is not transfer authority")
	}
}
