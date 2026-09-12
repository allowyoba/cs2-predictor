package telegram

import (
	"bytes"
	"errors"
	"fmt"
	"image/color"
	"math"
	"sort"
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

	// End-of-line labels instead of a boxed legend: gonum/plot draws a
	// Legend inside the same canvas as the data with no automatic reserved
	// gutter, so with more than a couple of series it either overlaps the
	// lines or gets clipped past the image edge — exactly the "collision"
	// this chart must avoid. Naming each line at its own endpoint needs no
	// color-matching lookup from a separate box either, which reads faster
	// on a phone screen than a traditional legend would.
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
			return nil, err
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
			return nil, err
		}
		labels.TextStyle = labelStyles
		labels.Offset = vg.Point{X: vg.Points(8)}
		p.Add(labels)
	}

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
