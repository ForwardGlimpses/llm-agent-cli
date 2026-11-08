//go:build !windows

package main

// initConsole is a no-op on terminals that already understand ANSI escapes.
func initConsole() {}
