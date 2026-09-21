package server_test

import (
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/mase/server/internal/testutil"
)

func TestHealth(t *testing.T) {
	s := testutil.NewServer(t)
	resp, err := http.Get(s.HTTP.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(body) != "ok" {
		t.Fatalf("/health: %d %q", resp.StatusCode, body)
	}
}

func TestSchemaIsBuiltByMigrations(t *testing.T) {
	s := testutil.NewServer(t)
	if got := s.Migrations(t); len(got) != 1 || got[0] != 1 {
		t.Fatalf("применённые миграции: %v", got)
	}
}

func TestRegisterLoginAndPing(t *testing.T) {
	s := testutil.NewServer(t)

	a := s.Dial(t)
	a.Register("+70000000001", "secret1", "Alice", "alice")
	a.Send(map[string]any{"type": "ping"})
	a.Recv("pong")

	// вход тем же паролем с другого соединения
	b := s.Dial(t)
	b.Login("+70000000001", "secret1")
	if b.ID != a.ID {
		t.Fatalf("тот же пользователь получил другой id: %d и %d", a.ID, b.ID)
	}

	// неверный пароль
	c := s.Dial(t)
	c.Send(map[string]any{"type": "auth.login", "phone": "+70000000001", "password": "wrong"})
	if m := c.Recv("err"); m["code"] != "bad_credentials" {
		t.Fatalf("неверный пароль: %v", m)
	}
}

func TestUnauthenticatedPingIsRejected(t *testing.T) {
	s := testutil.NewServer(t)
	c := s.Dial(t)
	c.Send(map[string]any{"type": "ping"})
	if m := c.Recv("err"); m["code"] != "auth_required" {
		t.Fatalf("ping без входа: %v", m)
	}
}

// Each NewServer starts from an empty database: state must not leak between tests.
func TestServersAreIsolated(t *testing.T) {
	t.Run("first registers", func(t *testing.T) {
		s := testutil.NewServer(t)
		s.Dial(t).Register("+70000000001", "secret1", "Alice", "alice")
	})
	t.Run("second does not see the user", func(t *testing.T) {
		s := testutil.NewServer(t)
		c := s.Dial(t)
		c.Send(map[string]any{"type": "auth.login", "phone": "+70000000001", "password": "secret1"})
		if m := c.Recv("err"); m["code"] != "bad_credentials" {
			t.Fatalf("пользователь из прошлого теста виден: %v", m)
		}
		if _, ok := c.TryRecv("auth.session", 100*time.Millisecond); ok {
			t.Fatal("вход не должен был пройти")
		}
	})
}
