package bench

import (
	"fmt"
	"io"
	"strings"

	"github.com/nem0z/book-finder/internal/search"
)

const (
	plotSize   = 640
	plotMargin = 60
)

// ratingColours colour works by rating, 0 to 3; ratingOrder draws irrelevant works first so that
// relevant ones stay visible on top.
var (
	ratingColours = []string{"#c8c8c8", "#f0b429", "#3d8bfd", "#1a7f37"}
	ratingOrder   = []int{0, 1, 2, 3}
)

// WriteSVG draws variant v as a scatter plot: one dot per rated work and query, at its rank before
// re-ranking (x) and after (y, best at the top), coloured by rating. Dots on the diagonal did not move;
// dots above it moved up. Dashed lines mark the bands of ranks.
func (e RerankEffect) WriteSVG(w io.Writer, v int) error {
	area := float64(plotSize - 2*plotMargin)
	pos := func(rank int) float64 { return plotMargin + area*(float64(rank)+0.5)/float64(RecallDepth) }

	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" font-family="sans-serif" font-size="12">`+"\n", plotSize, plotSize)
	fmt.Fprintf(&b, `<rect width="100%%" height="100%%" fill="white"/>`+"\n")
	fmt.Fprintf(&b, `<text x="%d" y="24" font-size="15" font-weight="bold">Re-ranking the top %d of %d queries: rank before → after</text>`+"\n",
		plotMargin, e.Depths[v], len(e.Queries))
	fmt.Fprintf(&b, `<rect x="%d" y="%d" width="%.0f" height="%.0f" fill="none" stroke="#888"/>`+"\n", plotMargin, plotMargin, area, area)
	fmt.Fprintf(&b, `<line x1="%d" y1="%d" x2="%.0f" y2="%.0f" stroke="#888" stroke-width="0.5"/>`+"\n",
		plotMargin, plotMargin, plotMargin+area, plotMargin+area)

	for _, d := range Depths[:len(Depths)-1] {
		x := pos(d) - area/float64(2*RecallDepth)
		fmt.Fprintf(&b, `<line x1="%.1f" y1="%d" x2="%.1f" y2="%.0f" stroke="#888" stroke-dasharray="4 3"/>`+"\n", x, plotMargin, x, plotMargin+area)
		fmt.Fprintf(&b, `<line x1="%d" y1="%.1f" x2="%.0f" y2="%.1f" stroke="#888" stroke-dasharray="4 3"/>`+"\n", plotMargin, x, plotMargin+area, x)
	}

	for _, tick := range []int{1, 10, 30, 50, 100} {
		p := pos(tick - 1)
		fmt.Fprintf(&b, `<text x="%.1f" y="%.0f" text-anchor="middle">%d</text>`+"\n", p, plotMargin+area+16, tick)
		fmt.Fprintf(&b, `<text x="%d" y="%.1f" text-anchor="end" dominant-baseline="middle">%d</text>`+"\n", plotMargin-6, p, tick)
	}

	fmt.Fprintf(&b, `<text x="%.0f" y="%d" text-anchor="middle">rank before re-ranking</text>`+"\n", plotMargin+area/2, plotSize-14)
	fmt.Fprintf(&b, `<text transform="translate(16 %.0f) rotate(-90)" text-anchor="middle">rank after re-ranking</text>`+"\n", plotMargin+area/2)

	moves := e.moves(v)
	for _, r := range ratingOrder {
		for _, m := range moves {
			if m.rating == r {
				fmt.Fprintf(&b, `<circle cx="%.1f" cy="%.1f" r="2.6" fill="%s" fill-opacity="0.75"/>`+"\n",
					pos(m.before), pos(m.after), ratingColours[r])
			}
		}
	}

	for i, c := range ratingColours {
		x := plotMargin + 10 + 110*i
		fmt.Fprintf(&b, `<circle cx="%d" cy="42" r="5" fill="%s"/><text x="%d" y="46">rated %d</text>`+"\n", x, c, x+9, i)
	}

	b.WriteString("</svg>\n")
	_, err := io.WriteString(w, b.String())

	return err
}

// curveColours colour the rating curves: before re-ranking, then each variant.
var curveColours = []string{"#555555", "#3d8bfd", "#d1495b"}

// WriteCurveSVG draws, for each order (before re-ranking, then each detailed variant), the mean rating at each
// rank over the queries, averaged over 5 ranks. A good re-ranker makes the curve start higher and fall
// faster: it moves the best-rated works to the top.
func (e RerankEffect) WriteCurveSVG(w io.Writer) error {
	area := float64(plotSize - 2*plotMargin)
	x := func(rank float64) float64 { return plotMargin + area*rank/float64(RecallDepth-1) }
	y := func(rating float64) float64 { return plotMargin + area*(1-rating/MaxRating) }

	names := []string{"before re-ranking"}
	orders := [][][]search.Result{e.Before}
	for _, v := range e.detailedVariants() {
		names, orders = append(names, fmt.Sprintf("re-rank top %d", e.Depths[v])), append(orders, e.After[v])
	}

	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" font-family="sans-serif" font-size="12">`+"\n", plotSize, plotSize)
	b.WriteString(`<rect width="100%" height="100%" fill="white"/>` + "\n")
	fmt.Fprintf(&b, `<text x="%d" y="24" font-size="15" font-weight="bold">Mean rating at each rank, %d queries</text>`+"\n", plotMargin, len(e.Queries))
	fmt.Fprintf(&b, `<rect x="%d" y="%d" width="%.0f" height="%.0f" fill="none" stroke="#888"/>`+"\n", plotMargin, plotMargin, area, area)
	for _, d := range Depths[:len(Depths)-1] {
		fmt.Fprintf(&b, `<line x1="%.1f" y1="%d" x2="%.1f" y2="%.0f" stroke="#888" stroke-dasharray="4 3"/>`+"\n",
			x(float64(d)-0.5), plotMargin, x(float64(d)-0.5), plotMargin+area)
	}

	for r := 0; r <= MaxRating; r++ {
		fmt.Fprintf(&b, `<text x="%d" y="%.1f" text-anchor="end" dominant-baseline="middle">%d</text>`+"\n", plotMargin-6, y(float64(r)), r)
	}

	for _, tick := range []int{1, 10, 30, 50, 100} {
		fmt.Fprintf(&b, `<text x="%.1f" y="%.0f" text-anchor="middle">%d</text>`+"\n", x(float64(tick-1)), plotMargin+area+16, tick)
	}

	fmt.Fprintf(&b, `<text x="%.0f" y="%d" text-anchor="middle">rank</text>`+"\n", plotMargin+area/2, plotSize-14)
	fmt.Fprintf(&b, `<text transform="translate(16 %.0f) rotate(-90)" text-anchor="middle">mean rating (5-rank average)</text>`+"\n", plotMargin+area/2)

	for oi, lists := range orders {
		curve := e.curve(lists)
		var pts []string
		for i, v := range curve {
			pts = append(pts, fmt.Sprintf("%.1f,%.1f", x(float64(i)), y(v)))
		}

		colour := curveColours[oi%len(curveColours)]
		fmt.Fprintf(&b, `<polyline points="%s" fill="none" stroke="%s" stroke-width="2"/>`+"\n", strings.Join(pts, " "), colour)
		lx := plotMargin + 10 + 170*oi
		fmt.Fprintf(&b, `<line x1="%d" y1="42" x2="%d" y2="42" stroke="%s" stroke-width="3"/><text x="%d" y="46">%s</text>`+"\n",
			lx, lx+20, colour, lx+26, names[oi])
	}

	b.WriteString("</svg>\n")
	_, err := io.WriteString(w, b.String())

	return err
}

// curve returns the mean rating at each rank of lists ([query]), each value averaged with the two ranks on
// either side.
func (e RerankEffect) curve(lists [][]search.Result) []float64 {
	sums, counts := make([]float64, RecallDepth), make([]int, RecallDepth)
	for qi, list := range lists {
		for i, w := range list[:min(RecallDepth, len(list))] {
			if r, ok := e.Ratings[qi][w.ID]; ok {
				sums[i] += float64(r)
				counts[i]++
			}
		}
	}

	out := make([]float64, RecallDepth)
	for i := range out {
		s, n := 0.0, 0
		for j := max(0, i-2); j <= min(RecallDepth-1, i+2); j++ {
			s += sums[j]
			n += counts[j]
		}

		if n > 0 {
			out[i] = s / float64(n)
		}
	}

	return out
}
