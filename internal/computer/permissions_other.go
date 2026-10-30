//go:build !darwin

package computer

import "errors"

// HostPermissions reports that bundled local control is not available here.
func HostPermissions() (PermissionStatus, error) {
	return PermissionStatus{}, nil
}

// RequestHostPermissions cannot grant access on unsupported platforms.
func RequestHostPermissions() (PermissionStatus, error) {
	return PermissionStatus{}, errors.New("内置本机控制目前仅支持 macOS")
}
