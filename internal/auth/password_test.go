package auth

import "testing"

func TestHashTokenStable(t *testing.T) {
	a := HashToken("abc")
	b := HashToken("abc")
	if a != b || len(a) != 64 {
		t.Fatalf("token hash %s", a)
	}
	if HashToken("abc") == HashToken("abd") {
		t.Fatal("different tokens must not collide")
	}
}

func TestPasswordRoundTrip(t *testing.T) {
	hash, err := HashPassword("correct-horse")
	if err != nil {
		t.Fatal(err)
	}
	if !CheckPassword(hash, "correct-horse") {
		t.Fatal("expected match")
	}
	if CheckPassword(hash, "wrong") {
		t.Fatal("expected reject")
	}
	if CheckPassword("", "correct-horse") {
		t.Fatal("empty hash must not match")
	}
}

func TestValidPassword(t *testing.T) {
	if ValidPassword("short") || !ValidPassword("long-enough") {
		t.Fatal("password rules")
	}
}
