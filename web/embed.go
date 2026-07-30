package web

import "embed"

// Assets contains the dashboard, login page, styles, scripts, and vendored chart library.
//
//go:embed *.html *.css *.js vendor/*
var Assets embed.FS
