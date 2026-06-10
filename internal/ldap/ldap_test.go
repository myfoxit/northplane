package ldap

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	goldap "github.com/go-ldap/ldap/v3"

	"github.com/northplane/northplane/internal/config"
	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/storage"
)

// fakeConn is an in-memory directory: paged searches return entries,
// binds verify against creds, filter-keyed searches serve VerifyLogin
// and group lookups.
type fakeConn struct {
	entries        []*goldap.Entry
	creds          map[string]string
	searchByFilter map[string][]*goldap.Entry
	closed         bool
}

func (f *fakeConn) Bind(dn, pw string) error {
	if pw == "" {
		return fmt.Errorf("ldap: empty password not allowed")
	}
	if want, ok := f.creds[dn]; ok && want == pw {
		return nil
	}
	return fmt.Errorf("LDAP Result Code 49: Invalid Credentials")
}

func (f *fakeConn) Search(req *goldap.SearchRequest) (*goldap.SearchResult, error) {
	if entries, ok := f.searchByFilter[req.Filter]; ok {
		return &goldap.SearchResult{Entries: entries}, nil
	}
	return &goldap.SearchResult{}, nil
}

func (f *fakeConn) SearchWithPaging(req *goldap.SearchRequest, _ uint32) (*goldap.SearchResult, error) {
	return &goldap.SearchResult{Entries: f.entries}, nil
}

func (f *fakeConn) Close() error { f.closed = true; return nil }

func entry(dn string, attrs map[string][]string) *goldap.Entry {
	return goldap.NewEntry(dn, attrs)
}

func testSyncer(t *testing.T, conn *fakeConn) (*Syncer, *storage.Store, context.Context) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	store, err := storage.Open(ctx, storage.Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); store.Close() })

	cfg := config.Defaults().LDAP
	cfg.URL = "ldaps://fake.test:636"
	cfg.BindDN = "cn=svc,dc=example,dc=net"
	cfg.BindPassword = "svcpw"
	cfg.BaseDN = "dc=example,dc=net"
	cfg.AdminGroup = "cn=northplane-admins,ou=groups,dc=example,dc=net"
	s := New(cfg, store, slog.Default())
	s.Dial = func(context.Context, config.LDAPConfig) (Conn, error) { return conn, nil }

	// role with an IdP group binding (shared mechanism with OIDC)
	if conn.creds == nil {
		conn.creds = map[string]string{}
	}
	conn.creds["cn=svc,dc=example,dc=net"] = "svcpw"
	_, err = store.PutResource(ctx, model.DefaultTenant, storage.KindRole, "ops", map[string]any{
		"name": "ops", "permissions": []string{"objects:read", "alerts:ack"},
		"idpGroups": []string{"cn=ops,ou=groups,dc=example,dc=net"},
	}, -1)
	if err != nil {
		t.Fatal(err)
	}
	return s, store, ctx
}

func directoryEntries() []*goldap.Entry {
	return []*goldap.Entry{
		entry("cn=Jane Doe,ou=people,dc=example,dc=net", map[string][]string{
			"mail": {"jane@example.net"}, "cn": {"Jane Doe"},
			"memberOf": {"cn=ops,ou=groups,dc=example,dc=net"},
		}),
		entry("cn=Adam Admin,ou=people,dc=example,dc=net", map[string][]string{
			"mail": {"adam@example.net"}, "cn": {"Adam Admin"},
			"memberOf": {"CN=Northplane-Admins,OU=groups,DC=example,DC=net"},
		}),
		entry("cn=Norm Nobody,ou=people,dc=example,dc=net", map[string][]string{
			"mail": {"norm@example.net"}, "cn": {"Norm Nobody"},
		}),
	}
}

func userBySubjectPrefix(t *testing.T, store *storage.Store, ctx context.Context, frag string) *model.User {
	t.Helper()
	users, err := store.ListUsers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range users {
		if strings.Contains(u.Subject, frag) {
			return u
		}
	}
	t.Fatalf("no user with subject containing %q", frag)
	return nil
}

func TestSyncCreatesAndMapsRoles(t *testing.T) {
	conn := &fakeConn{entries: directoryEntries()}
	s, store, ctx := testSyncer(t, conn)

	res, err := s.SyncNow(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Created != 3 || res.Disabled != 0 || res.Skipped != 0 {
		t.Fatalf("first sync: %+v", res)
	}

	jane := userBySubjectPrefix(t, store, ctx, "jane doe")
	if jane.Email != "jane@example.net" || jane.Local ||
		!strings.HasPrefix(jane.Subject, SubjectPrefix) {
		t.Fatalf("jane row: %+v", jane)
	}
	if len(jane.Roles) != 1 || jane.Roles[0] != "ops" {
		t.Fatalf("jane roles (IdP group → role): %v", jane.Roles)
	}
	adam := userBySubjectPrefix(t, store, ctx, "adam admin")
	if len(adam.Roles) != 1 || adam.Roles[0] != "admin" {
		t.Fatalf("adam roles (adminGroup bootstrap, case-insensitive): %v", adam.Roles)
	}
	norm := userBySubjectPrefix(t, store, ctx, "norm nobody")
	if len(norm.Roles) != 1 || norm.Roles[0] != "viewer" {
		t.Fatalf("norm roles (defaultRoles fallback): %v", norm.Roles)
	}

	// second pass: everything unchanged
	res, err = s.SyncNow(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Created != 0 || res.Updated != 0 || res.Unchanged != 3 {
		t.Fatalf("idempotent sync: %+v", res)
	}
	if !conn.closed {
		t.Fatal("connection not closed")
	}
}

func TestSyncUpdatesAndDisablesMissing(t *testing.T) {
	conn := &fakeConn{entries: directoryEntries()}
	s, store, ctx := testSyncer(t, conn)
	if _, err := s.SyncNow(ctx); err != nil {
		t.Fatal(err)
	}

	// directory changes: jane renamed + group changed, norm vanishes
	conn.entries = []*goldap.Entry{
		entry("cn=Jane Doe,ou=people,dc=example,dc=net", map[string][]string{
			"mail": {"jane@example.net"}, "cn": {"Jane Married"},
			"memberOf": {"cn=northplane-admins,ou=groups,dc=example,dc=net"},
		}),
		entry("cn=Adam Admin,ou=people,dc=example,dc=net", map[string][]string{
			"mail": {"adam@example.net"}, "cn": {"Adam Admin"},
			"memberOf": {"CN=Northplane-Admins,OU=groups,DC=example,DC=net"},
		}),
	}
	res, err := s.SyncNow(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Updated != 1 || res.Unchanged != 1 || res.Disabled != 1 {
		t.Fatalf("delta sync: %+v", res)
	}
	jane := userBySubjectPrefix(t, store, ctx, "jane doe")
	if jane.Name != "Jane Married" || len(jane.Roles) != 1 || jane.Roles[0] != "admin" {
		t.Fatalf("jane after update: %+v", jane)
	}
	norm := userBySubjectPrefix(t, store, ctx, "norm nobody")
	if !norm.Disabled {
		t.Fatalf("norm must be disabled after vanishing: %+v", norm)
	}

	// norm reappears → NOT resurrected (local disable is authoritative)
	conn.entries = append(conn.entries, directoryEntries()[2])
	res, err = s.SyncNow(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Skipped != 1 {
		t.Fatalf("disabled account must be skipped, got %+v", res)
	}
	if norm = userBySubjectPrefix(t, store, ctx, "norm nobody"); !norm.Disabled {
		t.Fatal("norm resurrected — disabled flag must stay authoritative")
	}
}

func TestSyncEmailConflictSkips(t *testing.T) {
	conn := &fakeConn{entries: directoryEntries()[:1]}
	s, store, ctx := testSyncer(t, conn)
	// a local break-glass admin already owns jane@example.net
	if _, err := store.CreateLocalUser(ctx, "Local Jane", "jane@example.net", "x"); err != nil {
		t.Fatal(err)
	}
	res, err := s.SyncNow(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Created != 0 || res.Skipped != 1 || len(res.Warnings) != 1 {
		t.Fatalf("conflict sync: %+v", res)
	}
	local, err := store.GetUserByEmail(ctx, "jane@example.net")
	if err != nil || !local.Local {
		t.Fatalf("local account must win the e-mail: %+v err=%v", local, err)
	}
}

func TestVerifyLogin(t *testing.T) {
	conn := &fakeConn{
		creds: map[string]string{"cn=Jane Doe,ou=people,dc=example,dc=net": "secret123"},
		searchByFilter: map[string][]*goldap.Entry{
			"(&(&(objectClass=person)(mail=*))(mail=jane@example.net))": directoryEntries()[:1],
		},
	}
	s, _, _ := testSyncer(t, conn)

	if err := s.VerifyLogin(context.Background(), "jane@example.net", "secret123"); err != nil {
		t.Fatalf("valid login: %v", err)
	}
	if err := s.VerifyLogin(context.Background(), "jane@example.net", "wrong"); err == nil {
		t.Fatal("wrong password must fail")
	}
	if err := s.VerifyLogin(context.Background(), "ghost@example.net", "x"); err == nil {
		t.Fatal("unknown user must fail")
	}
	// empty password must fail before any directory traffic (an
	// unauthenticated simple bind would otherwise "succeed")
	s.Dial = func(context.Context, config.LDAPConfig) (Conn, error) {
		t.Fatal("dial must not happen for empty passwords")
		return nil, nil
	}
	if err := s.VerifyLogin(context.Background(), "jane@example.net", ""); err == nil {
		t.Fatal("empty password must fail")
	}
}

func TestStatusReflectsRuns(t *testing.T) {
	conn := &fakeConn{entries: directoryEntries()}
	s, _, ctx := testSyncer(t, conn)
	st := s.Status()
	if !st.Configured || st.LastSyncAt != nil {
		t.Fatalf("pre-sync status: %+v", st)
	}
	if _, err := s.SyncNow(ctx); err != nil {
		t.Fatal(err)
	}
	st = s.Status()
	if st.LastSyncAt == nil || st.LastError != "" || st.LastResult == nil ||
		st.LastResult.Created != 3 {
		t.Fatalf("post-sync status: %+v", st)
	}
	// unconfigured constructor returns nil
	if New(config.LDAPConfig{}, nil, nil) != nil {
		t.Fatal("unconfigured LDAP must yield a nil syncer")
	}
}
