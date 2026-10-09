//go:build !windows

package link

import "errors"

func junction(string, string) error { return errors.New("junctions exist only on Windows") }
