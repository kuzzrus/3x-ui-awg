package job

import (
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service/integration"
)

// dueScanner is what the job asks of the Psiphon service: to start the weekly scan when it is due.
type dueScanner interface {
	ScanIfDue(now time.Time) (bool, error)
}

// PsiphonScanJob starts the scan of every Psiphon region by itself once a week, when the admin has switched that on. It runs
// often and the service decides, so a panel that was down at the hour a scan came due starts it when it is up again.
type PsiphonScanJob struct {
	psiphon dueScanner
	now     func() time.Time
}

// NewPsiphonScanJob creates the job for the Psiphon service of the panel.
func NewPsiphonScanJob() *PsiphonScanJob {
	return &PsiphonScanJob{psiphon: &integration.PsiphonService{}, now: time.Now}
}

// Run asks the service to start the weekly scan if it is due. A scan that cannot start (Psiphon is not installed, another scan
// runs) is no failure, and the next run tries again.
func (j *PsiphonScanJob) Run() {
	started, err := j.psiphon.ScanIfDue(j.now())
	if err != nil {
		logger.Debug("psiphon scan job:", err)
		return
	}
	if started {
		logger.Info("psiphon: the weekly scan of every region has started")
	}
}
