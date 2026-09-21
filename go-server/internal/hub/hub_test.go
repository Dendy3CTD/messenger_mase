package hub

import (
	"sync"
	"testing"
	"time"
)

// Hub tests use clients without a connection: the hub only queues into c.send.

func mustNotPanic(t *testing.T, name string, f func()) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("%s: паника: %v", name, r)
		}
	}()
	f()
}

// R-15: sending to a client that has been unregistered must not panic.
func TestSendToUnregisteredClientDoesNotPanic(t *testing.T) {
	h := New()
	c := newClient(nil)
	h.Register(c)
	h.Authenticate(c, 1, "t")
	h.Unregister(c)

	mustNotPanic(t, "Send", func() {
		if h.Send(1, []byte("x")) {
			t.Error("Send отчитался об успехе для отключённого пользователя")
		}
	})
	mustNotPanic(t, "SendTo", func() { h.SendTo(c, []byte("x")) })
	mustNotPanic(t, "BroadcastAuthenticated", func() { h.BroadcastAuthenticated([]byte("x"), -1) })
}

// R-15: a socket that authenticates twice as different users must leave nothing behind.
func TestReauthenticationLeavesNoStaleEntry(t *testing.T) {
	h := New()
	c := newClient(nil)
	h.Register(c)
	h.Authenticate(c, 1, "t1")
	h.Authenticate(c, 2, "t2")
	if h.IsOnline(1) {
		t.Error("пользователь 1 остался в хабе после повторного входа на том же соединении")
	}
	h.Unregister(c)
	if h.IsOnline(1) || h.IsOnline(2) {
		t.Errorf("после закрытия соединения онлайн: 1=%v 2=%v", h.IsOnline(1), h.IsOnline(2))
	}
	mustNotPanic(t, "Authenticate после закрытия", func() {
		c2 := newClient(nil)
		h.Register(c2)
		h.Authenticate(c2, 1, "t3")
	})
}

// R-15: a panic under the hub lock must not leave it locked; whatever happens, the hub keeps
// answering. The stress mix below panics on the unfixed hub and, once a lock is stuck, times out.
func TestConcurrentChurnKeepsTheHubAlive(t *testing.T) {
	h := New()
	done := make(chan struct{})
	go func() {
		defer close(done)
		var wg sync.WaitGroup
		for w := 0; w < 8; w++ {
			wg.Add(1)
			go func(w int) {
				defer wg.Done()
				defer func() { recover() }() // the unfixed hub panics here; the timeout below is what fails the test
				for i := 0; i < 300; i++ {
					c := newClient(nil)
					h.Register(c)
					h.Authenticate(c, int64(w%3+1), "t")
					h.BroadcastAuthenticated([]byte("x"), -1)
					h.Send(int64((w+1)%3+1), []byte("y"))
					h.Unregister(c)
				}
			}(w)
		}
		wg.Wait()
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("хаб завис (взаимоблокировка)")
	}
	mustNotPanic(t, "OnlineUserIDs после нагрузки", func() { h.OnlineUserIDs() })
}
