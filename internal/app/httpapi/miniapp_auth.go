package httpapi

import (
	"net/http"

	"cs2predictor/internal/domain/chat"
	"cs2predictor/internal/platform/common"
)

// The gate in front of every personal Mini App endpoint.
//
// Two checks, in this order, because they answer different questions and
// only one of them is about trust:
//
//  1. Did Telegram sign this launch, and is it recent? (VerifyInitData)
//  2. Is this person allowed in at all? (the access grant)
//
// A failure of the first is 401 and says nothing else: the caller has not
// proven who they are, so they are owed no information about what exists
// behind it. A failure of the second is 403 with the standing, because by
// then the caller has proven who they are and "you have not been granted
// access yet" is precisely what they need to know.

// miniAppHandler is a handler that runs with a verified user.
type miniAppHandler func(w http.ResponseWriter, r *http.Request, user authenticatedUser)

// withMiniAppAuth verifies the launch and requires an open door.
func withMiniAppAuth(deps MiniAppDeps, next miniAppHandler) http.Handler {
	return authenticate(deps, true, next)
}

// withMiniAppAuthAllowingClosed verifies the launch but lets somebody
// without access through — the endpoints about access itself, which
// otherwise could never be reached by the people who need them.
func withMiniAppAuthAllowingClosed(deps MiniAppDeps, next miniAppHandler) http.Handler {
	return authenticate(deps, false, next)
}

func authenticate(deps MiniAppDeps, requireAccess bool, next miniAppHandler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if deps.BotToken == "" || deps.Stats == nil || deps.Access == nil || deps.Clock == nil {
			http.Error(w, errMiniAppUnavailable.Error(), http.StatusServiceUnavailable)
			return
		}
		raw := initDataFromRequest(r.Header.Get("Authorization"), r.URL.Query().Get("init_data"))
		profile, err := VerifyInitData(raw, deps.BotToken, deps.Clock.Now())
		if err != nil {
			// No detail, no logging of the payload: a rejected launch is
			// somebody's credentials-shaped string, and neither the
			// response nor the log is a place to keep it.
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		user := authenticatedUser{ID: common.UserID{Value: profile.ID}, Profile: profile}

		if requireAccess {
			status, operator, err := miniAppStatus(r.Context(), deps, user.ID)
			if err != nil {
				miniAppError(w, deps.Log, "access lookup failed", err)
				return
			}
			if !operator && status != chat.MiniAppGranted {
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.WriteHeader(http.StatusForbidden)
				writeJSONBody(w, accessDTO{Status: string(status), Operator: false})
				if deps.Log != nil {
					deps.Log.Info("mini app access refused", "who", user.describeForLog(), "status", status)
				}
				return
			}
		}
		next(w, r, user)
	})
}
