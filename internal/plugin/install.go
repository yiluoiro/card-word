package plugin

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/yiluoiro/card-word/internal/cryptoutil"
)

// Limits enforced on uploaded packages.
const (
	MaxPackageBytes = 8 << 20  // 8 MiB compressed
	MaxUnpackedSize = 32 << 20 // 32 MiB uncompressed
	MaxFiles        = 400
	MaxFileBytes    = 8 << 20
)

// ErrSignatureRequired is returned when policy demands a signature but none
// was supplied.
var ErrSignatureRequired = errors.New("plugin: a valid signature is required")

// Installer verifies and unpacks plugin packages into a directory.
type Installer struct {
	Dir           string
	RequireSigned bool
	PublicKeys    []ed25519.PublicKey
	mu            sync.Mutex
}

// NewInstaller builds an installer rooted at dir.
func NewInstaller(dir string, requireSigned bool, keys []ed25519.PublicKey) *Installer {
	return &Installer{Dir: dir, RequireSigned: requireSigned, PublicKeys: keys}
}

// ParsePublicKeys decodes base64 Ed25519 public keys.
func ParsePublicKeys(values []string) ([]ed25519.PublicKey, error) {
	out := make([]ed25519.PublicKey, 0, len(values))
	for _, v := range values {
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(v))
		if err != nil {
			raw, err = base64.RawURLEncoding.DecodeString(strings.TrimSpace(v))
			if err != nil {
				return nil, fmt.Errorf("plugin: invalid public key: %w", err)
			}
		}
		if len(raw) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("plugin: public key must be %d bytes", ed25519.PublicKeySize)
		}
		out = append(out, ed25519.PublicKey(raw))
	}
	return out, nil
}

// VerifySignature checks an Ed25519 signature (base64) over the package bytes
// against any configured key.
func (i *Installer) VerifySignature(pkg []byte, signatureB64 string) (bool, error) {
	if strings.TrimSpace(signatureB64) == "" {
		return false, ErrSignatureRequired
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(signatureB64))
	if err != nil {
		sig, err = base64.RawURLEncoding.DecodeString(strings.TrimSpace(signatureB64))
		if err != nil {
			return false, fmt.Errorf("plugin: invalid signature encoding: %w", err)
		}
	}
	if len(i.PublicKeys) == 0 {
		return false, errors.New("plugin: no trusted public keys configured")
	}
	for _, key := range i.PublicKeys {
		if ed25519.Verify(key, pkg, sig) {
			return true, nil
		}
	}
	return false, errors.New("plugin: signature does not match any trusted key")
}

// Installed describes a successfully unpacked package.
type Installed struct {
	Manifest  *Manifest
	Dir       string
	SHA256    string
	Signature string
	Signed    bool
	Files     int
}

// Install verifies a package and writes it to disk atomically.
func (i *Installer) Install(ctx context.Context, pkg []byte, signatureB64, source string) (*Installed, error) {
	if len(pkg) == 0 {
		return nil, errors.New("plugin: empty package")
	}
	if len(pkg) > MaxPackageBytes {
		return nil, fmt.Errorf("plugin: package exceeds %d MiB", MaxPackageBytes>>20)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	digest := cryptoutil.SHA256Hex(pkg)
	signed := false
	if strings.TrimSpace(signatureB64) != "" || i.RequireSigned {
		ok, err := i.VerifySignature(pkg, signatureB64)
		if err != nil {
			if i.RequireSigned {
				return nil, err
			}
		} else {
			signed = ok
		}
	}

	zr, err := zip.NewReader(bytes.NewReader(pkg), int64(len(pkg)))
	if err != nil {
		return nil, fmt.Errorf("plugin: invalid zip: %w", err)
	}
	if len(zr.File) > MaxFiles {
		return nil, fmt.Errorf("plugin: package has too many files (%d)", len(zr.File))
	}

	var manifestData []byte
	total := int64(0)
	names := make([]string, 0, len(zr.File))
	for _, f := range zr.File {
		clean, err := safeRelPath(f.Name)
		if err != nil {
			return nil, err
		}
		if f.FileInfo().Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("plugin: symbolic links are not allowed (%s)", f.Name)
		}
		if f.FileInfo().IsDir() {
			continue
		}
		if f.UncompressedSize64 > MaxFileBytes {
			return nil, fmt.Errorf("plugin: file %s is too large", f.Name)
		}
		total += int64(f.UncompressedSize64)
		if total > MaxUnpackedSize {
			return nil, fmt.Errorf("plugin: unpacked package exceeds %d MiB", MaxUnpackedSize>>20)
		}
		names = append(names, clean)
		if clean == "manifest.json" {
			rc, err := f.Open()
			if err != nil {
				return nil, fmt.Errorf("plugin: read manifest: %w", err)
			}
			manifestData, err = io.ReadAll(io.LimitReader(rc, 256*1024))
			rc.Close()
			if err != nil {
				return nil, fmt.Errorf("plugin: read manifest: %w", err)
			}
		}
	}
	if manifestData == nil {
		return nil, errors.New("plugin: manifest.json missing at package root")
	}
	manifest, err := ParseManifest(manifestData)
	if err != nil {
		return nil, err
	}
	if !signed {
		if i.RequireSigned {
			return nil, ErrSignatureRequired
		}
		if manifest.Runtime.Type == "external" && !manifest.HasPermission(PermHTTPOutbound) {
			return nil, errors.New("plugin: external runtimes must declare the http.outbound permission")
		}
	}

	i.mu.Lock()
	defer i.mu.Unlock()

	if err := os.MkdirAll(i.Dir, 0o750); err != nil {
		return nil, fmt.Errorf("plugin: create plugin dir: %w", err)
	}
	tmpDir, err := os.MkdirTemp(i.Dir, ".staging-"+manifest.UID+"-")
	if err != nil {
		return nil, fmt.Errorf("plugin: create staging dir: %w", err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(tmpDir)
		}
	}()

	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		clean, err := safeRelPath(f.Name)
		if err != nil {
			return nil, err
		}
		target := filepath.Join(tmpDir, filepath.FromSlash(clean))
		if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
			return nil, fmt.Errorf("plugin: create dir: %w", err)
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("plugin: open entry: %w", err)
		}
		data, err := io.ReadAll(io.LimitReader(rc, MaxFileBytes+1))
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("plugin: read entry: %w", err)
		}
		if len(data) > MaxFileBytes {
			return nil, fmt.Errorf("plugin: file %s is too large", clean)
		}
		mode := os.FileMode(0o640)
		if strings.HasSuffix(clean, ".sh") {
			mode = 0o750
		}
		if err := os.WriteFile(target, data, mode); err != nil {
			return nil, fmt.Errorf("plugin: write entry: %w", err)
		}
	}

	finalDir := filepath.Join(i.Dir, manifest.UID)
	if err := os.RemoveAll(finalDir); err != nil {
		return nil, fmt.Errorf("plugin: replace previous version: %w", err)
	}
	if err := os.Rename(tmpDir, finalDir); err != nil {
		return nil, fmt.Errorf("plugin: activate package: %w", err)
	}
	cleanup = false

	return &Installed{
		Manifest:  manifest,
		Dir:       finalDir,
		SHA256:    digest,
		Signature: strings.TrimSpace(signatureB64),
		Signed:    signed,
		Files:     len(names),
	}, nil
}

// Uninstall deletes a plugin directory.
func (i *Installer) Uninstall(uid string) error {
	if !uidRe.MatchString(uid) {
		return errors.New("plugin: invalid uid")
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	target := filepath.Join(i.Dir, uid)
	if filepath.Dir(target) != filepath.Clean(i.Dir) {
		return errors.New("plugin: refusing to remove outside the plugin directory")
	}
	return os.RemoveAll(target)
}

// safeRelPath rejects absolute paths, traversal and Windows specific tricks.
func safeRelPath(name string) (string, error) {
	n := strings.ReplaceAll(strings.TrimSpace(name), "\\", "/")
	if n == "" {
		return "", errors.New("plugin: empty file name")
	}
	if strings.HasPrefix(n, "/") || strings.Contains(n, ":") {
		return "", fmt.Errorf("plugin: illegal absolute path %q", name)
	}
	clean := path.Clean(n)
	if clean == "." || strings.HasPrefix(clean, "../") || clean == ".." {
		return "", fmt.Errorf("plugin: illegal path %q", name)
	}
	if strings.Contains(clean, "\x00") {
		return "", fmt.Errorf("plugin: illegal path %q", name)
	}
	return clean, nil
}

// MarshalSettings renders manifest settings as JSON for storage.
func MarshalSettings(settings []Setting) string {
	if len(settings) == 0 {
		return "[]"
	}
	b, err := json.Marshal(settings)
	if err != nil {
		return "[]"
	}
	return string(b)
}
