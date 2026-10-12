package hy2

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"strings"
	"testing"
	"time"
)

func TestSelfSignedIdentityAndValidation(t *testing.T) {
	for _, host := range []string{"edge.example", "127.0.0.1", "2001:db8::1"} {
		t.Run(host, func(t *testing.T) {
			now := time.Now()
			certificate, key, pin, err := Generate(host, now)
			if err != nil {
				t.Fatal(err)
			}
			if err := Validate(certificate, key, pin, host, now); err != nil {
				t.Fatal(err)
			}
			if err := Validate(certificate, key, strings.Repeat("0", 64), host, now); err == nil {
				t.Fatal("wrong pin accepted")
			}
			if err := Validate(certificate, key, pin, "other.example", now); err == nil {
				t.Fatal("wrong host accepted")
			}
			if err := Validate(certificate, key, pin, host, now.AddDate(11, 0, 0)); err == nil {
				t.Fatal("expired certificate accepted")
			}
			pair, err := tls.X509KeyPair([]byte(certificate), []byte(key))
			if err != nil {
				t.Fatal(err)
			}
			block, _ := pem.Decode([]byte(certificate))
			cert, err := x509.ParseCertificate(block.Bytes)
			if err != nil {
				t.Fatal(err)
			}
			roots := x509.NewCertPool()
			roots.AddCert(cert)
			if _, err := cert.Verify(x509.VerifyOptions{Roots: roots, DNSName: host, CurrentTime: now}); err != nil {
				t.Fatal(err)
			}
			if len(pair.Certificate) != 1 {
				t.Fatal("unexpected certificate chain")
			}
		})
	}
}
