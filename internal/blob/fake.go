package blob

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"sync"
	"time"
)

// Fake is an in-memory Store used by tests and (optionally) the app in dev. It
// keeps object bytes in a map guarded by a mutex and computes the same SHA-256
// checksum on Put as the real client. PresignGet returns a deterministic,
// non-network fake URL. Zero value is ready to use.
type Fake struct {
	mu      sync.RWMutex
	objects map[string][]byte
	putErr  error
	delErr  error
}

// NewFake returns an initialised Fake. The zero value works too; this is just a
// convenience for callers that prefer a constructor.
func NewFake() *Fake {
	return &Fake{objects: make(map[string][]byte)}
}

// compile-time check that *Fake satisfies Store.
var _ Store = (*Fake)(nil)

// Put reads all of r (up to size is advisory only), stores the bytes under key,
// and returns the SHA-256 hex of those bytes.
// EnsureBucket is a no-op for the in-memory fake.
func (f *Fake) EnsureBucket(_ context.Context) error { return nil }

func (f *Fake) Put(_ context.Context, key string, r io.Reader, _ int64, _ string) (string, error) {
	f.mu.Lock()
	if f.putErr != nil {
		err := f.putErr
		f.putErr = nil
		f.mu.Unlock()
		return "", err
	}
	f.mu.Unlock()
	data, err := io.ReadAll(r)
	if err != nil {
		return "", fmt.Errorf("blob(fake): read body for %q: %w", key, err)
	}
	sum := sha256.Sum256(data)

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.objects == nil {
		f.objects = make(map[string][]byte)
	}
	// store a private copy so later mutations of the caller's slice can't alter it
	stored := make([]byte, len(data))
	copy(stored, data)
	f.objects[key] = stored

	return hex.EncodeToString(sum[:]), nil
}

// Get returns a reader over the stored bytes for key, or an error if absent.
func (f *Fake) Get(_ context.Context, key string) (io.ReadCloser, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	data, ok := f.objects[key]
	if !ok {
		return nil, fmt.Errorf("blob(fake): object %q not found", key)
	}
	buf := make([]byte, len(data))
	copy(buf, data)
	return io.NopCloser(bytes.NewReader(buf)), nil
}

// PresignGet returns a deterministic fake URL. It does not verify existence in a
// way the real presigner would; it is retrievable via getFakeURL in tests.
func (f *Fake) PresignGet(_ context.Context, key string, ttl time.Duration) (string, error) {
	return fmt.Sprintf("https://fake-blob.local/%s?ttl=%d", key, int64(ttl.Seconds())), nil
}

// Delete removes key. Deleting an absent key is a no-op (idempotent).
func (f *Fake) Delete(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.delErr != nil {
		err := f.delErr
		f.delErr = nil
		return err
	}
	delete(f.objects, key)
	return nil
}

// FailPut makes the next Put fail (tests). Zero value: disabled.
func (f *Fake) FailPut(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.putErr = err
}

// FailDelete makes the next Delete fail (tests).
func (f *Fake) FailDelete(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.delErr = err
}

// Len reports the number of stored objects.
func (f *Fake) Len() int {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return len(f.objects)
}
