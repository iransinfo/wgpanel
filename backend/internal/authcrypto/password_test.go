package authcrypto

import "testing"

func TestHashAndVerifyPassword(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}

	ok, err := VerifyPassword("correct horse battery staple", hash)
	if err != nil {
		t.Fatalf("VerifyPassword: %v", err)
	}
	if !ok {
		t.Fatal("expected correct password to verify")
	}

	ok, err = VerifyPassword("wrong password", hash)
	if err != nil {
		t.Fatalf("VerifyPassword: %v", err)
	}
	if ok {
		t.Fatal("expected wrong password to fail verification")
	}
}

func TestVerifyPasswordAgainstDummyHash(t *testing.T) {
	// auth.go leans on being able to verify against dummyHash without erroring, to
	// keep unknown-username logins taking roughly the same code path as known ones.
	ok, err := VerifyPassword("anything", dummyHashForTest)
	if err != nil {
		t.Fatalf("VerifyPassword against dummy hash: %v", err)
	}
	if ok {
		t.Fatal("dummy hash should never verify successfully")
	}
}

const dummyHashForTest = "argon2id$m=65536,t=1,p=4$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
