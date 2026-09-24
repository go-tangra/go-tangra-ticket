package inbound

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEdgeRoutes(t *testing.T) {
	var got []byte
	mail := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			Refusal(w, http.StatusRequestEntityTooLarge)
			return
		}
		got = b
		w.WriteHeader(http.StatusAccepted)
	})
	s := NewServer(ServerConfig{MaxBodyBytes: 16}, mail, nil)
	h := s.Handler()

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", PathHealthz, nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "ok") {
		t.Fatalf("healthz = %d", w.Code)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", PathMail, strings.NewReader("Subject: hi\r\n")))
	if w.Code != 202 || string(got) != "Subject: hi\r\n" {
		t.Fatalf("mail = %d %q", w.Code, got)
	}
	// declared oversize and streamed oversize both refused generically
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", PathMail, strings.NewReader(strings.Repeat("x", 64))))
	if w.Code != 413 || strings.TrimSpace(w.Body.String()) != `{"outcome":"refused"}` {
		t.Fatalf("oversize = %d %s", w.Code, w.Body)
	}
	r := httptest.NewRequest("POST", PathMail, strings.NewReader(strings.Repeat("x", 64)))
	r.ContentLength = -1
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 413 {
		t.Fatalf("streamed oversize = %d", w.Code)
	}
	for _, c := range []struct{ m, p string }{{"GET", "/"}, {"GET", PathMail}, {"POST", "/inbound/other"}} {
		w = httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(c.m, c.p, nil))
		if w.Code != 404 && w.Code != 405 {
			t.Errorf("%s %s = %d", c.m, c.p, w.Code)
		}
	}
	// no handler wired: 503 so the relay retries
	w = httptest.NewRecorder()
	NewServer(ServerConfig{}, nil, nil).Handler().ServeHTTP(w, httptest.NewRequest("POST", PathMail, strings.NewReader("x")))
	if w.Code != 503 || strings.TrimSpace(w.Body.String()) != `{"outcome":"refused"}` {
		t.Fatalf("unwired = %d %s", w.Code, w.Body)
	}
}

func selfSigned(t *testing.T) (certFile, keyFile string) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"}, DNSNames: []string{"localhost"},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kb, _ := x509.MarshalECPrivateKey(key)
	dir := t.TempDir()
	certFile, keyFile = filepath.Join(dir, "c.pem"), filepath.Join(dir, "k.pem")
	_ = os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
	_ = os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0o600)
	return
}

func TestServeTLSAndShutdown(t *testing.T) {
	cert, key := selfSigned(t)
	s := NewServer(ServerConfig{CertFile: cert, KeyFile: key}, nil, nil)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, lis) }()
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}} // #nosec G402 -- test against a self-signed cert
	res, err := client.Get("https://" + lis.Addr().String() + PathHealthz)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != 200 || res.TLS == nil {
		t.Fatalf("tls healthz = %d", res.StatusCode)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("serve: %v", err)
	}
}

func TestServeInsecureAndErrors(t *testing.T) {
	s := NewServer(ServerConfig{Insecure: true, Addr: "127.0.0.1:0"}, nil, nil)
	if c, err := s.TLSConfig(); c != nil || err != nil {
		t.Fatal("insecure tls config")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("run: %v", err)
	}
	bad := NewServer(ServerConfig{CertFile: "/nonexistent", KeyFile: "/nonexistent"}, nil, nil)
	lis, _ := net.Listen("tcp", "127.0.0.1:0")
	if err := bad.Serve(context.Background(), lis); err == nil {
		t.Fatal("missing cert accepted")
	}
	if err := NewServer(ServerConfig{Addr: "bad::addr::"}, nil, nil).Run(context.Background()); err == nil {
		t.Fatal("bad addr accepted")
	}
}
