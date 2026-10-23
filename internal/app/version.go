// Package app wires pulsed together and carries build metadata.
package app

import (
	"fmt"
	"runtime"
)

// Build metadata, set with -ldflags "-X github.com/aurimasniekis/go-pulse/internal/app.Version=…".
var (
	Version = "dev"
	Commit  = ""
	Date    = ""
)

// BuildInfo is the JSON shape of `pulsed version --json`.
type BuildInfo struct {
	Version   string `json:"version"`
	Commit    string `json:"commit,omitempty"`
	Date      string `json:"date,omitempty"`
	GoVersion string `json:"go"`
	Platform  string `json:"platform"`
}

// Info returns the build metadata of the running binary.
func Info() BuildInfo {
	return BuildInfo{
		Version:   Version,
		Commit:    Commit,
		Date:      Date,
		GoVersion: runtime.Version(),
		Platform:  runtime.GOOS + "/" + runtime.GOARCH,
	}
}

// String renders the build metadata on one line.
func (b BuildInfo) String() string {
	s := "pulsed " + b.Version
	if b.Commit != "" {
		s += " (" + b.Commit + ")"
	}
	if b.Date != "" {
		s += " built " + b.Date
	}
	return fmt.Sprintf("%s %s %s", s, b.GoVersion, b.Platform)
}
