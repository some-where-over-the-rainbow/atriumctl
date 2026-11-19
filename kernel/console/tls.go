package console

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// PrivateAddrs are this machine's loopback and private (home-network)
// addresses: the only ones the console listens on (decision 47).
func PrivateAddrs() ([]net.IP, error) {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var out []net.IP
	for _, i := range ifs {
		if i.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, _ := i.Addrs()
		for _, a := range addrs {
			n, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip := n.IP
			if ip.IsLoopback() || ip.IsPrivate() {
				out = append(out, ip)
			}
		}
	}
	return out, nil
}

// Certificate loads the console's self-signed certificate from dir, or
// makes a new one when it is missing, expires within 30 days, or does not
// cover every address and the hostname (decision 49).
func Certificate(dir string, ips []net.IP) (tls.Certificate, string, bool, error) {
	certPath, keyPath := filepath.Join(dir, "console-cert.pem"), filepath.Join(dir, "console-key.pem")
	host, _ := os.Hostname()
	if c, err := tls.LoadX509KeyPair(certPath, keyPath); err == nil {
		if leaf, err := x509.ParseCertificate(c.Certificate[0]); err == nil && covers(leaf, host, ips) &&
			time.Until(leaf.NotAfter) > 30*24*time.Hour {
			return c, fingerprint(leaf.Raw), false, nil
		}
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, "", false, err
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "atriumctl console " + host},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(397 * 24 * time.Hour), // phones refuse longer
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  ips,
	}
	if host != "" {
		tmpl.DNSNames = append(tmpl.DNSNames, host, host+".local")
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, "", false, err
	}
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return tls.Certificate{}, "", false, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return tls.Certificate{}, "", false, err
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0o600); err != nil {
		return tls.Certificate{}, "", false, err
	}
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		return tls.Certificate{}, "", false, err
	}
	c, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return tls.Certificate{}, "", false, err
	}
	return c, fingerprint(der), true, nil
}

func covers(c *x509.Certificate, host string, ips []net.IP) bool {
	for _, ip := range ips {
		found := false
		for _, have := range c.IPAddresses {
			if have.Equal(ip) {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	if host == "" {
		return true
	}
	for _, d := range c.DNSNames {
		if d == host {
			return true
		}
	}
	return false
}

func fingerprint(der []byte) string {
	h := sha256.Sum256(der)
	var parts []string
	for _, b := range h {
		parts = append(parts, fmt.Sprintf("%02X", b))
	}
	return strings.Join(parts, ":")
}

var errNoAddrs = errors.New("no loopback or private addresses to listen on")
