package webadapter

import "embed"

//go:embed web/*
var embeddedWeb embed.FS
