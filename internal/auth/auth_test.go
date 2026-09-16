package auth

import (
	"testing"
	"time"
)

func TestSessionRoundTripAndExpiry(t *testing.T) {
	secret := []byte("a sufficiently long test secret")
	now := time.Unix(1000, 0)
	value := SignSession(secret, "admin", now.Add(time.Hour))
	user, ok := VerifySession(secret, value, now)
	if !ok || user != "admin" {
		t.Fatalf("got user=%q ok=%v", user, ok)
	}
	if _, ok := VerifySession(secret, value, now.Add(time.Hour)); ok {
		t.Fatal("expired session accepted")
	}
	if _, ok := VerifySession([]byte("wrong"), value, now); ok {
		t.Fatal("session with wrong signature accepted")
	}
}
