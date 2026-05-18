//go:build !windows

package main

import "errors"

func runDesktop(url string) error {
	return errors.New("desktop mode is only supported on Windows")
}
