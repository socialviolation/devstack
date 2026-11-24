package stack

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/socialviolation/devstack/internal/workspace"
)

func TestStoreReadsRecordsWithoutOwnerOrExpiry(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := workspace.DataDir("navexa")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	old := `[{"name":"perf","base":"navexa","root":"/r","branch":"perf","overlay":["api"],"worktrees":{},"ports":{},"created_at":"2026-01-02T03:04:05Z"}]`
	if err := os.WriteFile(dir+"stacks.json", []byte(old), 0644); err != nil {
		t.Fatal(err)
	}
	rec, err := FindStack("navexa", "perf")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Owner != "" || !rec.ExpiresAt.IsZero() {
		t.Errorf("old record parsed with owner %q and expiry %v, want neither", rec.Owner, rec.ExpiresAt)
	}

	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "owner") || strings.Contains(string(data), "expires_at") {
		t.Errorf("a record with no owner or expiry writes them anyway: %s", data)
	}
}

func TestExpired(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	for _, r := range []Record{
		{Name: "agent-old", Base: "navexa", Owner: "alice", ExpiresAt: now.Add(-time.Minute)},
		{Name: "agent-due", Base: "navexa", Owner: "alice", ExpiresAt: now},
		{Name: "agent-new", Base: "navexa", Owner: "alice", ExpiresAt: now.Add(time.Minute)},
		{Name: "perf", Base: "navexa", CreatedAt: now.Add(-1000 * time.Hour)},
	} {
		if err := upsertStack(r); err != nil {
			t.Fatal(err)
		}
	}

	got, err := Expired("navexa", now)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, r := range got {
		names = append(names, r.Name)
	}
	if strings.Join(names, ",") != "agent-old,agent-due" {
		t.Errorf("Expired = %v, want [agent-old agent-due]", names)
	}
}

func TestExtendCapsTheLifetime(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	created := time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)
	now := created.Add(10 * time.Hour)
	if err := upsertStack(Record{Name: "agent-x", Base: "navexa", Owner: "alice", CreatedAt: created, ExpiresAt: created.Add(24 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := upsertStack(Record{Name: "perf", Base: "navexa", CreatedAt: created}); err != nil {
		t.Fatal(err)
	}

	got, err := Extend("navexa", "agent-x", 12*time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	if want := created.Add(36 * time.Hour); !got.Equal(want) {
		t.Errorf("extend by 12h = %v, want %v", got, want)
	}

	got, err = Extend("navexa", "agent-x", 100*time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	if want := created.Add(MaxLifetime); !got.Equal(want) {
		t.Errorf("extend past the cap = %v, want %v", got, want)
	}
	rec, _ := FindStack("navexa", "agent-x")
	if !rec.ExpiresAt.Equal(created.Add(MaxLifetime)) {
		t.Errorf("stored expiry = %v, want the cap", rec.ExpiresAt)
	}

	if _, err := Extend("navexa", "perf", time.Hour, now); err == nil || !strings.Contains(err.Error(), "no expiry") {
		t.Errorf("extend a stack with no expiry: err = %v, want a refusal", err)
	}
}

func TestCreateRecordsOwnerAndExpiry(t *testing.T) {
	base := newBase(t)
	noDaemon(t)

	before := time.Now()
	if _, err := Create(CreateInput{Base: base, Name: "agent-feat", Repos: []string{"backend"}, Owner: "alice", TTL: 24 * time.Hour}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	rec, err := FindStack(base.Name, "agent-feat")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Owner != "alice" {
		t.Errorf("owner = %q, want alice", rec.Owner)
	}
	if !rec.ExpiresAt.Equal(rec.CreatedAt.Add(24*time.Hour)) || rec.CreatedAt.Before(before) {
		t.Errorf("created %v, expires %v, want expiry 24h after creation", rec.CreatedAt, rec.ExpiresAt)
	}
}
