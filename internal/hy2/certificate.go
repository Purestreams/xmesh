package hy2

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"strings"
	"time"
)

// Generate provisions a persistent ECDSA identity. Pin is the SHA-256 hash of
// the DER certificate, as used by Hysteria's pinSHA256 (not an SPKI hash).
func Generate(host string, now time.Time) (certificate, privateKey, pin string, err error) {
	host = strings.Trim(host, "[]")
	if host == "" || strings.ContainsAny(host, "/?#@ \t\r\n") || (strings.Contains(host, ":") && net.ParseIP(host) == nil) {
		return "", "", "", fmt.Errorf("invalid HY2 certificate host")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", "", err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return "", "", "", err
	}
	template := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: host},
		NotBefore: now.Add(-time.Hour), NotAfter: now.AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	if ip := net.ParseIP(host); ip != nil {
		template.IPAddresses = []net.IP{ip}
	} else {
		template.DNSNames = []string{host}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return "", "", "", err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return "", "", "", err
	}
	hash := sha256.Sum256(der)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})), hex.EncodeToString(hash[:]), nil
}

func Validate(certificate, privateKey, pin, host string, now time.Time) error {
	pair, err := tls.X509KeyPair([]byte(certificate), []byte(privateKey))
	if err != nil {
		return fmt.Errorf("HY2 certificate/key: %w", err)
	}
	cert, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return err
	}
	hash := sha256.Sum256(cert.Raw)
	if pin != hex.EncodeToString(hash[:]) {
		return fmt.Errorf("HY2 certificate SHA-256 pin mismatch")
	}
	if now.Before(cert.NotBefore) || !now.Before(cert.NotAfter) {
		return fmt.Errorf("HY2 certificate is outside its validity period")
	}
	if err := cert.VerifyHostname(strings.Trim(host, "[]")); err != nil {
		return fmt.Errorf("HY2 certificate host: %w", err)
	}
	if !bytes.Equal(cert.RawIssuer, cert.RawSubject) {
		return fmt.Errorf("HY2 certificate must be self-signed")
	}
	if err := cert.CheckSignature(cert.SignatureAlgorithm, cert.RawTBSCertificate, cert.Signature); err != nil {
		return fmt.Errorf("HY2 self-signature: %w", err)
	}
	return nil
}
