//go:build windows

package main

import "golang.org/x/sys/windows"

// systemLanguages returns the Windows display languages (for example "nl-NL"),
// because Windows terminals do not set LANG.
func systemLanguages() []string {
	langs, err := windows.GetUserPreferredUILanguages(windows.MUI_LANGUAGE_NAME)
	if err != nil {
		return nil
	}
	return langs
}
