// Package version exposes build metadata injected via -ldflags.
package version

// Version is overridden at build time:
//
//	go build -ldflags "-X github.com/Caxson/TalkTalk/internal/version.Version=v0.1.0"
var Version = "dev"
