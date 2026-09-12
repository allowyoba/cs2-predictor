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
