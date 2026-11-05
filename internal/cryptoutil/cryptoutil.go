// Package cryptoutil provides the cryptographic primitives used across
// card word: password hashing, token generation, authenticated encryption
// and message authentication.
package cryptoutil

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters. Tuned for ~100ms on a modern laptop core.
const (
	argonTime    = 3
	argonMemory  = 64 * 1024 // 64 MiB
	argonThreads = 2
	argonKeyLen  = 32
	argonSaltLen = 16
)

var (
	// ErrInvalidHash indicates a stored hash could not be parsed.
	ErrInvalidHash = errors.New("cryptoutil: invalid password hash")
	// ErrIncompatibleVersion indicates an unsupported argon2 version.
	ErrIncompatibleVersion = errors.New("cryptoutil: incompatible argon2 version")
)

// RandomBytes returns n cryptographically secure random bytes.
func RandomBytes(n int) ([]byte, error) {
	if n <= 0 {
		return nil, errors.New("cryptoutil: invalid random length")
	}
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("cryptoutil: read random: %w", err)
	}
	return b, nil
}

// RandomToken returns an URL-safe random token of n bytes of entropy.
func RandomToken(n int) (string, error) {
	b, err := RandomBytes(n)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// RandomDigits returns a cryptographically secure decimal string of length n.
// Base64URL encodes bytes as unpadded base64url.
func Base64URL(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func RandomDigits(n int) (string, error) {
	var sb strings.Builder
	for i := 0; i < n; i++ {
		b, err := RandomBytes(1)
		if err != nil {
			return "", err
		}
		sb.WriteByte('0' + b[0]%10)
	}
	return sb.String(), nil
}

// TokenHash returns the SHA-256 digest of a token, hex encoded.
// Tokens are always stored hashed so a database leak does not grant access.
func TokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return fmt.Sprintf("%x", sum[:])
}

// HashPassword derives an argon2id hash usable for storage.
func HashPassword(password string) (string, error) {
	if len(password) < 8 {
		return "", errors.New("cryptoutil: password too short")
	}
	if len(password) > 1024 {
		return "", errors.New("cryptoutil: password too long")
	}
	salt, err := RandomBytes(argonSaltLen)
	if err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// VerifyPassword compares a plaintext password with a stored argon2id hash
// in constant time.
func VerifyPassword(encoded, password string) (bool, error) {
	if len(encoded) > 1024 || len(password) > 1024 {
		return false, ErrInvalidHash
	}
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, ErrInvalidHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return false, ErrInvalidHash
	}
	if version != argon2.Version {
		return false, ErrIncompatibleVersion
	}
	var memory uint32
	var timeCost uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &timeCost, &threads); err != nil {
		return false, ErrInvalidHash
	}
	// Corrupt/imported hashes must not panic or allocate unbounded memory.
	if memory < 8 || memory > 256*1024 || timeCost < 1 || timeCost > 10 || threads < 1 || threads > 16 {
		return false, ErrInvalidHash
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, ErrInvalidHash
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, ErrInvalidHash
	}
	if len(salt) < 8 || len(salt) > 64 || len(want) < 16 || len(want) > 64 {
		return false, ErrInvalidHash
	}
	got := argon2.IDKey([]byte(password), salt, timeCost, memory, threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// NewAEAD builds an AES-256-GCM AEAD from a 32 byte key.
func NewAEAD(key []byte) (cipher.AEAD, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("cryptoutil: aes-256 requires a 32 byte key, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("cryptoutil: new cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("cryptoutil: new gcm: %w", err)
	}
	return aead, nil
}

// Seal encrypts plaintext with AES-256-GCM and a random nonce, returning a
// base64url string. Additional authenticated data binds the ciphertext to a
// context string (for example a card key id) to prevent cross-record swaps.
func Seal(key []byte, plaintext []byte, aad string) (string, error) {
	aead, err := NewAEAD(key)
	if err != nil {
		return "", err
	}
	nonce, err := RandomBytes(aead.NonceSize())
	if err != nil {
		return "", err
	}
	out := aead.Seal(nonce, nonce, plaintext, []byte(aad))
	return base64.RawURLEncoding.EncodeToString(out), nil
}

// Open decrypts a value produced by Seal.
func Open(key []byte, encoded, aad string) ([]byte, error) {
	aead, err := NewAEAD(key)
	if err != nil {
		return nil, err
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("cryptoutil: decode sealed value: %w", err)
	}
	if len(raw) < aead.NonceSize() {
		return nil, errors.New("cryptoutil: sealed value too short")
	}
	nonce, body := raw[:aead.NonceSize()], raw[aead.NonceSize():]
	plain, err := aead.Open(nil, nonce, body, []byte(aad))
	if err != nil {
		return nil, fmt.Errorf("cryptoutil: open sealed value: %w", err)
	}
	return plain, nil
}

// HMACSHA256Hex returns a hex encoded HMAC-SHA256 signature.
func HMACSHA256Hex(key []byte, message string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(message))
	return fmt.Sprintf("%x", mac.Sum(nil))
}

// VerifyHMACSHA256Hex checks a hex signature in constant time.
func VerifyHMACSHA256Hex(key []byte, message, signature string) bool {
	want := HMACSHA256Hex(key, message)
	return hmac.Equal([]byte(want), []byte(strings.ToLower(strings.TrimSpace(signature))))
}

// SHA256Hex returns the hex encoded SHA-256 digest of data.
func SHA256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum[:])
}

// ParseKey accepts a 32 byte key encoded as base64 (raw or standard) or hex
// and returns the raw bytes.
func ParseKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, errors.New("cryptoutil: empty key")
	}
	if len(s) == 64 {
		if b, err := hexDecode(s); err == nil {
			return b, nil
		}
	}
	for _, enc := range []*base64.Encoding{base64.RawURLEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.StdEncoding} {
		if b, err := enc.DecodeString(s); err == nil && len(b) == 32 {
			return b, nil
		}
	}
	return nil, errors.New("cryptoutil: key must be 32 bytes encoded as base64 or hex")
}

func hexDecode(s string) ([]byte, error) {
	out := make([]byte, len(s)/2)
	for i := 0; i < len(out); i++ {
		var v int
		if _, err := fmt.Sscanf(s[i*2:i*2+2], "%02x", &v); err != nil {
			return nil, err
		}
		out[i] = byte(v)
	}
	return out, nil
}

// MaskSecret renders a secret for display, keeping only the first and last
// few characters.
func MaskSecret(s string) string {
	runes := []rune(s)
	switch {
	case len(runes) == 0:
		return ""
	case len(runes) <= 8:
		return strings.Repeat("*", len(runes))
	case len(runes) <= 16:
		return string(runes[:2]) + strings.Repeat("*", len(runes)-4) + string(runes[len(runes)-2:])
	default:
		return string(runes[:4]) + strings.Repeat("*", 8) + string(runes[len(runes)-4:])
	}
}

// IntToBytes is a small helper used when deriving deterministic nonces.
func IntToBytes(v uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, v)
	return b
}

// NumCPU exposes runtime.NumCPU for pool sizing decisions.
func NumCPU() int { return runtime.NumCPU() }

// Itoa is a tiny wrapper used by callers that build human readable codes.
func Itoa(v int) string { return strconv.Itoa(v) }
