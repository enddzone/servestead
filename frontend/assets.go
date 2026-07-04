package frontend

import "embed"

// Assets contains the local UI CSS and JavaScript served by servestead ui.
//
//go:embed assets/*
var Assets embed.FS
