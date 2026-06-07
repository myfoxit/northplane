package storage

// User-management storage: CRUD, email-uniqueness conflict, the disabled
// guard on OIDC JIT-provisioning, and the last-admin counter (SPEC §11.2).
// Runs on the same SQLite-always / PostgreSQL-when-configured matrix.

import (
	"context"
	"testing"

	"github.com/northplane/northplane/internal/model"
)

func TestUserCRUDAndRoles(t *testing.T) {
	matrix(t, func(t *testing.T, s *Store) {
		ctx := context.Background()

		u, err := s.CreateUser(ctx, &model.User{
			Name: "Ada", Email: "ada@example.net", Local: true,
			PassHash: "salt$hash", Roles: []string{"operator"},
		})
		if err != nil || u.ID == "" || u.Version != 1 {
			t.Fatalf("create: %v %+v", err, u)
		}

		got, err := s.GetUser(ctx, u.ID)
		if err != nil || got.Name != "Ada" || len(got.Roles) != 1 || got.Roles[0] != "operator" {
			t.Fatalf("get roundtrip: %v %+v", err, got)
		}
		if !got.Local || got.PassHash != "salt$hash" {
			t.Fatalf("local/hash not persisted: %+v", got)
		}

		// duplicate email → ErrDuplicate (the API turns this into a 409)
		if _, err := s.CreateUser(ctx, &model.User{Name: "Eve", Email: "ada@example.net"}); err != ErrDuplicate {
			t.Fatalf("dup email want ErrDuplicate, got %v", err)
		}

		// update name/email/roles/disabled, version bumps
		got.Name, got.Email = "Ada L.", "ada.l@example.net"
		got.Roles = []string{"viewer", "operator"}
		if err := s.UpdateUser(ctx, got); err != nil {
			t.Fatalf("update: %v", err)
		}
		got2, _ := s.GetUser(ctx, u.ID)
		if got2.Name != "Ada L." || got2.Email != "ada.l@example.net" ||
			len(got2.Roles) != 2 || got2.Version != 2 {
			t.Fatalf("update not applied: %+v", got2)
		}

		// updating into another user's email conflicts
		other, _ := s.CreateUser(ctx, &model.User{Name: "Bob", Email: "bob@example.net"})
		other.Email = "ada.l@example.net"
		if err := s.UpdateUser(ctx, other); err != ErrDuplicate {
			t.Fatalf("update dup email want ErrDuplicate, got %v", err)
		}

		// set / clear password
		if err := s.SetPassword(ctx, u.ID, "new$hash"); err != nil {
			t.Fatalf("set pw: %v", err)
		}
		got3, _ := s.GetUser(ctx, u.ID)
		if got3.PassHash != "new$hash" {
			t.Fatalf("set pw not applied: %q", got3.PassHash)
		}

		// delete
		if err := s.DeleteUser(ctx, u.ID); err != nil {
			t.Fatalf("delete: %v", err)
		}
		if _, err := s.GetUser(ctx, u.ID); err != ErrNotFound {
			t.Fatalf("deleted user still present: %v", err)
		}
		// deleting again → ErrNotFound
		if err := s.DeleteUser(ctx, u.ID); err != ErrNotFound {
			t.Fatalf("double delete want ErrNotFound, got %v", err)
		}
	})
}

func TestCountEnabledAdmins(t *testing.T) {
	matrix(t, func(t *testing.T, s *Store) {
		ctx := context.Background()

		if n, err := s.CountEnabledAdmins(ctx); err != nil || n != 0 {
			t.Fatalf("empty: n=%d err=%v", n, err)
		}

		// CreateLocalUser seeds the admin role
		a1, err := s.CreateLocalUser(ctx, "Admin1", "a1@example.net", "x$y")
		if err != nil {
			t.Fatal(err)
		}
		if n, _ := s.CountEnabledAdmins(ctx); n != 1 {
			t.Fatalf("one admin: n=%d", n)
		}

		// a non-local admin (SSO) must NOT count
		if _, err := s.CreateUser(ctx, &model.User{
			Name: "SSO", Email: "sso@example.net", Roles: []string{"admin"}}); err != nil {
			t.Fatal(err)
		}
		// a local non-admin must NOT count
		if _, err := s.CreateUser(ctx, &model.User{
			Name: "Op", Email: "op@example.net", Local: true, Roles: []string{"operator"}}); err != nil {
			t.Fatal(err)
		}
		if n, _ := s.CountEnabledAdmins(ctx); n != 1 {
			t.Fatalf("still one local admin: n=%d", n)
		}

		// second local admin
		a2, _ := s.CreateLocalUser(ctx, "Admin2", "a2@example.net", "x$y")
		if n, _ := s.CountEnabledAdmins(ctx); n != 2 {
			t.Fatalf("two admins: n=%d", n)
		}

		// disabling one drops the count
		a2.Disabled = true
		if err := s.UpdateUser(ctx, a2); err != nil {
			t.Fatal(err)
		}
		if n, _ := s.CountEnabledAdmins(ctx); n != 1 {
			t.Fatalf("after disable: n=%d", n)
		}
		_ = a1
	})
}

func TestUpsertUserBySubjectDisabledGuard(t *testing.T) {
	matrix(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		const subject = "https://idp.example|sub-123"

		// first login provisions the user
		u, err := s.UpsertUserBySubject(ctx, subject, "Carol", "carol@example.net")
		if err != nil {
			t.Fatalf("provision: %v", err)
		}

		// an admin disables the account
		u.Disabled = true
		if err := s.UpdateUser(ctx, u); err != nil {
			t.Fatal(err)
		}

		// a subsequent OIDC login must be refused AND must not re-enable
		if _, err := s.UpsertUserBySubject(ctx, subject, "Carol", "carol@example.net"); err != ErrDisabled {
			t.Fatalf("disabled OIDC re-login want ErrDisabled, got %v", err)
		}
		again, _ := s.GetUser(ctx, u.ID)
		if !again.Disabled {
			t.Fatal("disabled user was resurrected by OIDC JIT-provisioning")
		}
	})
}
