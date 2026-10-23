//go:build !linux

package store

func checkLocalFS(string) error { return nil }
