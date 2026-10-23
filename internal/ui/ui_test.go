package ui

import (
	"io/fs"
	"testing"
)

func TestFSMatchesBuildState(t *testing.T) {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		t.Fatal(err)
	}
	_, statErr := fs.Stat(sub, "index.html")
	if got := FS(); (got != nil) != (statErr == nil) {
		t.Fatalf("FS() nil=%v but index.html present=%v", got == nil, statErr == nil)
	}
}
