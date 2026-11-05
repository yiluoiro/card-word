package payment

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"sort"
	"strings"
)

func keyDER(value string) ([]byte, error) {
	if b, _ := pem.Decode([]byte(value)); b != nil {
		return b.Bytes, nil
	}
	return base64.StdEncoding.DecodeString(strings.Join(strings.Fields(value), ""))
}
func parsePrivate(value string) (*rsa.PrivateKey, error) {
	b, err := keyDER(value)
	if err != nil {
		return nil, errors.New("私钥编码无效")
	}
	k, err := x509.ParsePKCS1PrivateKey(b)
	if err != nil {
		p, e := x509.ParsePKCS8PrivateKey(b)
		if e != nil {
			return nil, errors.New("请填写 RSA PKCS#1 或 PKCS#8 私钥")
		}
		var ok bool
		k, ok = p.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("需要 RSA 私钥")
		}
	}
	if k.N.BitLen() < 2048 {
		return nil, errors.New("RSA 私钥至少需要 2048 位")
	}
	return k, k.Validate()
}
func parsePublic(value string) (*rsa.PublicKey, error) {
	b, err := keyDER(value)
	if err != nil {
		return nil, errors.New("公钥编码无效")
	}
	p, err := x509.ParsePKIXPublicKey(b)
	if err != nil {
		return nil, errors.New("请填写 PEM 或 Base64 格式的 RSA 公钥")
	}
	k, ok := p.(*rsa.PublicKey)
	if !ok || k.N.BitLen() < 2048 {
		return nil, errors.New("RSA 公钥至少需要 2048 位")
	}
	return k, nil
}
func rsaSign(k *rsa.PrivateKey, text string) (string, error) {
	hash := sha256.Sum256([]byte(text))
	sig, err := rsa.SignPKCS1v15(rand.Reader, k, crypto.SHA256, hash[:])
	return base64.StdEncoding.EncodeToString(sig), err
}
func rsaVerify(k *rsa.PublicKey, text, signature string) error {
	sig, err := base64.StdEncoding.DecodeString(signature)
	if err != nil {
		return errors.New("invalid signature encoding")
	}
	hash := sha256.Sum256([]byte(text))
	return rsa.VerifyPKCS1v15(k, crypto.SHA256, hash[:], sig)
}
func canonical(params map[string]string, excludeType bool) string {
	keys := make([]string, 0, len(params))
	for k, v := range params {
		if k != "sign" && v != "" && (!excludeType || k != "sign_type") {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, k+"="+params[k])
	}
	return strings.Join(pairs, "&")
}
func byteLimit(s string, n int) string {
	for len(s) > n {
		r := []rune(s)
		s = string(r[:len(r)-1])
	}
	return s
}
