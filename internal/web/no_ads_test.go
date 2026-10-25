package web

import (
	"strings"
	"testing"
)

// This fork carries no advertising: upstream's sponsor slots were cut out, and a later
// sync must not bring their routes back.
func TestRouterServesNoSponsorRoutes(t *testing.T) {
	for route := range registeredContractRoutes(t) {
		if strings.Contains(strings.ToLower(route), "sponsor") {
			t.Errorf("%s serves sponsor content", route)
		}
	}
}
