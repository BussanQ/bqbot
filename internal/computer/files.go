package computer

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"strings"

	"bqbot/internal/store"
)

// WorkspaceFileRequest streams a single file or directory listing using the
// stored bridge credential. The caller owns the response body.
func WorkspaceFileRequest(ctx context.Context, st *store.Store, cfg store.ComputerConfig, method, resource, path string, body io.Reader, length int64) (*http.Response, error) {
	if !fs.ValidPath(path) || (resource != "list" && resource != "content") {
		return nil, errors.New("工作区文件路径无效")
	}
	cfg, token, err := workspaceConnection(cfg, st)
	if err != nil {
		return nil, err
	}
	target := strings.TrimRight(cfg.URL, "/") + "/files/" + resource + "?path=" + url.QueryEscape(path)
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, err
	}
	req.ContentLength = length
	req.Header.Set("Authorization", "Bearer "+token)
	return workspaceFileHTTP.Do(req)
}

var workspaceFileHTTP = &http.Client{
	Transport:     &http.Transport{Proxy: nil},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}
