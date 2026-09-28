//go:build !windows

package main

// systemLanguages: on Linux and macOS the terminal passes LANG/LC_* (read by
// detectLanguage); there is nothing more to ask.
func systemLanguages() []string { return nil }
