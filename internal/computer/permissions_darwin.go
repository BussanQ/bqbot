//go:build darwin

package computer

import (
	"errors"
	"fmt"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

var permissionAPI struct {
	once        sync.Once
	err         error
	trusted     func() bool
	prompt      func(uintptr) bool
	preflight   func() bool
	request     func() bool
	dictionary  func(uintptr, *uintptr, *uintptr, int64, uintptr, uintptr) uintptr
	release     func(uintptr)
	promptKey   uintptr
	booleanTrue uintptr
}

func loadPermissionAPI() error {
	permissionAPI.once.Do(func() {
		load := func(path string) uintptr {
			if permissionAPI.err != nil {
				return 0
			}
			h, err := purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_LOCAL)
			if err != nil {
				permissionAPI.err = fmt.Errorf("load macOS permissions: %w", err)
			}
			return h
		}
		ax := load("/System/Library/Frameworks/ApplicationServices.framework/ApplicationServices")
		cg := load("/System/Library/Frameworks/CoreGraphics.framework/CoreGraphics")
		cf := load("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation")
		symbol := func(h uintptr, name string) uintptr {
			if permissionAPI.err != nil {
				return 0
			}
			p, err := purego.Dlsym(h, name)
			if err != nil {
				permissionAPI.err = fmt.Errorf("macOS permission API %s: %w", name, err)
			}
			return p
		}
		bind := func(fn any, h uintptr, name string) {
			if p := symbol(h, name); p != 0 {
				purego.RegisterFunc(fn, p)
			}
		}
		bind(&permissionAPI.trusted, ax, "AXIsProcessTrusted")
		bind(&permissionAPI.prompt, ax, "AXIsProcessTrustedWithOptions")
		bind(&permissionAPI.preflight, cg, "CGPreflightScreenCaptureAccess")
		bind(&permissionAPI.request, cg, "CGRequestScreenCaptureAccess")
		bind(&permissionAPI.dictionary, cf, "CFDictionaryCreate")
		bind(&permissionAPI.release, cf, "CFRelease")
		if p := symbol(ax, "kAXTrustedCheckOptionPrompt"); p != 0 {
			permissionAPI.promptKey = *(*uintptr)(unsafe.Pointer(p))
		}
		if p := symbol(cf, "kCFBooleanTrue"); p != 0 {
			permissionAPI.booleanTrue = *(*uintptr)(unsafe.Pointer(p))
		}
	})
	return permissionAPI.err
}

// HostPermissions checks permissions without showing a system prompt.
func HostPermissions() (PermissionStatus, error) {
	p := PermissionStatus{Supported: true}
	if err := loadPermissionAPI(); err != nil {
		return p, err
	}
	p.Accessibility = permissionAPI.trusted()
	p.ScreenRecording = permissionAPI.preflight()
	return p, nil
}

// RequestHostPermissions must be called on the GUI thread after a user gesture.
func RequestHostPermissions() (PermissionStatus, error) {
	if err := loadPermissionAPI(); err != nil {
		return PermissionStatus{Supported: true}, err
	}
	key, value := permissionAPI.promptKey, permissionAPI.booleanTrue
	options := permissionAPI.dictionary(0, &key, &value, 1, 0, 0)
	if options == 0 {
		return PermissionStatus{Supported: true}, errors.New("create macOS permission prompt options")
	}
	permissionAPI.prompt(options)
	permissionAPI.release(options)
	permissionAPI.request()
	return HostPermissions()
}
