// Package resources embeds native application assets.
package resources

import _ "embed"

// Icon is shared by the packaged application and the development Dock icon.
//
//go:embed icon.png
var Icon []byte
