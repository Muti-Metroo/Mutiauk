//go:build !windows

package route

// defaultRouteStateFile is where persisted route state lives on Unix.
var defaultRouteStateFile = "/var/lib/mutiauk/routes.json"
