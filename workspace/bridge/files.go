package main

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type fileEntry struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	Size int64  `json:"size"`
}

func workspaceFiles(root *os.Root) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Workspace-Files", "1")
		name := r.URL.Query().Get("path")
		if !fs.ValidPath(name) || (name == "." && r.URL.Path != "/files/list") {
			http.Error(w, "工作区路径无效", http.StatusBadRequest)
			return
		}
		// File transfers can outlast the short default deadline for control RPCs.
		rc := http.NewResponseController(w)
		_ = rc.SetReadDeadline(time.Now().Add(30 * time.Minute))
		_ = rc.SetWriteDeadline(time.Now().Add(30 * time.Minute))
		var err error
		switch {
		case r.URL.Path == "/files/list" && r.Method == http.MethodGet:
			err = listFiles(w, root, name)
		case r.URL.Path == "/files/content" && r.Method == http.MethodPut:
			err = receiveFile(root, name, r.Body)
			if err == nil {
				w.WriteHeader(http.StatusCreated)
			}
		case r.URL.Path == "/files/content" && r.Method == http.MethodGet:
			err = sendFile(w, root, name)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
		if err != nil {
			switch {
			case errors.Is(err, fs.ErrExist):
				http.Error(w, "同名文件已存在，请重命名后再上传", http.StatusConflict)
			case errors.Is(err, fs.ErrNotExist):
				http.Error(w, "文件或目录已不存在", http.StatusNotFound)
			case errors.Is(err, fs.ErrInvalid):
				http.Error(w, "请选择普通文件或目录", http.StatusBadRequest)
			default:
				http.Error(w, "无法访问文件，请检查权限和磁盘空间", http.StatusInternalServerError)
			}
		}
	}
}

func listFiles(w http.ResponseWriter, root *os.Root, name string) error {
	dir, err := root.OpenRoot(name)
	if err != nil {
		return err
	}
	defer dir.Close()
	items, err := fs.ReadDir(dir.FS(), ".")
	if err != nil {
		return err
	}
	entries := make([]fileEntry, 0, len(items))
	for _, item := range items {
		if strings.HasPrefix(item.Name(), ".bqbot-upload-") {
			continue
		}
		entry := fileEntry{Name: item.Name(), Kind: "other"}
		switch {
		case item.IsDir():
			entry.Kind = "dir"
		case item.Type().IsRegular():
			info, err := item.Info()
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				return err
			}
			entry.Kind = "file"
			entry.Size = info.Size()
		}
		entries = append(entries, entry)
	}
	slices.SortStableFunc(entries, func(a, b fileEntry) int {
		if (a.Kind == "dir") != (b.Kind == "dir") {
			if a.Kind == "dir" {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Name, b.Name)
	})
	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(entries)
}

func receiveFile(root *os.Root, name string, body io.Reader) error {
	dir, err := root.OpenRoot(path.Dir(name))
	if err != nil {
		return err
	}
	defer dir.Close()
	base := path.Base(name)
	if _, err = dir.Lstat(base); err == nil {
		return fs.ErrExist
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	tmp := ".bqbot-upload-" + rand.Text()
	f, err := dir.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer dir.Remove(tmp)
	_, err = io.Copy(f, body)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	// Publish only a complete file, without overwriting a concurrent upload.
	return dir.Link(tmp, base)
}

func sendFile(w http.ResponseWriter, root *os.Root, name string) error {
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fs.ErrInvalid
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	w.WriteHeader(http.StatusOK)
	if _, err = io.CopyN(w, f, info.Size()); err != nil {
		panic(http.ErrAbortHandler)
	}
	return nil
}
