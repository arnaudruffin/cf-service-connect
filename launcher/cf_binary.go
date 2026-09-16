package launcher

import (
	"fmt"
	"os"

	"github.com/phayes/freeport"
)

func getAvailablePort() int {
	return freeport.GetPort()
}

// cfBinaryNameEnvVar named the Cloud Foundry CLI binary that this plugin used
// to shell out to for `cf ssh` (see issue #70, which added it for Windows users
// with cf7/cf8-named binaries).
const cfBinaryNameEnvVar = "CF_BINARY_NAME"

// CFBinaryName returns the configured Cloud Foundry CLI binary name, defaulting
// to "cf".
//
// Deprecated: the SSH tunnel is now established in-process, so the plugin no
// longer invokes the cf binary and this setting has no effect. It is still read
// so that WarnIfCFBinaryNameSet can tell users their configuration is now inert
// instead of silently ignoring it.
func CFBinaryName() string {
	if name, exists := os.LookupEnv(cfBinaryNameEnvVar); exists && name != "" {
		return name
	}
	return "cf"
}

// WarnIfCFBinaryNameSet notifies the user when CF_BINARY_NAME is set, since it
// no longer has any effect. Setting it is harmless; silently ignoring it would
// not be, because a user who set it to work around a problem deserves to know
// that the workaround is no longer doing anything.
func WarnIfCFBinaryNameSet() {
	name, exists := os.LookupEnv(cfBinaryNameEnvVar)
	if !exists || name == "" {
		return
	}
	fmt.Printf(
		"Note: %s is set to %q but is no longer used. This plugin now creates the SSH tunnel itself instead of running `cf ssh`, so no cf binary is invoked.\n",
		cfBinaryNameEnvVar, name)
}
