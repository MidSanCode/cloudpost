package sender

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/emersion/go-msgauth/dkim"

	"cloudpost/internal/state"
)

// DKIM signing: outbound messages whose From domain matches the primary
// domain are signed with the configured RSA key before delivery. The private
// key never leaves the config file; the public half is published as DNS TXT
// at <selector>._domainkey.<domain> so receiving servers can verify.

var (
	dkimMu      sync.Mutex
	dkimLastPEM string
	dkimLastKey *rsa.PrivateKey
)

// ParseDKIMKey parses a PEM-encoded RSA private key (PKCS#1 or PKCS#8).
func ParseDKIMKey(pemStr string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, fmt.Errorf("no PEM block found")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("bad private key: %w", err)
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("only RSA keys are supported, got %T", k)
	}
	return rk, nil
}

// GenerateDKIMKey creates a 2048-bit RSA keypair. The private half is
// returned PEM-encoded for storage; the public half is returned as the
// base64 (DER SPKI) payload for the DNS TXT record.
func GenerateDKIMKey() (privPEM string, pubB64 string, err error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return "", "", err
	}
	privPEM = string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return "", "", err
	}
	return privPEM, base64Std(der), nil
}

// DKIMDNSRecord builds the TXT record to publish for the given selector.
func DKIMDNSRecord(selector, domain string, key *rsa.PrivateKey) string {
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("v=DKIM1; k=rsa; p=%s", base64Std(der))
}

// base64Std encodes DER bytes as the standard-base64 payload used in the
// DNS key record (no line wrapping).
func base64Std(der []byte) string {
	return stdBase64.EncodeToString(der)
}

var stdBase64 = base64.StdEncoding

func fromDomain(addr string) string {
	i := strings.LastIndexByte(addr, '@')
	if i < 0 || i == len(addr)-1 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(addr[i+1:]))
}

// dkimSign returns the DKIM-signed message when signing applies to `from`,
// otherwise the original bytes unchanged.
func (s *Sender) dkimSign(from string, raw []byte) ([]byte, bool) {
	cfg := s.deps.State.Config()
	if cfg == nil || !cfg.DKIMEnabled || cfg.DKIMSelector == "" || cfg.DKIMKeyPEM == "" {
		return raw, false
	}
	domain := fromDomain(from)
	if domain == "" || !strings.EqualFold(domain, cfg.PrimaryDomain) {
		return raw, false // only sign mail from our own primary domain
	}
	key := s.loadKey(cfg)
	if key == nil {
		return raw, false
	}
	var out bytes.Buffer
	opts := &dkim.SignOptions{
		Domain:                 cfg.PrimaryDomain,
		Selector:               cfg.DKIMSelector,
		Signer:                 key,
		HeaderCanonicalization: dkim.CanonicalizationRelaxed,
		BodyCanonicalization:   dkim.CanonicalizationRelaxed,
	}
	if err := dkim.Sign(&out, bytes.NewReader(raw), opts); err != nil {
		log.Printf("[dkim] sign failed: %v (sending unsigned)", err)
		return raw, false
	}
	return out.Bytes(), true
}

// loadKey parses and caches the private key so each queued message does not
// re-parse the PEM.
func (s *Sender) loadKey(cfg *state.Config) *rsa.PrivateKey {
	dkimMu.Lock()
	defer dkimMu.Unlock()
	if dkimLastKey != nil && dkimLastPEM == cfg.DKIMKeyPEM {
		return dkimLastKey
	}
	key, err := ParseDKIMKey(cfg.DKIMKeyPEM)
	if err != nil {
		log.Printf("[dkim] bad private key: %v (sending unsigned)", err)
		return nil
	}
	dkimLastKey, dkimLastPEM = key, cfg.DKIMKeyPEM
	return key
}
