// Package web embeds the server dashboard so the shipped binary is fully
// self-contained.
package web

import "embed"

// Assets holds the dashboard files.
//
//go:embed assets
var Assets embed.FS
