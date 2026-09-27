//go:build windows

package agentinstall

var lookupPasswdShell = func(string) string { return "" }
