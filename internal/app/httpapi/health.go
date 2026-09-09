package httpapi

import (
	"context"
	"encoding/json"
	"net/http"

	"cs2predictor/internal/app"
	"cs2predictor/internal/domain/enrichment"
)

// DBPinger is satisfied by *pgxpool.Pool — kept as a narrow interface here
// so this package doesn't need to import the postgres adapter just for a
// health check.
type DBPinger interface {
	Ping(ctx context.Context) error
}

// livenessHandler always reports healthy once the process is up: process
// alive, no external dependency checked. That is what the image's own
// HEALTHCHECK and the development stack poll. A deploy waits on
// readinessHandler instead, since it needs to know migrations ran.
func livenessHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "UP"})
	}
}

// readinessHandler reports DOWN if the database is unreachable, and
// additionally reports the competition-providers health indicator so
// orchestrators can tell a healthy-but-stale-data instance apart from one
// that's simply still starting. A nil pool skips the DB check (used only
// by tests that don't wire one).
//
// enrichmentState/enrichmentSources report each enabled enrichment
// provider's (Valve VRS, ...) sync status for observability only: an
// optional enrichment source being down must never make the whole
// instance report unready, so nothing here touches overall/dbStatus.
func readinessHandler(gateway *app.CompetitionProviderGateway, pool DBPinger, enrichmentState enrichment.SyncStateRepository, enrichmentSources []enrichment.Source) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		status, details := gateway.Health()
		components := map[string]any{
			"competitionProviders": map[string]any{"status": string(status), "details": details},
		}

		overall := "UP"
		if status == app.HealthDown {
			overall = "DOWN"
		}

		if pool != nil {
			dbStatus := "UP"
			dbDetails := map[string]any{}
			if err := pool.Ping(r.Context()); err != nil {
				dbStatus = "DOWN"
				overall = "DOWN"
				dbDetails["error"] = err.Error()
			}
			components["database"] = map[string]any{"status": dbStatus, "details": dbDetails}
		}

		if enrichmentState != nil && len(enrichmentSources) > 0 {
			enrichmentComponents := map[string]any{}
			for _, source := range enrichmentSources {
				st, err := enrichmentState.State(r.Context(), source)
				if err != nil {
					enrichmentComponents[string(source)] = map[string]any{"status": "UNKNOWN", "details": map[string]any{"error": err.Error()}}
					continue
				}
				enrichmentComponents[string(source)] = map[string]any{"status": "UP", "details": map[string]any{
					"lastSuccessAt":       st.LastSuccessAt,
					"lastErrorAt":         st.LastErrorAt,
					"lastError":           st.LastError,
					"consecutiveFailures": st.ConsecutiveFailures,
				}}
			}
			components["enrichment"] = enrichmentComponents
		}

		w.Header().Set("Content-Type", "application/json")
		if overall == "DOWN" {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": overall, "components": components})
	}
}

// versionHandler reports build metadata — useful for confirming which
// image/commit is actually running in a given environment.
func versionHandler(version, commit, buildTime string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"version": version, "commit": commit, "buildTime": buildTime,
		})
	}
}
