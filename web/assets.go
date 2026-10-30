// Package web embeds the desktop frontend without exposing filesystem paths.
package web

import "embed"

// Assets contains the bundled HTML, styles, and JavaScript modules.
//
//go:embed *.html *.css *.js
var Assets embed.FS
