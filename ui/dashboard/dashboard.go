// Package dashboard embeds the SmartEfficiency dashboard's static assets
// (ui/dashboard/assets/) into the tray binary via go:embed, so the tray
// executable is fully self-contained - no separate files to install or find
// at runtime.
package dashboard

import "embed"

//go:embed assets
var Assets embed.FS
