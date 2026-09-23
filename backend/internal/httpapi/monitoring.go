package httpapi

import (
	"errors"
	"net/http"
	"time"

	"wgpanel-api/internal/store"
)

type usageSampleResponse struct {
	Bucket  string `json:"bucket"`
	RxBytes int64  `json:"rx_bytes"`
	TxBytes int64  `json:"tx_bytes"`
}

// handleGetAccountUsage serves the account detail usage-over-time chart
// (docs/STORY-10-monitoring-and-domain-management.md, docs/PRD-monitoring-stats.md
// §6.3). Defaults to the last 7 days bucketed hourly - raw samples are only
// retained 7 days (see migration 0011's retention policy), so a wider default range
// would just return an incomplete series with no indication why.
func (s *Server) handleGetAccountUsage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	identity, _ := callerIdentityFromContext(ctx)
	ns := callerNamespaceArg(identity)

	if _, err := s.Store.GetAccount(ctx, id, ns); errors.Is(err, store.ErrAccountNotFound) {
		writeJSONError(w, http.StatusNotFound, "account_not_found", "no account with that id")
		return
	} else if err != nil {
		s.Logger.Error("get_account_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not fetch account")
		return
	}

	bucket := r.URL.Query().Get("bucket")
	if bucket != "day" {
		bucket = "hour"
	}
	to := time.Now()
	from := to.Add(-7 * 24 * time.Hour)
	if v := r.URL.Query().Get("from"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			from = t
		} else {
			writeJSONError(w, http.StatusBadRequest, "invalid_request", "from must be RFC3339")
			return
		}
	}
	if v := r.URL.Query().Get("to"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			to = t
		} else {
			writeJSONError(w, http.StatusBadRequest, "invalid_request", "to must be RFC3339")
			return
		}
	}

	samples, err := s.Store.AccountUsageSeries(ctx, id, bucket, from, to)
	if err != nil {
		s.Logger.Error("account_usage_series_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not fetch usage series")
		return
	}

	out := make([]usageSampleResponse, 0, len(samples))
	for _, sm := range samples {
		out = append(out, usageSampleResponse{
			Bucket:  sm.Bucket.Format(time.RFC3339),
			RxBytes: sm.RxBytes,
			TxBytes: sm.TxBytes,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"bucket": bucket, "samples": out})
}

type metricsSampleResponse struct {
	Bucket        string   `json:"bucket"`
	CPUPercent    *float32 `json:"cpu_percent"`
	MemUsedBytes  *int64   `json:"mem_used_bytes"`
	MemTotalBytes *int64   `json:"mem_total_bytes"`
}

// handleGetNodeMetrics serves the Nodes page's CPU/RAM history chart. Defaults to
// the last 24 hours - node_metrics is retained 30 days (migration 0011), so a wider
// range is available via ?from=/?to= if needed.
func (s *Server) handleGetNodeMetrics(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")

	if _, err := s.Store.GetNode(ctx, id); errors.Is(err, store.ErrNodeNotFound) {
		writeJSONError(w, http.StatusNotFound, "node_not_found", "no node with that id")
		return
	} else if err != nil {
		s.Logger.Error("get_node_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not fetch node")
		return
	}

	to := time.Now()
	from := to.Add(-24 * time.Hour)
	if v := r.URL.Query().Get("from"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			from = t
		} else {
			writeJSONError(w, http.StatusBadRequest, "invalid_request", "from must be RFC3339")
			return
		}
	}
	if v := r.URL.Query().Get("to"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			to = t
		} else {
			writeJSONError(w, http.StatusBadRequest, "invalid_request", "to must be RFC3339")
			return
		}
	}

	samples, err := s.Store.NodeMetricsSeries(ctx, id, from, to)
	if err != nil {
		s.Logger.Error("node_metrics_series_failed", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not fetch metrics series")
		return
	}

	out := make([]metricsSampleResponse, 0, len(samples))
	for _, sm := range samples {
		out = append(out, metricsSampleResponse{
			Bucket:        sm.Bucket.Format(time.RFC3339),
			CPUPercent:    sm.CPUPercent,
			MemUsedBytes:  sm.MemUsedBytes,
			MemTotalBytes: sm.MemTotalBytes,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"samples": out})
}
