// Package slow is a driver of the e2e workspace.
package slow

// Driver is the slow driver.
type Driver struct{}

// Name names it.
func (Driver) Name() string { return "slow" }
