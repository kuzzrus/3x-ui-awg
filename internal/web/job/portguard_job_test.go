package job

import (
	"context"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/portguard"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

type nothingClosed struct{}

func (nothingClosed) Load() (portguard.State, error) { return portguard.State{}, nil }
func (nothingClosed) Save(portguard.State) error     { return nil }

type noFirewall struct{}

func (noFirewall) Apply(context.Context, []portguard.Port) error { return nil }
func (noFirewall) Remove(context.Context) error                  { return nil }

type countingOpener struct{ swept int }

func (c *countingOpener) Open(context.Context, int) (func(), error) { return func() {}, nil }
func (c *countingOpener) Sweep(context.Context)                     { c.swept++ }

// The job that watches the lease of the closed ports also takes out the rule that a certificate round left in ufw.
func TestPortGuardJobSweepsWhatACertificateRoundLeftInTheFirewall(t *testing.T) {
	service.SetPortGuard(portguard.New(nothingClosed{}, noFirewall{}, nil))
	opener := &countingOpener{}
	service.SetAcmeFirewall(opener)
	t.Cleanup(func() {
		service.SetPortGuard(nil)
		service.SetAcmeFirewall(nil)
	})

	NewPortGuardJob().Run()
	NewPortGuardJob().Run()
	if opener.swept != 2 {
		t.Fatalf("the sweep ran %d times in two runs of the job, want 2", opener.swept)
	}
}
