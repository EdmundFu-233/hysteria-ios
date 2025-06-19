//go:build none
// +build none

// This file is a build-time workaround for gomobile dependency issues.
// It is not part of the application logic and is excluded from builds
// by the build tags above. Its purpose is to force the Go toolchain
// to include necessary gomobile packages in the module's dependency graph.

package libhysteria

import (
	_ "golang.org/x/mobile/bind"
	_ "golang.org/x/mobile/bind/objc"
)
