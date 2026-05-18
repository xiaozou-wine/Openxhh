//go:build windows

package main

import webview "github.com/webview/webview_go"

func runDesktop(url string) error {
	window := webview.New(false)
	defer window.Destroy()
	window.SetTitle("Openxhh 控制台")
	window.SetSize(1180, 780, webview.HintNone)
	window.Navigate(url)
	window.Run()
	return nil
}
