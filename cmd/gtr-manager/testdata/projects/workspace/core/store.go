// Package store is the core of the e2e workspace.
package store

import "greet"

// Driver stores values.
type Driver interface{ Name() string }

// Describe uses a dependency of the core.
func Describe(d Driver) string { return greet.Hello(d.Name()) }
