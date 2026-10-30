package webgo

import "github.com/egoist/mygo/plugins/updater"

// CheckForUpdates uses MyGo's signature-verifying installer and confirmation UI.
func (d *Desktop) CheckForUpdates() { updater.CheckForUpdates() }
