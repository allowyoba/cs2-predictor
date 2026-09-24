package httpapi

import (
	"encoding/json"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

// The page and the API are one product shipped as two files, and nothing
// in Go's type system connects them: the app reads `entry.first_team`, the
// DTO decides whether that key exists, and a rename on either side is a
// blank line on somebody's screen rather than a compile error.
//
// This is the test that catches it. It marshals the real DTOs, collects
// the keys they actually emit, and holds every field the page reads
// against that list — which is precisely how the history screen came to
// render empty team names.

// jsonKeys collects every key name in a marshalled payload, at any depth.
func jsonKeys(t *testing.T, value any) map[string]bool {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	keys := map[string]bool{}
	var walk func(any)
	walk = func(node any) {
		switch typed := node.(type) {
		case map[string]any:
			for key, child := range typed {
				keys[key] = true
				walk(child)
			}
		case []any:
			for _, child := range typed {
				walk(child)
			}
		}
	}
	walk(decoded)
	return keys
}

// appScript is the page's own code, with comments stripped: a field name
// mentioned in prose is not a field the page reads.
func appScript(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../../miniapp/static/app.js")
	if err != nil {
		t.Fatalf("read the app: %v — it ships inside the binary from internal/miniapp", err)
	}
	lines := strings.Split(string(raw), "\n")
	var kept []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "*") || strings.HasPrefix(trimmed, "/*") {
			continue
		}
		if idx := strings.Index(line, "//"); idx >= 0 {
			line = line[:idx]
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

// sampleDashboard and sampleHistory are filled far enough that every
// optional branch of the payload is present — an omitempty field that
// happens to be zero here would look like a key the page may not read.
func sampleDashboard() dashboardDTO {
	var body dashboardDTO
	body.User.ID = 1
	body.User.DisplayName = "Аня"
	body.User.PhotoURL = "https://example.invalid/a.jpg"
	body.Summary = summaryDTO{Predictions: 10, Correct: 7, Exact: 3, Accuracy: 70, Points: 12, Tournaments: 2}
	body.Form = formDTO{CurrentStreak: 2, BestStreak: 5, Recent: []bool{true, false}}
	body.Trend = &trendDTO{CurrentAccuracy: 70, PreviousAccuracy: 60, DeltaPP: 10, CurrentSample: 10, PreviousSample: 9}
	body.Games = []gameDTO{{Game: "CS2", Predictions: 8, Accuracy: 75, CurrentStreak: 2, Trend: body.Trend}}
	body.Chats = []chatRefDTO{{ID: -100, Title: "Прогнозы", Predictions: 8}}
	body.Best = []teamDTO{{Team: "G2", Game: "CS2", Accuracy: 80, Predictions: 5}}
	body.Worst = []teamDTO{{Team: "NAVI", Game: "CS2", Accuracy: 20, Predictions: 5}}
	body.Teams = []teamDTO{{Team: "MOUZ", Game: "CS2", Accuracy: 60, Predictions: 12}}
	body.Bias = []biasDTO{{
		Team: "NAVI", Game: "CS2", Matches: 18, PickRate: 78, WinRate: 57, Accuracy: 61, BiasPP: 21,
	}}
	return body
}

func sampleHistory() historyDTO {
	return historyDTO{Count: 1, Entries: []historyEntryDTO{{
		PlayedAt: time.Now(), Game: "CS2", Event: "IEM", Tier: "s", Chat: "Прогнозы",
		First: "G2", Second: "NAVI", Predicted: "2:0", Actual: "2:1",
		Correct: true, Points: 1, Chats: 1,
	}}}
}

func sampleActive() activeEntryDTO {
	starts := time.Now()
	return activeEntryDTO{
		Game: "CS2", Event: "IEM", Chat: "Прогнозы", First: "G2", Second: "NAVI",
		Predicted: "2:0", StartsAt: &starts, ClosesAt: starts, Stream: "https://example.invalid", Live: true,
	}
}

func sampleChats() chatsDTO {
	return chatsDTO{
		Count: 1,
		Chats: []chatStandingDTO{{
			ID: -100, Chat: "Прогнозы", Predictions: 8, Accuracy: 75,
			Points: 12, Tournaments: 2, Gold: 1, Silver: 1, Bronze: 1,
		}},
		Medals: []medalDTO{{Place: 1, Event: "IEM", Chat: "Прогнозы", Game: "CS2", AwardedAt: time.Now()}},
	}
}

func sampleSettings() settingsDTO {
	return settingsDTO{
		Personal: personalSettingsDTO{
			Locale: "RU", Timezone: "Europe/Moscow", Nickname: "Аня",
			Notify: []switchDTO{{Kind: "recaps", On: true}},
		},
		Chats: []chatSettingsDTO{{
			ID: -100, Title: "Прогнозы", Locale: "RU", Timezone: "Europe/Moscow",
			StreamLanguage: "RU", TopTierOnly: true, PreferHLTVFlag: true,
			QuietFrom: 60, QuietTo: 480, CanManage: true,
			Games:  []gameToggleDTO{{Game: "CS2", Enabled: true, AutoSubscribe: true}},
			Notify: []switchDTO{{Kind: "digests", On: true}},
		}},
	}
}

func sampleResults() resultsDTO {
	var body resultsDTO
	body.Period.Key, body.Period.Label = "month:2026-03", "Март 2026"
	body.Summary = summaryDTO{Predictions: 5, Correct: 4, Exact: 1, Accuracy: 80, Points: 12, Tournaments: 1}
	previous := summaryDTO{Predictions: 4, Correct: 2, Exact: 0, Accuracy: 50, Points: 4, Tournaments: 1}
	body.Previous = &previous
	delta := 30
	body.DeltaPP = &delta
	body.Chats = []resultsChatDTO{{ID: -100, Title: "Прогнозы", Predictions: 5, Accuracy: 80, Points: 12}}
	body.AvailableMonths = []periodOptionDTO{{Key: "month:2026-03", Label: "Март 2026"}}
	body.AvailableYears = []periodOptionDTO{{Key: "year:2026", Label: "2026"}}
	return body
}

func sampleSegments() segmentsDTO {
	return segmentsDTO{
		Total:        120,
		Heatmap:      []heatCellDTO{{Format: "BO3", Stage: "playoff", Accuracy: 81, Predictions: 38, DeltaPP: 10}},
		Mistakes:     []mistakeDTO{{Tag: "bo1", Count: 6}},
		MistakeTotal: 42,
		Fingerprint:  []axisDTO{{Axis: "consistency", Score: 88, Sample: 120, Meaningful: true}},
		Opponents:    []segmentDTO{{Key: "even", Accuracy: 57, Predictions: 61, DeltaPP: -3}},
		Ranked:       61,
	}
}

func TestMiniappContract_ThePageOnlyReadsFieldsTheAPIEmits(t *testing.T) {
	script := appScript(t)
	emitted := jsonKeys(t, sampleDashboard())
	for key := range jsonKeys(t, sampleHistory()) {
		emitted[key] = true
	}
	// The active, chats and medal payloads feed the same page too.
	for key := range jsonKeys(t, activeDTO{Count: 1, Entries: []activeEntryDTO{sampleActive()}}) {
		emitted[key] = true
	}
	for key := range jsonKeys(t, sampleChats()) {
		emitted[key] = true
	}
	for key := range jsonKeys(t, sampleSettings()) {
		emitted[key] = true
	}
	for key := range jsonKeys(t, sampleSegments()) {
		emitted[key] = true
	}
	for key := range jsonKeys(t, sampleResults()) {
		emitted[key] = true
	}
	// The public team endpoint feeds the same page.
	for key := range jsonKeys(t, teamsResponse{Teams: []miniappTeam{{
		ID: "1", Name: "G2", Location: "EU", Logo: "u", LogoProvider: "u", LogoHLTV: "u", Chip: "dark",
	}}}) {
		emitted[key] = true
	}
	// The two screens that are not about the past.
	// starts_at is omitempty and legitimately absent for an unscheduled
	// match, so the sample carries one — otherwise this check would call a
	// field the API does emit a mistake.
	startsAt := time.Now()
	for key := range jsonKeys(t, activeDTO{Entries: []activeEntryDTO{{
		Game: "CS2", Event: "IEM", Chat: "c", First: "G2", Second: "NAVI",
		Predicted: "2:0", StartsAt: &startsAt, ClosesAt: time.Now(), Stream: "u", Live: true,
	}}}) {
		emitted[key] = true
	}
	for key := range jsonKeys(t, chatsDTO{Chats: []chatStandingDTO{{
		Chat: "c", Predictions: 1, Accuracy: 1, Points: 1, Tournaments: 1, Gold: 1, Silver: 1, Bronze: 1,
	}}}) {
		emitted[key] = true
	}

	// The cross-chat leaderboard: chat-level totals only, no member row.
	for key := range jsonKeys(t, chatLeaderboardDTO{Chats: []chatStandingRowDTO{{
		ID: -100, Chat: "c", Rank: 1, Points: 1, Accuracy: 1, Predictions: 1, Participants: 1,
	}}}) {
		emitted[key] = true
	}

	// Access is the one payload the page reads before it has anything else.
	for key := range jsonKeys(t, accessDTO{Status: "PENDING", Operator: false}) {
		emitted[key] = true
	}

	// Every property read off an API object. The receivers are the names
	// the page gives those objects.
	pattern := regexp.MustCompile(`\b(?:entry|data|summary|user|team|body|game|chat)\.([a-z][a-z_]*)\b`)
	// Properties of our own JavaScript objects rather than of an API
	// payload: listed explicitly so the check stays about the contract.
	local := map[string]bool{
		"append": true, "style": true, "replace": true, "length": true, "map": true,
		"filter": true, "value": true, "code": true, "label": true, "dataset": true,
		"push": true, "slice": true, "sort": true, "reduce": true, "entries": true,
		"teams": true, "games": true, "access": true, "classList": true, "hidden": true,
		"textContent": true, "querySelector": true, "querySelectorAll": true,
		"addEventListener": true, "toString": true,
	}

	var missing []string
	for _, match := range pattern.FindAllStringSubmatch(script, -1) {
		field := match[1]
		if local[field] || emitted[field] {
			continue
		}
		missing = append(missing, match[0])
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("the page reads fields the API does not emit: %v\n"+
			"Either the DTO's json tag or the page is wrong — this is how the history screen "+
			"rendered blank team names.", uniqueStrings(missing))
	}
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, value := range values {
		if seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}
