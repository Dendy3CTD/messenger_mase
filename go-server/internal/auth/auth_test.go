package auth

import (
	"regexp"
	"testing"
	"time"
)

func TestHashPasswordKnownVector(t *testing.T) {
	// SHA-256("abc"), the standard test vector
	const want = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	if got := HashPassword("abc"); got != want {
		t.Fatalf("HashPassword(abc) = %s", got)
	}
}

func TestHashPasswordIsDeterministicAndDistinguishesInputs(t *testing.T) {
	if HashPassword("secret") != HashPassword("secret") {
		t.Fatal("хеш одного пароля должен совпадать")
	}
	if HashPassword("secret") == HashPassword("Secret") {
		t.Fatal("хеши разных паролей не должны совпадать")
	}
	if len(HashPassword("")) != 64 {
		t.Fatal("хеш пустой строки должен быть 64 hex-символа")
	}
}

func TestNewTokenFormatAndUniqueness(t *testing.T) {
	hex64 := regexp.MustCompile(`^[0-9a-f]{64}$`)
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		tok, err := NewToken()
		if err != nil {
			t.Fatal(err)
		}
		if !hex64.MatchString(tok) {
			t.Fatalf("токен %q не 64 hex-символа", tok)
		}
		if seen[tok] {
			t.Fatalf("повтор токена %q", tok)
		}
		seen[tok] = true
	}
}

func TestRandomDigits(t *testing.T) {
	digits := regexp.MustCompile(`^[0-9]+$`)
	for _, n := range []int{1, 7, 32} {
		got := RandomDigits(n)
		if len(got) != n || !digits.MatchString(got) {
			t.Fatalf("RandomDigits(%d) = %q", n, got)
		}
	}
	if RandomDigits(0) != "" {
		t.Fatal("RandomDigits(0) должен вернуть пустую строку")
	}
}

func TestClocks(t *testing.T) {
	before := time.Now()
	sec, milli := NowSec(), NowMilli()
	after := time.Now()
	if sec < before.Unix() || sec > after.Unix() {
		t.Fatalf("NowSec %d вне [%d, %d]", sec, before.Unix(), after.Unix())
	}
	if milli < before.UnixMilli() || milli > after.UnixMilli() {
		t.Fatalf("NowMilli %d вне [%d, %d]", milli, before.UnixMilli(), after.UnixMilli())
	}
}
