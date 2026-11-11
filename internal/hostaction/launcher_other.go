//go:build !linux

package hostaction

import (
	"context"
	"errors"
)

var errUnsupported = errors.New("hostaction: running commands that change the host needs Linux")

var readOnlyUsr = func() bool { return false }

func systemdUsable() bool { return false }

type directLauncher struct{}

func (directLauncher) start(context.Context, *run) error { return errUnsupported }
func (directLauncher) alive(*run) bool                   { return false }
func (directLauncher) stop(*run)                         {}
func (directLauncher) release(*run)                      {}

type unitLauncher struct{}

func (unitLauncher) start(context.Context, *run) error { return errSystemdUnusable }
func (unitLauncher) alive(*run) bool                   { return false }
func (unitLauncher) stop(*run)                         {}
func (unitLauncher) release(*run)                      {}
