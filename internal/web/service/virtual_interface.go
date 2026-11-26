package service

import "github.com/mhsanaei/3x-ui/v3/internal/util/sys"

// isVirtualInterface keeps the name upstream's network traffic code calls; the list itself lives in
// util/sys, which the node agent shares.
func isVirtualInterface(name string) bool {
	return sys.IsVirtualInterface(name)
}
