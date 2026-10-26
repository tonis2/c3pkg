// Package storage keeps uploaded .c3l files, addressed by their SHA-256.
package storage

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
)

var ErrNotFound = errors.New("storage: not found")

var keyRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Storage is where package files live. The key is the file's lowercase hex SHA-256.
type Storage interface {
	// Put stores data under key. created is false if the key already existed.
	Put(key string, data []byte) (created bool, err error)
	Open(key string) (io.ReadSeekCloser, error)
	Delete(key string) error
}

// Local stores files on disk as <dir>/<key[:2]>/<key>.c3l.
type Local struct{ Dir string }

func (l Local) path(key string) (string, error) {
	if !keyRe.MatchString(key) {
		return "", fmt.Errorf("storage: invalid key %q", key)
	}
	return filepath.Join(l.Dir, key[:2], key+".c3l"), nil
}

func (l Local) Put(key string, data []byte) (bool, error) {
	p, err := l.path(key)
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(p); err == nil {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return false, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".upload-*")
	if err != nil {
		return false, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return false, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return false, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	// rename is atomic, so readers never see a partial file
	if err := os.Rename(tmp.Name(), p); err != nil {
		return false, err
	}
	return true, nil
}

func (l Local) Open(key string) (io.ReadSeekCloser, error) {
	p, err := l.path(key)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	return f, err
}

func (l Local) Delete(key string) error {
	p, err := l.path(key)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
