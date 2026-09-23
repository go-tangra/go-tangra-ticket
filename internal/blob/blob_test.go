package blob

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// wantSHA256 computes the reference checksum the way a caller would independently.
func wantSHA256(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestFakePutGetRoundTrip(t *testing.T) {
	ctx := context.Background()
	f := NewFake()

	payload := []byte("synthetic paperless document bytes \x00\x01\x02 not-real-data")
	key := "docs/2026/abc.pdf"

	got, err := f.Put(ctx, key, bytes.NewReader(payload), int64(len(payload)), "application/pdf")
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	// checksum must equal the SHA-256 hex of the input bytes
	if want := wantSHA256(payload); got != want {
		t.Fatalf("checksum mismatch:\n got=%s\nwant=%s", got, want)
	}

	rc, err := f.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer rc.Close()
	back, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(back, payload) {
		t.Fatalf("round-trip bytes differ:\n got=%q\nwant=%q", back, payload)
	}
}

func TestFakePresignGetRetrievable(t *testing.T) {
	ctx := context.Background()
	f := NewFake()

	payload := []byte("presign me")
	key := "presign/key.txt"
	if _, err := f.Put(ctx, key, bytes.NewReader(payload), int64(len(payload)), "text/plain"); err != nil {
		t.Fatalf("Put: %v", err)
	}

	url, err := f.PresignGet(ctx, key, 5*time.Minute)
	if err != nil {
		t.Fatalf("PresignGet: %v", err)
	}
	if url == "" {
		t.Fatal("PresignGet returned empty URL")
	}
	// The fake URL must be deterministic and reference the key.
	if !strings.Contains(url, key) {
		t.Fatalf("presigned URL %q does not reference key %q", url, key)
	}

	// "Retrievable" for the fake: the key it points at still resolves via Get.
	rc, err := f.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get after presign: %v", err)
	}
	defer rc.Close()
	back, _ := io.ReadAll(rc)
	if !bytes.Equal(back, payload) {
		t.Fatalf("presigned key content differs: got=%q want=%q", back, payload)
	}
}

func TestFakeDeleteRemovesObject(t *testing.T) {
	ctx := context.Background()
	f := NewFake()

	key := "delete/me.bin"
	payload := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	if _, err := f.Put(ctx, key, bytes.NewReader(payload), int64(len(payload)), "application/octet-stream"); err != nil {
		t.Fatalf("Put: %v", err)
	}

	if err := f.Delete(ctx, key); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if _, err := f.Get(ctx, key); err == nil {
		t.Fatal("Get after Delete: expected error, got nil")
	}

	// Delete is idempotent: deleting an absent key must not error.
	if err := f.Delete(ctx, key); err != nil {
		t.Fatalf("second Delete should be a no-op, got: %v", err)
	}
}

// TestSecretKeyNeverInErrors ensures the real client's error strings never leak
// the secret key. We point the client at a dead endpoint so every operation
// fails, then assert the secret never appears in any returned error.
func TestSecretKeyNeverInErrors(t *testing.T) {
	const secret = "SUPER-SECRET-KEY-do-not-leak-42"

	// A server that immediately closes connections so all ops error out.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "no hijack", http.StatusInternalServerError)
			return
		}
		conn, _, err := hj.Hijack()
		if err == nil {
			_ = conn.Close()
		}
	}))
	defer srv.Close()

	endpoint := strings.TrimPrefix(srv.URL, "http://")
	st, err := New(Config{
		Endpoint:  endpoint,
		Bucket:    "paperless",
		Region:    "us-east-1",
		AccessKey: "AKIAFAKE",
		SecretKey: secret,
		UseSSL:    false,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	check := func(name string, err error) {
		if err == nil {
			return // an unexpected success still can't leak a secret
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("%s error leaked the secret key: %v", name, err)
		}
	}

	payload := []byte("bytes that must never appear alongside the secret in errors")
	_, putErr := st.Put(ctx, "k", bytes.NewReader(payload), int64(len(payload)), "text/plain")
	check("Put", putErr)

	_, getErr := st.Get(ctx, "k")
	check("Get", getErr)

	// PresignGet is offline (no round-trip) but assert it still never leaks.
	presignURL, presignErr := st.PresignGet(ctx, "k", time.Minute)
	check("PresignGet", presignErr)
	if strings.Contains(presignURL, secret) {
		t.Fatalf("PresignGet URL leaked the secret key: %s", presignURL)
	}

	check("Delete", st.Delete(ctx, "k"))
}

func TestFakeEnsureBucketNoop(t *testing.T) {
	if err := NewFake().EnsureBucket(context.Background()); err != nil {
		t.Fatalf("fake EnsureBucket: %v", err)
	}
}

// The real client must satisfy Store (compile-time), including EnsureBucket.
var _ Store = (*client)(nil)
