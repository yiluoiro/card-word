package cryptoutil

import (
	"errors"
	"testing"
)

func TestRejectUnsafePasswordHashParameters(t *testing.T) {
	for _, params := range []string{"m=65536,t=0,p=2", "m=65536,t=3,p=0", "m=4294967295,t=3,p=2", "m=65536,t=4294967295,p=2"} {
		hash := "$argon2id$v=19$" + params + "$MTIzNDU2Nzg5MDEyMzQ1Ng$MTIzNDU2Nzg5MDEyMzQ1Njc4OTAxMjM0NTY3ODkwMTI"
		if ok, err := VerifyPassword(hash, "test-password"); ok || !errors.Is(err, ErrInvalidHash) {
			t.Fatalf("unsafe hash accepted: %s %v", params, err)
		}
	}
	hash, err := HashPassword("valid-password-123")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := VerifyPassword(hash, "valid-password-123"); err != nil || !ok {
		t.Fatal("valid password rejected")
	}
}
