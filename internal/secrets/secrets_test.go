package secrets

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	wardenv1 "github.com/go-freya/freya/services/warden/api/proto/warden/v1"
)

const secretValue = "s3cr3t-relay-token-value"

type fakeWarden struct {
	wardenv1.SecretsClient
	auth  string
	id    string
	value string
	err   error
}

func (f *fakeWarden) GetPassword(ctx context.Context, in *wardenv1.GetPasswordRequest, _ ...grpc.CallOption) (*wardenv1.GetPasswordResponse, error) {
	md, _ := metadata.FromOutgoingContext(ctx)
	if v := md.Get("authorization"); len(v) > 0 {
		f.auth = v[0]
	}
	f.id = in.GetId()
	if f.err != nil {
		return nil, f.err
	}
	return &wardenv1.GetPasswordResponse{Password: f.value}, nil
}

func TestWardenFetch(t *testing.T) {
	fw := &fakeWarden{value: secretValue}
	w := &Warden{Client: fw, Token: func(context.Context) (string, error) { return "platform-tok", nil }}
	v, err := w.Fetch(context.Background(), "sec-1")
	if err != nil || v != secretValue || fw.id != "sec-1" || fw.auth != "Bearer platform-tok" {
		t.Fatalf("fetch = %q %v (id %q auth %q)", v, err, fw.id, fw.auth)
	}
	fw.err = status.Error(codes.PermissionDenied, "forbidden "+secretValue)
	_, err = w.Fetch(context.Background(), "sec-1")
	if !errors.Is(err, ErrUnavailable) || strings.Contains(err.Error(), secretValue) || !strings.Contains(err.Error(), "PermissionDenied") {
		t.Fatalf("error = %v", err)
	}
	bad := &Warden{Client: fw, Token: func(context.Context) (string, error) { return "", ErrUnavailable }}
	if _, err := bad.Fetch(context.Background(), "x"); !errors.Is(err, ErrUnavailable) {
		t.Fatal("token failure")
	}
	var nilW *Warden
	if _, err := nilW.Fetch(context.Background(), "x"); !errors.Is(err, ErrUnavailable) {
		t.Fatal("nil warden")
	}
	if NewWarden(nil, nil).Client == nil {
		t.Fatal("NewWarden client")
	}
}

func TestFileToken(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "tok")
	if _, err := FileToken(p)(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatal("missing file")
	}
	_ = os.WriteFile(p, []byte("  \n"), 0o600)
	if _, err := FileToken(p)(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatal("empty file")
	}
	_ = os.WriteFile(p, []byte("tok\n"), 0o600)
	if v, err := FileToken(p)(context.Background()); err != nil || v != "tok" {
		t.Fatalf("token = %q %v", v, err)
	}
}

func TestResolver(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	p := filepath.Join(dir, "relay")
	_ = os.WriteFile(p, []byte(secretValue+"\n"), 0o600)
	wf := FetcherFunc(func(_ context.Context, id string) (string, error) { return "w:" + id, nil })
	r := Resolver{Warden: wf, AllowFile: true}
	cases := map[string]string{"warden:abc": "w:abc", "abc": "w:abc", "file:" + p: secretValue}
	for ref, want := range cases {
		if v, err := r.Fetch(ctx, ref); err != nil || v != want {
			t.Errorf("%s = %q %v", ref, v, err)
		}
	}
	if _, err := r.Fetch(ctx, " "); !errors.Is(err, ErrNotConfigured) {
		t.Fatal("empty ref")
	}
	if _, err := r.Fetch(ctx, "warden:"); !errors.Is(err, ErrNotConfigured) {
		t.Fatal("empty warden id")
	}
	if _, err := r.Fetch(ctx, "file:"+filepath.Join(dir, "none")); !errors.Is(err, ErrUnavailable) || strings.Contains(err.Error(), dir) {
		t.Fatalf("missing file: %v", err)
	}
	if _, err := (Resolver{}).Fetch(ctx, "file:"+p); !errors.Is(err, ErrUnavailable) {
		t.Fatal("file refs must be opt-in")
	}
	if _, err := (Resolver{}).Fetch(ctx, "abc"); !errors.Is(err, ErrUnavailable) {
		t.Fatal("no warden")
	}
}

func TestCachedRotationAndStaleServe(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)
	calls, value := 0, "v1"
	var fail error
	f := FetcherFunc(func(_ context.Context, ref string) (string, error) {
		calls++
		if fail != nil {
			return "", fail
		}
		if value == "" {
			return "", nil
		}
		return value + "@" + ref, nil
	})
	c := NewCached(f, "relay", "smtp", time.Minute)
	c.Now = func() time.Time { return now }
	if v, _ := c.RelayToken(ctx); v != "v1@relay" {
		t.Fatalf("relay = %q", v)
	}
	if v, _ := c.RelayToken(ctx); v != "v1@relay" || calls != 1 {
		t.Fatal("not cached")
	}
	value = "v2" // rotated in warden
	now = now.Add(2 * time.Minute)
	if v, _ := c.RelayToken(ctx); v != "v2@relay" || calls != 2 {
		t.Fatalf("rotation not re-read: %q", v)
	}
	fail = fmt.Errorf("%w: down", ErrUnavailable)
	now = now.Add(2 * time.Minute)
	if v, err := c.RelayToken(ctx); err != nil || v != "v2@relay" {
		t.Fatalf("stale serve = %q %v", v, err)
	}
	if _, err := c.SMTPPassword(ctx); !errors.Is(err, ErrUnavailable) {
		t.Fatal("never-fetched value must error")
	}
	now = now.Add(20 * time.Minute) // beyond MaxStale (10×Refresh)
	if _, err := c.RelayToken(ctx); !errors.Is(err, ErrUnavailable) {
		t.Fatal("stale value served beyond MaxStale")
	}
	fail = nil
	value = ""
	if _, err := c.SMTPPassword(ctx); !errors.Is(err, ErrEmpty) {
		t.Fatal("empty secret")
	}
	c.Invalidate()
	value = "v3"
	if v, _ := c.RelayToken(ctx); v != "v3@relay" {
		t.Fatal("invalidate")
	}
	if _, err := NewCached(f, "", "", time.Minute).SMTPPassword(ctx); !errors.Is(err, ErrNotConfigured) {
		t.Fatal("unconfigured ref")
	}
	if _, err := NewCached(nil, "r", "", time.Minute).RelayToken(ctx); !errors.Is(err, ErrUnavailable) {
		t.Fatal("nil fetcher")
	}
	if strings.Contains(c.String(), "v3") || strings.Contains(fmt.Sprintf("%v", c), "v3") {
		t.Fatal("value printed")
	}
	if (&Cached{}).now().IsZero() {
		t.Fatal("default clock")
	}
}

func TestFake(t *testing.T) {
	ctx := context.Background()
	f := &Fake{}
	if _, err := f.RelayToken(ctx); !errors.Is(err, ErrNotConfigured) {
		t.Fatal("empty relay")
	}
	if _, err := f.SMTPPassword(ctx); !errors.Is(err, ErrNotConfigured) {
		t.Fatal("empty smtp")
	}
	f.SetRelay("r")
	f.SMTP = "p"
	if v, _ := f.RelayToken(ctx); v != "r" || f.RelayHit != 3-1 {
		t.Fatalf("relay = %q hits %d", v, f.RelayHit)
	}
	if v, _ := f.SMTPPassword(ctx); v != "p" {
		t.Fatal("smtp")
	}
	f.Err = ErrUnavailable
	if _, err := f.RelayToken(ctx); !errors.Is(err, ErrUnavailable) {
		t.Fatal("relay err")
	}
	if _, err := f.SMTPPassword(ctx); !errors.Is(err, ErrUnavailable) {
		t.Fatal("smtp err")
	}
}
