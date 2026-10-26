package storage_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"testing"

	"github.com/tonis/c3pkg/internal/storage"
)

func TestLocal(t *testing.T) {
	s := storage.Local{Dir: t.TempDir()}
	sum := sha256.Sum256([]byte("data"))
	key := hex.EncodeToString(sum[:])

	if created, err := s.Put(key, []byte("data")); err != nil || !created {
		t.Fatalf("first put: %v %v", created, err)
	}
	if created, err := s.Put(key, []byte("data")); err != nil || created {
		t.Fatalf("second put: %v %v", created, err)
	}
	f, err := s.Open(key)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(f)
	f.Close()
	if string(got) != "data" {
		t.Errorf("read %q", got)
	}
	if err := s.Delete(key); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Open(key); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("after delete: %v", err)
	}
	if err := s.Delete(key); err != nil {
		t.Errorf("deleting a missing key should not fail: %v", err)
	}
}

func TestLocalRejectsBadKeys(t *testing.T) {
	s := storage.Local{Dir: t.TempDir()}
	for _, key := range []string{"", "../../etc/passwd", "abc", "ZZ" + string(make([]byte, 62))} {
		if _, err := s.Put(key, nil); err == nil {
			t.Errorf("Put accepted key %q", key)
		}
		if _, err := s.Open(key); err == nil {
			t.Errorf("Open accepted key %q", key)
		}
	}
}
