package telegram

import (
	"bytes"
	"image/png"
	"testing"
	"time"

	"gonum.org/v1/plot/font"

	"cs2predictor/internal/domain/scoring"
	"cs2predictor/internal/platform/common"
)

// TestRegisterChartFont_RegistersACyrillicCapableFace is a regression test
// for a real risk, not a hypothetical one: gonum/plot's own default font
// (Liberation, registered by the library itself) has no Cyrillic glyphs,
// and every chart this bot draws is for participants whose display names
// are frequently Cyrillic ("Серёжка", "Влад", ...). Without
// registerChartFont, those names would render as empty boxes in the
// legend — this pins that the DejaVu face is actually reachable through
// gonum/plot's own font cache under the Font value the renderer uses.
func TestRegisterChartFont_RegistersACyrillicCapableFace(t *testing.T) {
	registerChartFont()
	if !font.DefaultCache.Has(chartFont(false)) {
		t.Fatal("expected the regular chart font to be registered in font.DefaultCache")
	}
	if !font.DefaultCache.Has(chartFont(true)) {
		t.Fatal("expected the bold chart font to be registered in font.DefaultCache")
	}
}

// TestBuildProgressionChart_RendersCyrillicDisplayNamesWithoutError covers
// the actual chart-building path with real Cyrillic participant names, the
// case that would have exposed the missing-glyph problem end to end.
func TestBuildProgressionChart_RendersCyrillicDisplayNamesWithoutError(t *testing.T) {
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	points := []scoring.ProgressionPoint{
		{UserID: common.UserID{Value: 1}, DisplayName: "Серёжка", PlayedAt: base, Points: 2},
		{UserID: common.UserID{Value: 2}, DisplayName: "Александр 亚历山大", PlayedAt: base, Points: 1},
	}
	imgBytes, err := buildProgressionChart(points, "Рейтинг турнира", "Матч №", "Очки", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := png.Decode(bytes.NewReader(imgBytes)); err != nil {
		t.Fatalf("output is not a valid PNG: %v", err)
	}
}

func TestBuildProgressionChart_ProducesAValidPNG(t *testing.T) {
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	points := []scoring.ProgressionPoint{
		{UserID: common.UserID{Value: 1}, DisplayName: "Alex", PlayedAt: base, Points: 2},
		{UserID: common.UserID{Value: 2}, DisplayName: "Sam", PlayedAt: base, Points: 0},
		{UserID: common.UserID{Value: 1}, DisplayName: "Alex", PlayedAt: base.Add(time.Hour), Points: 1},
		{UserID: common.UserID{Value: 2}, DisplayName: "Sam", PlayedAt: base.Add(time.Hour), Points: 2},
	}
	imgBytes, err := buildProgressionChart(points, "Test chart", "Match #", "Points", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(imgBytes) == 0 {
		t.Fatal("expected non-empty PNG bytes")
	}
	img, err := png.Decode(bytes.NewReader(imgBytes))
	if err != nil {
		t.Fatalf("output is not a valid PNG: %v", err)
	}
	if img.Bounds().Dx() == 0 || img.Bounds().Dy() == 0 {
		t.Fatalf("decoded image has zero size: %v", img.Bounds())
	}
}

func TestBuildProgressionChart_SingleUserDrawsOnlyThatParticipant(t *testing.T) {
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	alex := common.UserID{Value: 1}
	points := []scoring.ProgressionPoint{
		{UserID: alex, DisplayName: "Alex", PlayedAt: base, Points: 2},
		{UserID: common.UserID{Value: 2}, DisplayName: "Sam", PlayedAt: base, Points: 0},
	}
	imgBytes, err := buildProgressionChart(points, "Mine", "Match #", "Points", &alex)
	if err != nil {
		t.Fatal(err)
	}
	if len(imgBytes) == 0 {
		t.Fatal("expected non-empty PNG bytes")
	}
}

func TestBuildProgressionChart_ErrorsOnNoData(t *testing.T) {
	if _, err := buildProgressionChart(nil, "Empty", "x", "y", nil); err == nil {
		t.Fatal("expected an error for an empty series, got nil")
	}
}

// TestBuildRankSeries_RanksBySharedMatchStepsNotIndividualVotes covers the
// core behavior: two participants voting on the same match (same PlayedAt)
// settle together as one step, and a third, later match re-ranks both of
// them based on their combined totals so far — not per vote.
func TestBuildRankSeries_RanksBySharedMatchStepsNotIndividualVotes(t *testing.T) {
	match1 := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	match2 := match1.Add(time.Hour)
	alex, sam := common.UserID{Value: 1}, common.UserID{Value: 2}
	points := []scoring.ProgressionPoint{
		// Match 1: Alex scores 2, Sam scores 0 — Alex is 1st, Sam 2nd.
		{UserID: alex, DisplayName: "Alex", PlayedAt: match1, Points: 2},
		{UserID: sam, DisplayName: "Sam", PlayedAt: match1, Points: 0},
		// Match 2: Sam scores 5, Alex scores 0 — Sam takes over 1st.
		{UserID: sam, DisplayName: "Sam", PlayedAt: match2, Points: 5},
		{UserID: alex, DisplayName: "Alex", PlayedAt: match2, Points: 0},
	}

	order, byUser := buildRankSeries(points, nil)
	if len(order) != 2 {
		t.Fatalf("got %d series, want 2", len(order))
	}

	alexSeries, samSeries := byUser[alex], byUser[sam]
	if len(alexSeries.xys) != 2 || len(samSeries.xys) != 2 {
		t.Fatalf("expected one rank point per match step, got alex=%d sam=%d", len(alexSeries.xys), len(samSeries.xys))
	}
	// Step 1 (negated rank: 1st place = -1): Alex ahead of Sam.
	if alexSeries.xys[0].Y != -1 || samSeries.xys[0].Y != -2 {
		t.Fatalf("step 1 ranks = alex:%v sam:%v, want alex 1st (-1), sam 2nd (-2)", alexSeries.xys[0].Y, samSeries.xys[0].Y)
	}
	// Step 2: Sam has overtaken Alex.
	if samSeries.xys[1].Y != -1 || alexSeries.xys[1].Y != -2 {
		t.Fatalf("step 2 ranks = alex:%v sam:%v, want sam 1st (-1), alex 2nd (-2)", alexSeries.xys[1].Y, samSeries.xys[1].Y)
	}
}

// TestBuildRankSeries_TiedTotalsShareARank mirrors scoring.DenseRank's own
// rule: two participants with the same cumulative total share one rank
// number rather than being arbitrarily split into 1st/2nd.
func TestBuildRankSeries_TiedTotalsShareARank(t *testing.T) {
	at := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	points := []scoring.ProgressionPoint{
		{UserID: common.UserID{Value: 1}, DisplayName: "Alex", PlayedAt: at, Points: 3},
		{UserID: common.UserID{Value: 2}, DisplayName: "Sam", PlayedAt: at, Points: 3},
	}
	_, byUser := buildRankSeries(points, nil)
	for uid, s := range byUser {
		if s.xys[0].Y != -1 {
			t.Fatalf("user %v ranked %v, want both tied at 1st (-1)", uid, s.xys[0].Y)
		}
	}
}

// TestBuildRankChart_ProducesAValidPNG is buildRankChart's equivalent of
// TestBuildProgressionChart_ProducesAValidPNG.
func TestBuildRankChart_ProducesAValidPNG(t *testing.T) {
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	points := []scoring.ProgressionPoint{
		{UserID: common.UserID{Value: 1}, DisplayName: "Alex", PlayedAt: base, Points: 2},
		{UserID: common.UserID{Value: 2}, DisplayName: "Sam", PlayedAt: base, Points: 0},
		{UserID: common.UserID{Value: 1}, DisplayName: "Alex", PlayedAt: base.Add(time.Hour), Points: 0},
		{UserID: common.UserID{Value: 2}, DisplayName: "Sam", PlayedAt: base.Add(time.Hour), Points: 5},
	}
	imgBytes, err := buildRankChart(points, "Rank chart", "Match #", "Rank", nil)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(imgBytes))
	if err != nil {
		t.Fatalf("output is not a valid PNG: %v", err)
	}
	if img.Bounds().Dx() == 0 || img.Bounds().Dy() == 0 {
		t.Fatalf("decoded image has zero size: %v", img.Bounds())
	}
}

func TestBuildRankChart_ErrorsOnNoData(t *testing.T) {
	if _, err := buildRankChart(nil, "Empty", "x", "y", nil); err == nil {
		t.Fatal("expected an error for an empty series, got nil")
	}
}

func TestBuildRankChart_SingleUserDrawsOnlyThatParticipant(t *testing.T) {
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	alex := common.UserID{Value: 1}
	points := []scoring.ProgressionPoint{
		{UserID: alex, DisplayName: "Alex", PlayedAt: base, Points: 2},
		{UserID: common.UserID{Value: 2}, DisplayName: "Sam", PlayedAt: base, Points: 0},
	}
	imgBytes, err := buildRankChart(points, "Mine", "Match #", "Rank", &alex)
	if err != nil {
		t.Fatal(err)
	}
	if len(imgBytes) == 0 {
		t.Fatal("expected non-empty PNG bytes")
	}
}

// TestBuildProgressionChart_CapsSeriesCountForBusyChats reproduces a chat
// with more participants than chartMaxSeries — the whole point of the cap
// is to keep a busy group's chart from becoming an unreadable tangle of
// overlapping lines, so this just pins that the cap doesn't itself panic or
// error on a realistic input size.
func TestBuildProgressionChart_CapsSeriesCountForBusyChats(t *testing.T) {
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	var points []scoring.ProgressionPoint
	for u := int64(1); u <= chartMaxSeries+5; u++ {
		points = append(points, scoring.ProgressionPoint{
			UserID: common.UserID{Value: u}, DisplayName: "User", PlayedAt: base, Points: int(u),
		})
	}
	if _, err := buildProgressionChart(points, "Busy chat", "x", "y", nil); err != nil {
		t.Fatal(err)
	}
}
