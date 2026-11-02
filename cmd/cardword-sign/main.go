// Command cardword-sign manages Ed25519 signing keys for card word plugin
// packages.
//
//	cardword-sign keygen --out ./keys
//	cardword-sign sign --key ./keys/ed25519.key --in plugin.zip [--sig plugin.sig]
//	cardword-sign verify --pub ./keys/ed25519.pub --in plugin.zip --sig plugin.sig
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "keygen":
		err = keygen(os.Args[2:])
	case "sign":
		err = sign(os.Args[2:])
	case "verify":
		err = verify(os.Args[2:])
	case "-h", "--help", "help":
		usage()
		return
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "cardword-sign:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `cardword-sign - plugin package signing helper

Usage:
  cardword-sign keygen --out DIR
      Generate an Ed25519 key pair. Private key: DIR/ed25519.key (0600),
      public key: DIR/ed25519.pub.

  cardword-sign sign --key FILE --in PACKAGE.zip [--sig FILE]
      Sign the package bytes. Prints the base64 signature; writes it to
      --sig when provided. Use the same value in the admin upload form or
      in your marketplace index.

  cardword-sign verify --pub FILE --in PACKAGE.zip --sig SIGNATURE
      Verify a signature against a package.

The core verifies the signature over the whole ZIP byte stream, so the
package must not be re-zipped after signing.
`)
}

func keygen(args []string) error {
	fs := flag.NewFlagSet("keygen", flag.ContinueOnError)
	out := fs.String("out", "./keys", "output directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := os.MkdirAll(*out, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", *out, err)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("generate key: %w", err)
	}
	seed := priv.Seed()
	keyPath := filepath.Join(*out, "ed25519.key")
	pubPath := filepath.Join(*out, "ed25519.pub")
	if err := os.WriteFile(keyPath, []byte(base64.StdEncoding.EncodeToString(seed)), 0o600); err != nil {
		return fmt.Errorf("write private key: %w", err)
	}
	if err := os.WriteFile(pubPath, []byte(base64.StdEncoding.EncodeToString(pub)), 0o644); err != nil {
		return fmt.Errorf("write public key: %w", err)
	}
	fmt.Printf("private key: %s\npublic key:  %s\n\nTrust this public key with:\n  CARDWORD_PLUGIN_PUBLIC_KEYS=\"%s\"\n",
		keyPath, pubPath, base64.StdEncoding.EncodeToString(pub))
	return nil
}

func loadPrivateKey(path string) (ed25519.PrivateKey, error) {
	raw, err := os.ReadFile(strings.TrimSpace(path))
	if err != nil {
		return nil, fmt.Errorf("read key: %w", err)
	}
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		return nil, fmt.Errorf("decode key (expect base64): %w", err)
	}
	switch len(seed) {
	case ed25519.SeedSize:
		return ed25519.NewKeyFromSeed(seed), nil
	case ed25519.PrivateKeySize:
		return ed25519.PrivateKey(seed), nil
	default:
		return nil, fmt.Errorf("unexpected key length %d", len(seed))
	}
}

func sign(args []string) error {
	fs := flag.NewFlagSet("sign", flag.ContinueOnError)
	keyPath := fs.String("key", "", "private key file")
	in := fs.String("in", "", "plugin zip")
	sigOut := fs.String("sig", "", "optional signature output file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *keyPath == "" || *in == "" {
		return fmt.Errorf("--key and --in are required")
	}
	key, err := loadPrivateKey(*keyPath)
	if err != nil {
		return err
	}
	pkg, err := os.ReadFile(*in)
	if err != nil {
		return fmt.Errorf("read package: %w", err)
	}
	if len(pkg) == 0 {
		return fmt.Errorf("package is empty")
	}
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(key, pkg))
	fmt.Println(sig)
	if *sigOut != "" {
		if err := os.WriteFile(*sigOut, []byte(sig), 0o644); err != nil {
			return fmt.Errorf("write signature: %w", err)
		}
		fmt.Fprintf(os.Stderr, "signature written to %s\n", *sigOut)
	}
	return nil
}

func verify(args []string) error {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	pubPath := fs.String("pub", "", "public key file")
	in := fs.String("in", "", "plugin zip")
	sig := fs.String("sig", "", "base64 signature (or path to a file containing it)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *pubPath == "" || *in == "" || *sig == "" {
		return fmt.Errorf("--pub, --in and --sig are required")
	}
	raw, err := os.ReadFile(*pubPath)
	if err != nil {
		return fmt.Errorf("read public key: %w", err)
	}
	pub, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		return fmt.Errorf("decode public key: %w", err)
	}
	if len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("public key must be %d bytes", ed25519.PublicKeySize)
	}
	pkg, err := os.ReadFile(*in)
	if err != nil {
		return fmt.Errorf("read package: %w", err)
	}
	sigValue := *sig
	if st, err := os.Stat(sigValue); err == nil && !st.IsDir() {
		body, err := os.ReadFile(sigValue)
		if err != nil {
			return fmt.Errorf("read signature file: %w", err)
		}
		sigValue = strings.TrimSpace(string(body))
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(sigValue))
	if err != nil {
		return fmt.Errorf("decode signature: %w", err)
	}
	if !ed25519.Verify(ed25519.PublicKey(pub), pkg, decoded) {
		return fmt.Errorf("signature does not match the package")
	}
	fmt.Println("signature OK")
	return nil
}
