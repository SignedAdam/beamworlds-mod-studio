//go:build !production

package main

func debugBrowserArgs() []string {
	return []string{"--remote-debugging-port=9222"}
}
