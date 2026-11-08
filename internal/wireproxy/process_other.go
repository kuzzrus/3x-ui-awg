//go:build !linux

package wireproxy

import "os/exec"

// stopGroupOnCancel does nothing off Linux: warpwp drives systemd, so the scripts never run anywhere else.
func stopGroupOnCancel(_ *exec.Cmd) (finished func()) { return func() {} }
