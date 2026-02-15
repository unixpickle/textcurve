package textcurve

import (
	"errors"
	"math"

	"github.com/golang/freetype/truetype"
	"github.com/unixpickle/model3d/model2d"
	"golang.org/x/image/font"
	"golang.org/x/image/math/fixed"
)

type Contour []model2d.Coord
type Outlines []Contour

type HAlign int

const (
	HAlignLeft HAlign = iota
	HAlignCenter
	HAlignRight
)

type VAlign int

const (
	VAlignBaseline VAlign = iota
	VAlignTop
	VAlignCenter
	VAlignBottom
)

type Align struct {
	HAlign HAlign
	VAlign VAlign
}

type Options struct {
	Size      float64 // "OpenSCAD-ish": em height in your model units
	CurveSegs int     // flattening segments per quadratic
	Align     Align
	Kerning   bool
}

// ParseTTF parses a TTF/OTF(TrueType outlines) font file.
func ParseTTF(ttfBytes []byte) (*truetype.Font, error) {
	f, err := truetype.Parse(ttfBytes)
	if err != nil {
		return nil, err
	}
	return f, nil
}

// TextOutlines returns contours for each glyph, already positioned, scaled to Options.Size,
// and aligned per Options.Align.
func TextOutlines(ttFont *truetype.Font, s string, opt Options) (Outlines, error) {
	if ttFont == nil {
		return nil, errors.New("nil font")
	}
	if opt.Size <= 0 {
		return nil, errors.New("Size must be > 0")
	}
	if opt.CurveSegs <= 0 {
		opt.CurveSegs = 8
	}

	// Scale: map 1 em -> opt.Size in model units.
	upem := float64(ttFont.FUnitsPerEm())
	scale := opt.Size / upem

	// truetype uses 26.6 fixed point "scale" for glyph loading.
	// We choose a fixed scale proportional to upem so that glyph coords come out in font units,
	// then apply our own float scale.
	//
	// Setting fixedScale = 64*upem makes 1 font unit = 64 in the GlyphBuf.
	fixedScale := fixed.Int26_6(int32(upem * 64))

	var gb truetype.GlyphBuf
	var outlines Outlines

	// Pen position in font units (float font-units, before applying `scale`).
	penX := 0.0

	var prev truetype.Index
	hasPrev := false

	// Track overall bounds in model units for alignment.
	minX, minY := math.Inf(1), math.Inf(1)
	maxX, maxY := math.Inf(-1), math.Inf(-1)

	for _, r := range s {
		idx := ttFont.Index(r)

		// Kerning in font units (FUnits)
		if opt.Kerning && hasPrev {
			k := ttFont.Kern(fixedScale, prev, idx) // 26.6
			penX += float64(k) / 64.0
		}

		// Load glyph into gb; coords are in 26.6 at our fixedScale.
		gb = truetype.GlyphBuf{}
		if err := gb.Load(ttFont, fixedScale, idx, font.HintingNone); err != nil {
			// Skip missing glyphs
			adv := ttFont.HMetric(fixedScale, idx).AdvanceWidth
			penX += float64(adv) / 64.0
			prev, hasPrev = idx, true
			continue
		}

		// Convert contours to polylines in model units
		contours := glyphContoursToPolylines(&gb, penX, scale, opt.CurveSegs)

		// Update bounds
		for _, c := range contours {
			for _, p := range c {
				if p.X < minX {
					minX = p.X
				}
				if p.Y < minY {
					minY = p.Y
				}
				if p.X > maxX {
					maxX = p.X
				}
				if p.Y > maxY {
					maxY = p.Y
				}
			}
		}

		outlines = append(outlines, contours...)

		// Advance pen by glyph advance width
		adv := ttFont.HMetric(fixedScale, idx).AdvanceWidth
		penX += float64(adv) / 64.0

		prev, hasPrev = idx, true
	}

	if len(outlines) == 0 {
		return nil, nil
	}

	// Alignment translation
	dx, dy := computeAlign(opt, minX, minY, maxX, maxY)

	// Apply translation
	for i := range outlines {
		for j := range outlines[i] {
			outlines[i][j].X += dx
			outlines[i][j].Y += dy
		}
	}

	return outlines, nil
}

// computeAlign uses the final bounds and (optionally) font vertical metrics.
// For simplicity, baseline means y=0 baseline, and top/bottom use outline bounds.
func computeAlign(opt Options, minX, minY, maxX, maxY float64) (dx, dy float64) {
	width := maxX - minX

	switch opt.Align.HAlign {
	case HAlignRight:
		dx = -maxX
	case HAlignCenter:
		dx = -(minX + width/2)
	case HAlignLeft:
		dx = -minX
	default:
		panic("unknown HAlign")
	}

	switch opt.Align.VAlign {
	case VAlignTop:
		dy = -maxY
	case VAlignCenter:
		dy = -(minY + (maxY-minY)/2)
	case VAlignBottom:
		dy = -minY
	case VAlignBaseline:
		// Keep baseline at y=0. Our outlines are already in a baseline-ish space
		// because TTF glyph Y is relative to baseline; we invert Y in conversion.
		dy = 0
	default:
		panic("unknown VAlign")
	}

	return dx, dy
}

// glyphContoursToPolylines converts truetype contour points into flattened polylines.
// penX is in font units; scale maps font units -> model units.
// NOTE: We invert Y because TTF Y goes up; most model coords want Y up too, but if your downstream
// expects OpenSCAD-like Y up, keep it as-is. Here we keep Y up by not flipping twice.
func glyphContoursToPolylines(gb *truetype.GlyphBuf, penX float64, scale float64, segs int) []Contour {
	pts := gb.Points
	ends := gb.Ends

	var out []Contour
	start := 0

	for _, end := range ends {
		contourPts := pts[start : end+1]
		start = end + 1
		if len(contourPts) == 0 {
			continue
		}

		// Build a polyline by walking points and flattening implied quadratics.
		poly := flattenTrueTypeContour(contourPts, penX, scale, segs)
		if len(poly) >= 3 {
			out = append(out, poly)
		}
	}

	return out
}

// flattenTrueTypeContour handles on-curve/off-curve quadratic points per TrueType spec.
// This is a simplified implementation sufficient for "mostly consistent" shapes.
func flattenTrueTypeContour(pts []truetype.Point, penX float64, scale float64, segs int) Contour {
	// Helper to convert truetype.Point(26.6) -> model units
	toVec := func(p truetype.Point) model2d.Coord {
		x := (float64(p.X)/64.0 + penX) * scale
		y := (float64(p.Y) / 64.0) * scale
		return model2d.Coord{X: x, Y: y}
	}

	onCurve := func(p truetype.Point) bool { return p.Flags&0x01 != 0 }

	n := len(pts)
	if n == 0 {
		return nil
	}

	type pt struct {
		coord   model2d.Coord
		onCurve bool
	}

	expanded := make([]pt, 0, n*2)
	for i := 0; i < n; i++ {
		p := pts[i]
		cur := pt{coord: toVec(p), onCurve: onCurve(p)}
		if len(expanded) > 0 {
			prev := expanded[len(expanded)-1]
			if !prev.onCurve && !cur.onCurve {
				expanded = append(expanded, pt{coord: prev.coord.Mid(cur.coord), onCurve: true})
			}
		}
		expanded = append(expanded, cur)
	}

	if len(expanded) == 0 {
		return nil
	}

	if !expanded[0].onCurve {
		last := expanded[len(expanded)-1]
		if last.onCurve {
			expanded = append([]pt{last}, expanded...)
		} else {
			start := pt{coord: last.coord.Mid(expanded[0].coord), onCurve: true}
			expanded = append([]pt{start}, expanded...)
		}
	}

	// Close the loop by repeating the start point.
	if expanded[len(expanded)-1].coord != expanded[0].coord || expanded[len(expanded)-1].onCurve != expanded[0].onCurve {
		expanded = append(expanded, expanded[0])
	}

	poly := Contour{expanded[0].coord}
	prevOn := expanded[0].coord
	for i := 1; i < len(expanded); i++ {
		p := expanded[i]
		if p.onCurve {
			poly = append(poly, p.coord)
			prevOn = p.coord
			continue
		}
		if i+1 >= len(expanded) || !expanded[i+1].onCurve {
			return poly
		}
		nextOn := expanded[i+1].coord
		poly = append(poly, flattenQuad(prevOn, p.coord, nextOn, segs)...)
		prevOn = nextOn
		i++
	}

	// Ensure explicit closure.
	if len(poly) > 0 && (poly[len(poly)-1] != poly[0]) {
		poly = append(poly, poly[0])
	}

	return poly
}

func flattenQuad(p0, p1, p2 model2d.Coord, segs int) []model2d.Coord {
	out := make([]model2d.Coord, 0, segs)
	for i := 1; i <= segs; i++ {
		t := float64(i) / float64(segs)
		u := 1 - t
		p := p0.Scale(u * u).Add(p1.Scale(2 * u * t)).Add(p2.Scale(t * t))
		out = append(out, p)
	}
	return out
}
