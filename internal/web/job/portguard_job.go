package job

import (
	"context"

	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

// PortGuardJob rolls back a change of the closed ports that nobody confirmed in time.
type PortGuardJob struct{}

// NewPortGuardJob creates a new port guard job instance.
func NewPortGuardJob() *PortGuardJob {
	return new(PortGuardJob)
}

// Run costs one read of a setting when nothing waits to be confirmed.
func (j *PortGuardJob) Run() {
	service.PortGuard().Tick(context.Background())
}
