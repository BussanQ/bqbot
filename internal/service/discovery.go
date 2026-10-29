package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
)

// Discovery is the private on-disk connection information for a standalone server.
type Discovery struct {
	URL   string `json:"url"`
	Token string `json:"token"`
}
type discoveryFile struct {
	info    os.FileInfo
	content []byte
}

// WriteDiscovery atomically publishes URL and Token in a 0600 file. Its parent
// must be a private directory; missing directories are created with mode 0700.
// Existing files are never overwritten, including stale files and symlinks.
// A caller must explicitly remove a stale discovery file before starting again.
func (s *Server) WriteDiscovery(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("service is closed")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	dir := filepath.Dir(abs)
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return errors.New("discovery directory must be private (0700) and not a symlink")
	}
	data, err := json.Marshal(Discovery{s.URL, s.Token})
	if err != nil {
		return err
	}
	data = append(data, '\n')
	f, err := os.CreateTemp(dir, ".bqbot-discovery-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	info, err = f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	// A hard link publishes the complete file atomically without replacing an
	// existing path, unlike Rename. The temporary name is removed on return.
	if err = os.Link(f.Name(), abs); err != nil {
		return err
	}
	s.discovery[abs] = discoveryFile{info, data}
	return nil
}

func (s *Server) removeDiscovery(path string, own discoveryFile) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || !os.SameFile(info, own.info) {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	opened, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	if !os.SameFile(opened, own.info) {
		f.Close()
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(f, 4096))
	f.Close()
	if err != nil {
		return err
	}
	if !bytes.Equal(data, own.content) {
		return nil
	}
	return os.Remove(path)
}
