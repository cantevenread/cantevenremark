//go:build !darwin

package main

import "errors"

const nativeSupported = false

type nativeApp struct{}

func newNativeApp(onOpen func([]string), onTerminate func()) (*nativeApp, error) {
	return nil, errors.New("the native window is only available on macOS")
}

func (a *nativeApp) Run(url string)                  {}
func (a *nativeApp) PickFiles() []string             { return nil }
func (a *nativeApp) PickFolder(prompt string) string { return "" }
func (a *nativeApp) Quit()                           {}
