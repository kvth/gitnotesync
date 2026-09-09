//go:build !unix

package cmd

// sdNotify does nothing where there is no service manager to notify.
func sdNotify(string) {}
