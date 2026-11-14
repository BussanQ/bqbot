package webgo

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"time"

	"bqbot/internal/service"
	"github.com/egoist/mygo"
)

// Only the native picker supplies host paths. The page sees workspace paths.
func uploadComputerFile(api *service.Client, bot, dir string) (bool, error) {
	if !fs.ValidPath(dir) {
		return false, errors.New("工作区目录无效")
	}
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Title: "上传文件到工作区"})
	if err != nil || len(paths) == 0 {
		return false, err
	}
	f, err := os.Open(paths[0])
	if err != nil {
		return false, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, errors.New("请选择普通文件")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	err = api.UploadWorkspaceFile(ctx, bot, path.Join(dir, filepath.Base(paths[0])), f, info.Size())
	return err == nil, err
}

func downloadComputerFile(api *service.Client, bot, name string) (bool, error) {
	base := path.Base(name)
	if !fs.ValidPath(name) || name == "." || !filepath.IsLocal(base) || filepath.Base(base) != base {
		return false, errors.New("工作区文件名无效")
	}
	dest, err := mygo.Dialog.Save(mygo.SaveDialogOptions{Title: "保存工作区文件", DefaultPath: base})
	if err != nil || dest == "" {
		return false, err
	}
	// Keep the chosen destination intact until all remote bytes have arrived.
	f, err := os.CreateTemp(filepath.Dir(dest), ".bqbot-download-*")
	if err != nil {
		return false, err
	}
	defer os.Remove(f.Name())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	err = api.DownloadWorkspaceFile(ctx, bot, name, f)
	closeErr := f.Close()
	if err != nil {
		return false, err
	}
	if closeErr != nil {
		return false, closeErr
	}
	err = os.Rename(f.Name(), dest)
	return err == nil, err
}
