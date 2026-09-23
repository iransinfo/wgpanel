package httpapi

import (
	"context"
	"time"
)

// heartbeatStaleAfter matches the 30s figure in docs/PRD-node-management.md §6.1/§7.
const heartbeatStaleAfter = 30 * time.Second

// deviceRetention is how long an account_devices row survives without a new sighting
// before the sweep prunes it - long enough for "recently seen devices" to be useful
// in the account detail view, bounded so the table can't grow forever from NAT/roaming
// endpoint churn.
const deviceRetention = 30 * 24 * time.Hour

// deviceSweepEveryNTicks runs the device prune on a much lower cadence than the 5s
// offline sweep - retention is measured in days, so once every ~5 minutes is plenty.
const deviceSweepEveryNTicks = 60

// RunOfflineSweepLoop periodically flips nodes whose heartbeat has gone stale to
// offline, and (at a much lower cadence) prunes stale device-sighting rows. Runs
// until ctx is cancelled - started as a goroutine from cmd/api/main.go alongside the
// two HTTP servers.
func (s *Server) RunOfflineSweepLoop(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	tick := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n, err := s.Store.SweepOfflineNodes(ctx, heartbeatStaleAfter)
			if err != nil {
				s.Logger.Error("offline_sweep_failed", "error", err)
				continue
			}
			if n > 0 {
				s.Logger.Info("offline_sweep", "nodes_marked_offline", n)
			}

			tick++
			if tick%deviceSweepEveryNTicks == 0 {
				pruned, err := s.Store.PruneStaleDevices(ctx, deviceRetention)
				if err != nil {
					s.Logger.Error("device_prune_failed", "error", err)
					continue
				}
				if pruned > 0 {
					s.Logger.Info("device_prune", "devices_pruned", pruned)
				}
			}
		}
	}
}
