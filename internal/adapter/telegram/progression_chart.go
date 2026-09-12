package telegram

import (
	"bytes"
	"errors"
	"fmt"
	"image/color"
	"math"
	"sort"
	"strconv"
	"sync"

	stdfnt "golang.org/x/image/font"
	"golang.org/x/image/font/opentype"

	"codeberg.org/go-fonts/dejavu/dejavusans"
	"codeberg.org/go-fonts/dejavu/dejavusansbold"

	"gonum.org/v1/plot"
	"gonum.org/v1/plot/font"
	"gonum.org/v1/plot/plotter"
	plottext "gonum.org/v1/plot/text"
	"gonum.org/v1/plot/vg"
	"gonum.org/v1/plot/vg/draw"

	"cs2predictor/internal/domain/scoring"
	"cs2predictor/internal/platform/common"
)

// chartTypeface names the font registered below — gonum/plot's own default
// (Liberation, registered by the library itself) has no Cyrillic glyphs at
// all, and every chart this bot draws is for a Russian-first audience:
// participant display names ("Серёжка", "Влад", ...) are frequently
// Cyrillic. Without this, those names would render as empty boxes in the
// one place — the legend — where getting them right matters most.
const chartTypeface font.Typeface = "DejaVu"

var registerChartFontOnce sync.Once

// registerChartFont adds DejaVu Sans (regular and bold) to gonum/plot's
// global font cache. DejaVu was chosen over hand-rolling glyph coverage
// checks because it's already an indirect dependency of gonum/plot's own
// module graph (pulled in transitively) and has broad Cyrillic coverage.
func registerChartFont() {
	registerChartFontOnce.Do(func() {
		regular, err := opentype.Parse(dejavusans.TTF)
		if err != nil {
			panic(fmt.Errorf("chart: parse DejaVu Sans regular: %w", err))
		}
		bold, err := opentype.Parse(dejavusansbold.TTF)
		if err != nil {
			panic(fmt.Errorf("chart: parse DejaVu Sans bold: %w", err))
		}
		font.DefaultCache.Add(font.Collection{
			{Font: font.Font{Typeface: chartTypeface}, Face: regular},
			{Font: font.Font{Typeface: chartTypeface, Weight: stdfnt.WeightBold}, Face: bold},
		})
	})
}

func chartFont(bold bool) font.Font {
	f := font.Font{Typeface: chartTypeface}
	if bold {
		f.Weight = stdfnt.WeightBold
	}
	return f
}

// chartMaxSeries bounds how many participants' lines a group chart ever
// draws. A busy chat can easily have fifteen or more voters; plotting all
// of them produces an unreadable tangle of overlapping lines and a legend
// nobody can read on a phone screen. Capping to the current top scorers —
// exactly who a "rating movement" chart is about in the first place — keeps
// every remaining line individually traceable.
const chartMaxSeries = 8

// chartPalette is a small, deliberately chosen set of distinct, harmonious
// colors for a dark chart background — no two adjacent enough in hue to be
// confused at a glance, ordered so the first few (used most often, since
// series are drawn best-scorer-first) read clearly even to someone with
// red-green color blindness.
var chartPalette = []color.RGBA{
	{R: 0x38, G: 0xBD, B: 0xF8, A: 0xFF}, // sky blue
	{R: 0xFB, G: 0x92, B: 0x3C, A: 0xFF}, // orange
	{R: 0xA7, G: 0x8B, B: 0xFA, A: 0xFF}, // violet
	{R: 0x34, G: 0xD3, B: 0x99, A: 0xFF}, // emerald
	{R: 0xF4, G: 0x72, B: 0xB6, A: 0xFF}, // pink
	{R: 0xFB, G: 0xBF, B: 0x24, A: 0xFF}, // amber
	{R: 0x60, G: 0xA5, B: 0xFA, A: 0xFF}, // blue
	{R: 0xF8, G: 0x71, B: 0x71, A: 0xFF}, // coral red
}

var (
	chartBackground = color.RGBA{R: 0x0F, G: 0x17, B: 0x2A, A: 0xFF} // deep slate navy
	chartGrid       = color.RGBA{R: 0x27, G: 0x33, B: 0x4A, A: 0xFF} // faint slate, just visible against the background
	chartAxis       = color.RGBA{R: 0x47, G: 0x55, B: 0x6B, A: 0xFF} // slightly brighter than the grid so axes still read as structure
	chartText       = color.RGBA{R: 0xE2, G: 0xE8, B: 0xF0, A: 0xFF} // near-white, soft enough not to glare on the dark ground
	chartMuted      = color.RGBA{R: 0x94, G: 0xA3, B: 0xB8, A: 0xFF} // dimmer text for axis labels
)

// chartSeries is one participant's cumulative-points line: xys[i].Y is
// their total after their i-th settled prediction, in play order.
type chartSeries struct {
	name  string
	xys   plotter.XYs
	final int
}

// buildChartSeries turns a time-ordered flat list of scoring events into
// one cumulative-points series per participant (or, when singleUser is
// set, just theirs), best-scorer-first and capped to chartMaxSeries in
// group mode — see buildProgressionChart's own doc comment for why.
func buildChartSeries(points []scoring.ProgressionPoint, singleUser *common.UserID) (order []common.UserID, byUser map[common.UserID]*chartSeries) {
	order = make([]common.UserID, 0)
	byUser = make(map[common.UserID]*chartSeries)
	for _, pt := range points {
		if singleUser != nil && pt.UserID != *singleUser {
			continue
		}
		s, ok := byUser[pt.UserID]
		if !ok {
			s = &chartSeries{name: pt.DisplayName}
			byUser[pt.UserID] = s
			order = append(order, pt.UserID)
		}
		cumulative := pt.Points
		if n := len(s.xys); n > 0 {
			cumulative += int(s.xys[n-1].Y)
		}
		s.xys = append(s.xys, plotter.XY{X: float64(len(s.xys) + 1), Y: float64(cumulative)})
		s.final = cumulative
	}

	// Best-scorer-first: both what a "rating movement" chart is naturally
	// about, and what makes the chartMaxSeries cutoff below keep the
	// participants a reader most wants to see.
	sort.SliceStable(order, func(i, j int) bool { return byUser[order[i]].final > byUser[order[j]].final })
	if singleUser == nil && len(order) > chartMaxSeries {
		order = order[:chartMaxSeries]
	}
	return order, byUser
}

// buildRankSeries turns the same time-ordered flat list of scoring events
// buildChartSeries uses into one leaderboard-position line per participant
// instead of a cumulative-points one. Every voter of the same match shares
// exactly one PlayedAt (see ProgressionRepository), so consecutive points
// sharing a timestamp are grouped into one "step" — the standings are
// recomputed once per step, after that whole match's points have been
// added, the same way the real leaderboard updates once a match settles,
// not once per individual vote within it.
//
// Rank at each step follows scoring.DenseRank's own rule (a tie shares a
// rank; the next distinct total's rank does not skip ahead) over every
// participant with a cumulative total so far — not just whoever is in this
// particular step — since someone who hasn't predicted in a while is still
// on the board and still has a place to show. DenseRank additionally
// breaks ties on exact-score/prediction counts that ProgressionPoint
// doesn't carry; here a tie breaks on user id instead, purely for a stable
// draw order — it never changes the rank NUMBER itself, only which of two
// equal-points users is drawn "above" the other in the underlying data.
//
// Y is stored as the negative rank (1st place = -1) rather than the rank
// itself: gonum/plot draws larger Y values higher, and negating is the
// simplest way to get "1st place at the top" out of that without fighting
// the library's own axis orientation. buildRankChart's custom tick marker
// relabels those negative values back to the positive rank a reader
// expects to see.
func buildRankSeries(points []scoring.ProgressionPoint, singleUser *common.UserID) (order []common.UserID, byUser map[common.UserID]*chartSeries) {
	totals := map[common.UserID]int{}
	names := map[common.UserID]string{}
	byUser = make(map[common.UserID]*chartSeries)
	order = make([]common.UserID, 0)

	type standing struct {
		userID common.UserID
		total  int
	}

	step := 0
	for i := 0; i < len(points); {
		j := i + 1
		for j < len(points) && points[j].PlayedAt.Equal(points[i].PlayedAt) {
			j++
		}
		step++
		for _, pt := range points[i:j] {
			totals[pt.UserID] += pt.Points
			names[pt.UserID] = pt.DisplayName
		}
		i = j

		standings := make([]standing, 0, len(totals))
		for uid, total := range totals {
			standings = append(standings, standing{uid, total})
		}
		sort.SliceStable(standings, func(a, b int) bool {
			if standings[a].total != standings[b].total {
				return standings[a].total > standings[b].total
			}
			return standings[a].userID.Value < standings[b].userID.Value
		})

		rank := 0
		var previousTotal *int
		for _, s := range standings {
			if previousTotal == nil || s.total != *previousTotal {
				rank++
				total := s.total
				previousTotal = &total
			}
			if singleUser != nil && s.userID != *singleUser {
				continue
			}
			series, ok := byUser[s.userID]
			if !ok {
				series = &chartSeries{name: names[s.userID]}
				byUser[s.userID] = series
				order = append(order, s.userID)
			}
			series.xys = append(series.xys, plotter.XY{X: float64(step), Y: float64(-rank)})
			series.final = -rank
		}
	}

	// Best (closest to 1st place) first — same convention buildChartSeries
	// uses for points, just in the negated rank space described above.
	sort.SliceStable(order, func(i, j int) bool { return byUser[order[i]].final > byUser[order[j]].final })
	if singleUser == nil && len(order) > chartMaxSeries {
		order = order[:chartMaxSeries]
	}
	return order, byUser
}

func styleChartAxes(p *plot.Plot) {
	for _, axis := range []*plot.Axis{&p.X, &p.Y} {
		axis.Label.TextStyle.Color = chartMuted
		axis.Label.TextStyle.Font = font.From(chartFont(false), 12)
		axis.Tick.Label.Color = chartMuted
		axis.Tick.Label.Font = font.From(chartFont(false), 11)
		axis.Tick.Color = chartAxis
		axis.Color = chartAxis
		axis.Width = vg.Points(1)
	}
	// Whole-number match indices on the x-axis — "match 2.5" means nothing.
	p.X.Tick.Marker = plot.DefaultTicks{}
}

func addChartGrid(p *plot.Plot) {
	grid := plotter.NewGrid()
	grid.Vertical.Color = chartGrid
	grid.Vertical.Width = vg.Points(0.5)
	grid.Horizontal.Color = chartGrid
	grid.Horizontal.Width = vg.Points(0.5)
	p.Add(grid)
}

// endLabelYs returns, for each index into order, the Y position its
// end-of-line label should be drawn at — the series' own final value,
// nudged apart from its neighbors when two or more participants finished
// within a few points of each other, so their labels don't overlap.
func endLabelYs(order []common.UserID, byUser map[common.UserID]*chartSeries, minY, maxY float64) map[int]float64 {
	type endLabel struct {
		orderIdx int
		y        float64
	}
	ends := make([]endLabel, len(order))
	for i, uid := range order {
		s := byUser[uid]
		ends[i] = endLabel{orderIdx: i, y: s.xys[len(s.xys)-1].Y}
	}
	if len(ends) > 1 {
		// Greedily push labels apart top-down so two participants who
		// finished within a few points of each other still get
		// individually readable labels, rather than overlapping text.
		sort.SliceStable(ends, func(a, b int) bool { return ends[a].y > ends[b].y })
		minGap := (maxY - minY) * 0.07
		if minGap <= 0 {
			minGap = 1
		}
		for i := 1; i < len(ends); i++ {
			if ends[i-1].y-ends[i].y < minGap {
				ends[i].y = ends[i-1].y - minGap
			}
		}
	}
	byOrderIdx := make(map[int]float64, len(ends))
	for _, e := range ends {
		byOrderIdx[e.orderIdx] = e.y
	}
	return byOrderIdx
}

// chartDataBounds is the min/max Y and max X across every point in every
// series — used both to size the end-label de-collision gap and to widen
// the axis enough to hold the labels themselves.
func chartDataBounds(order []common.UserID, byUser map[common.UserID]*chartSeries) (minY, maxY, maxX float64) {
	minY, maxY = math.MaxFloat64, -math.MaxFloat64
	for _, uid := range order {
		for _, pt := range byUser[uid].xys {
			minY = math.Min(minY, pt.Y)
			maxY = math.Max(maxY, pt.Y)
			maxX = math.Max(maxX, pt.X)
		}
	}
	return minY, maxY, maxX
}

// buildProgressionChart turns a time-ordered flat list of scoring events
// into a cumulative-points-per-participant line chart, PNG-encoded. Each
// participant's own points are summed in the order their matches were
// played (already guaranteed by ProgressionRepository), so the resulting
// line is their rating's actual match-by-match trajectory, not a
// reordering. singleUser, when non-nil, draws exactly that participant (the
// DM "my own progression" case) and skips the end-of-line labels entirely,
// since a one-line chart naming itself is just noise.
func buildProgressionChart(points []scoring.ProgressionPoint, title, xLabel, yLabel string, singleUser *common.UserID) ([]byte, error) {
	order, byUser := buildChartSeries(points, singleUser)
	if len(order) == 0 {
		return nil, errors.New("no settled predictions to chart")
	}
	p := newChartPlot(title, xLabel, yLabel)
	if err := drawChartSeries(p, order, byUser, singleUser); err != nil {
		return nil, err
	}
	return renderChartPNG(p)
}

// buildRankChart is buildProgressionChart's sibling for leaderboard
// position instead of cumulative points — same figure, same "one line per
// participant, best first" drawing (drawChartSeries doesn't care what the
// Y axis actually measures), built from buildRankSeries instead of
// buildChartSeries. The only real difference is the Y axis itself: ranks
// are stored negative (see buildRankSeries) so 1st place plots at the top,
// which means the default numeric tick labels would read "-1, -2, -3, ..."
// instead of "1, 2, 3, ...". The custom ticker below is the fix — it
// negates each tick's label back to the rank a reader actually expects.
func buildRankChart(points []scoring.ProgressionPoint, title, xLabel, yLabel string, singleUser *common.UserID) ([]byte, error) {
	order, byUser := buildRankSeries(points, singleUser)
	if len(order) == 0 {
		return nil, errors.New("no settled predictions to chart")
	}
	p := newChartPlot(title, xLabel, yLabel)
	p.Y.Tick.Marker = plot.TickerFunc(func(min, max float64) []plot.Tick {
		var ticks []plot.Tick
		for v := int(math.Ceil(min)); v <= int(math.Floor(max)); v++ {
			ticks = append(ticks, plot.Tick{Value: float64(v), Label: strconv.Itoa(-v)})
		}
		return ticks
	})
	if err := drawChartSeries(p, order, byUser, singleUser); err != nil {
		return nil, err
	}
	return renderChartPNG(p)
}

// newChartPlot builds the styled, empty canvas both chart kinds share —
// background, title, axis labels/styling, and the grid — before either
// draws its own series onto it.
func newChartPlot(title, xLabel, yLabel string) *plot.Plot {
	registerChartFont()
	p := plot.New()
	p.BackgroundColor = chartBackground
	p.Title.Text = title
	p.Title.TextStyle.Color = chartText
	p.Title.TextStyle.Font = font.From(chartFont(true), 16)
	p.X.Label.Text = xLabel
	p.Y.Label.Text = yLabel
	styleChartAxes(p)
	addChartGrid(p)
	return p
}

// drawChartSeries adds every participant's line — and, in group mode, its
// end-of-line label — to p. Shared by buildProgressionChart and
// buildRankChart: both draw the exact same shape of figure (a handful of
// XY lines, best-first, labeled at their own endpoint), they just disagree
// on what a line's Y values mean.
//
// End-of-line labels instead of a boxed legend: gonum/plot draws a Legend
// inside the same canvas as the data with no automatic reserved gutter, so
// with more than a couple of series it either overlaps the lines or gets
// clipped past the image edge — exactly the "collision" this chart must
// avoid. Naming each line at its own endpoint needs no color-matching
// lookup from a separate box either, which reads faster on a phone screen
// than a traditional legend would.
func drawChartSeries(p *plot.Plot, order []common.UserID, byUser map[common.UserID]*chartSeries, singleUser *common.UserID) error {
	minY, maxY, maxX := chartDataBounds(order, byUser)
	var endYByOrderIdx map[int]float64
	if singleUser == nil {
		endYByOrderIdx = endLabelYs(order, byUser, minY, maxY)

		// Reserve room to the right of the last data point for the labels —
		// without this the plot's own data area fills the whole canvas and
		// an end label at the last point draws half off the image edge.
		xRange := maxX - 1
		labelGutter := xRange * 0.30
		if labelGutter <= 0 {
			labelGutter = 1
		}
		p.X.Max = maxX + labelGutter
	}

	var labelPts plotter.XYs
	var labelNames []string
	var labelStyles []plottext.Style

	for i, uid := range order {
		s := byUser[uid]
		line, pts, err := plotter.NewLinePoints(s.xys)
		if err != nil {
			return err
		}
		col := chartPalette[i%len(chartPalette)]
		line.LineStyle = draw.LineStyle{Color: col, Width: vg.Points(2.5)}
		pts.GlyphStyle = draw.GlyphStyle{Color: col, Radius: vg.Points(2.5), Shape: draw.CircleGlyph{}}
		p.Add(line, pts)

		if singleUser == nil {
			labelPts = append(labelPts, plotter.XY{X: maxX, Y: endYByOrderIdx[i]})
			labelNames = append(labelNames, s.name)
			labelStyles = append(labelStyles, plottext.Style{
				Color: col, Font: font.From(chartFont(false), 11), XAlign: draw.XLeft, YAlign: draw.YCenter,
				Handler: plot.DefaultTextHandler,
			})
		}
	}
	if singleUser == nil {
		labels, err := plotter.NewLabels(plotter.XYLabels{XYs: labelPts, Labels: labelNames})
		if err != nil {
			return err
		}
		labels.TextStyle = labelStyles
		labels.Offset = vg.Point{X: vg.Points(8)}
		p.Add(labels)
	}
	return nil
}

// renderChartPNG encodes the finished plot at a fixed size — both chart
// kinds are sent as a Telegram photo the same way, so they share one size.
func renderChartPNG(p *plot.Plot) ([]byte, error) {
	writerTo, err := p.WriterTo(9*vg.Inch, 5*vg.Inch, "png")
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if _, err := writerTo.WriteTo(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
