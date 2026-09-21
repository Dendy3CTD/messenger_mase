package server_test

import (
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/mase/server/internal/hub"
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

// S-8 / R-15 (docs/regression-f6.md): signing in as a second user on an already authenticated
// socket used to leave a closed client in the hub's user map; the next login of the first user
// then panicked while holding the hub lock and the whole server stopped answering.
func TestS8_ReauthOnSameSocketDoesNotStopTheHub(t *testing.T) {
	s := testutil.NewServer(t)

	// Bob exists (registered from a connection that is then closed)
	b := s.Dial(t)
	b.Register("+70000000002", "secret2", "Bob", "bob")
	bob := b.ID
	b.Close()
	waitOffline(t, bob)

	// X registers Alice and then signs in as Bob on the same socket
	x := s.Dial(t)
	x.Register("+70000000001", "secret1", "Alice", "alice")
	alice := x.ID
	x.Send(map[string]any{"type": "auth.login", "phone": "+70000000002", "password": "secret2"})
	x.Recv("auth.session")
	x.Close()
	waitOffline(t, bob) // the server has processed the close of X: Bob's entry is gone

	// a new connection signs in as Alice: must get an answer
	z := s.Dial(t)
	z.Send(map[string]any{"type": "auth.login", "phone": "+70000000001", "password": "secret1"})
	if _, ok := z.TryRecv("auth.session", testutil.RecvTimeout); !ok {
		t.Fatal("вход Alice не получил ответа: хаб заблокирован")
	}
	z.Close()
	waitOffline(t, alice)
	if hub.H.IsOnline(bob) {
		t.Error("Bob остался в хабе")
	}
}

func waitOffline(t *testing.T, uid int64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for hub.H.IsOnline(uid) {
		if time.Now().After(deadline) {
			t.Fatalf("пользователь %d остался в хабе после закрытия соединения", uid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// One connection per user: a newer login closes the older connection and keeps the new one working.
func TestSecondLoginReplacesTheFirstConnection(t *testing.T) {
	s := testutil.NewServer(t)
	first := s.Dial(t)
	first.Register("+70000000001", "secret1", "Alice", "alice")

	second := s.Dial(t)
	second.Login("+70000000001", "secret1")

	if !first.WaitClosed(3 * time.Second) {
		t.Fatal("первое соединение пользователя не закрыто после входа со второго")
	}
	second.Send(map[string]any{"type": "ping"})
	second.Recv("pong")
	if !hub.H.IsOnline(second.ID) {
		t.Fatal("пользователь должен остаться в хабе через второе соединение")
	}
}
