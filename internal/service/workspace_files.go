package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"bqbot/internal/computer"
	"bqbot/internal/store"
)

type FileEntry struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	Size int64  `json:"size"`
}

func (s *Server) workspaceFileRoutes(mux *http.ServeMux) {
	serve := s.withBot(func(w http.ResponseWriter, r *http.Request, b store.Bot) {
		cfg, err := s.h.Store.Computer(b.ID)
		if err != nil {
			fail(w, invalid("找不到伙伴的电脑配置"))
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
		defer cancel()
		var input io.Reader
		if r.Method == http.MethodPut {
			input = r.Body
			_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(30 * time.Minute))
		}
		resource := r.PathValue("resource")
		resp, err := computer.WorkspaceFileRequest(ctx, s.h.Store, cfg, r.Method, resource, r.URL.Query().Get("path"), input, r.ContentLength)
		if err != nil {
			fail(w, invalid(err.Error()))
			return
		}
		defer resp.Body.Close()
		if resp.Header.Get("X-Workspace-Files") != "1" {
			if resp.StatusCode == http.StatusNotFound {
				fail(w, invalid("此工作区尚未支持文件传输，请更新 Workspace Bridge"))
			} else {
				fail(w, invalid(fmt.Sprintf("工作区文件服务不可用（HTTP %d）", resp.StatusCode)))
			}
			return
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			fail(w, problem{resp.StatusCode, strings.TrimSpace(string(msg))})
			return
		}
		if resp.ContentLength >= 0 {
			w.Header().Set("Content-Length", strconv.FormatInt(resp.ContentLength, 10))
		}
		w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
		w.WriteHeader(resp.StatusCode)
		if _, err := io.Copy(w, resp.Body); err != nil {
			panic(http.ErrAbortHandler)
		}
	})
	mux.HandleFunc("GET /v1/computers/{bot}/files/{resource}", serve)
	mux.HandleFunc("PUT /v1/computers/{bot}/files/{resource}", serve)
}

func workspaceFilePath(bot, resource, path string) string {
	return "/v1/computers/" + url.PathEscape(bot) + "/files/" + resource + "?path=" + url.QueryEscape(path)
}

func (c *Client) WorkspaceFiles(bot, path string) (files []FileEntry, err error) {
	err = c.do("GET", workspaceFilePath(bot, "list", path), nil, &files)
	return
}

func (c *Client) UploadWorkspaceFile(ctx context.Context, bot, path string, source io.Reader, size int64) error {
	resp, err := c.rawRequest(ctx, "PUT", workspaceFilePath(bot, "content", path), source, size, "application/octet-stream")
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

func (c *Client) DownloadWorkspaceFile(ctx context.Context, bot, path string, dest io.Writer) error {
	resp, err := c.request(ctx, "GET", workspaceFilePath(bot, "content", path), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, err = io.Copy(dest, resp.Body)
	return err
}
