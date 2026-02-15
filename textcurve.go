package textcurve

import (
	"encoding/binary"
	"errors"
	"math"
	"sync"

	"github.com/golang/freetype/truetype"
	"github.com/unixpickle/model3d/model2d"
	"golang.org/x/image/font"
	"golang.org/x/image/math/fixed"
)

var fontAscenders sync.Map // map[*truetype.Font]float64 (FUnits)

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
	Size      float64 // OpenSCAD-like: target ascent (baseline to top) in model units
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
	if asc, ok := parseOS2TypoAscender(ttfBytes); ok && asc > 0 {
		fontAscenders.Store(f, asc)
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

	// Scale: map font ascent (baseline->top) -> opt.Size in model units,
	// to match OpenSCAD's text(size=...).
	upem := float64(ttFont.FUnitsPerEm())
	ascent := lookupAscender(ttFont)
	if ascent <= 0 {
		fontBounds := ttFont.Bounds(fixed.Int26_6(ttFont.FUnitsPerEm()))
		ascent = float64(fontBounds.Max.Y)
	}
	if ascent <= 0 {
		ascent = upem
	}
	scale := opt.Size / ascent

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
		contourPts := pts[start:end]
		start = end
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
// This version correctly handles wrap-around implied points and consecutive off-curve points.
func flattenTrueTypeContour(pts []truetype.Point, penX float64, scale float64, segs int) Contour {
	if len(pts) == 0 {
		return nil
	}

	toVec := func(p truetype.Point) model2d.Coord {
		x := (float64(p.X)/64.0 + penX) * scale
		y := (float64(p.Y) / 64.0) * scale
		return model2d.Coord{X: x, Y: y}
	}
	onCurve := func(p truetype.Point) bool { return p.Flags&0x01 != 0 }

	n := len(pts)

	// Choose the TrueType start point.
	var start model2d.Coord
	startIdx := 0
	if onCurve(pts[0]) {
		start = toVec(pts[0])
		startIdx = 0
	} else if onCurve(pts[n-1]) {
		start = toVec(pts[n-1])
		startIdx = n - 1
	} else {
		start = toVec(pts[n-1]).Mid(toVec(pts[0]))
		startIdx = 0
	}

	poly := make(Contour, 0, n*segs+4)
	poly = append(poly, start)

	prevOn := start
	var haveCtrl bool
	var ctrl model2d.Coord

	// Walk points once around the contour, starting after the chosen anchor.
	i := (startIdx + 1) % n
	for steps := 0; steps < n; steps++ {
		p := pts[i]

		if onCurve(p) {
			on := toVec(p)
			if haveCtrl {
				// Quadratic: prevOn -> ctrl -> on
				poly = append(poly, flattenQuad(prevOn, ctrl, on, segs)...)
				haveCtrl = false
			} else {
				// Line: prevOn -> on
				poly = append(poly, on)
			}
			prevOn = on
			i = (i + 1) % n
			continue
		}

		// Off-curve control point.
		c := toVec(p)
		if haveCtrl {
			// Two consecutive off-curve points => implied on-curve at midpoint.
			implied := ctrl.Mid(c)
			poly = append(poly, flattenQuad(prevOn, ctrl, implied, segs)...)
			prevOn = implied
			// Keep the new control pending.
			ctrl = c
			haveCtrl = true
		} else {
			ctrl = c
			haveCtrl = true
		}
		i = (i + 1) % n
	}

	// Close contour back to start.
	if haveCtrl {
		poly = append(poly, flattenQuad(prevOn, ctrl, start, segs)...)
	} else {
		// Avoid duplicating if already at start.
		if poly[len(poly)-1] != start {
			poly = append(poly, start)
		}
	}

	// Ensure explicit closure.
	if poly[len(poly)-1] != poly[0] {
		poly = append(poly, poly[0])
	}

	if len(poly) < 4 {
		return nil
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

func lookupAscender(ttFont *truetype.Font) float64 {
	if v, ok := fontAscenders.Load(ttFont); ok {
		if asc, ok := v.(float64); ok {
			return asc
		}
	}
	return 0
}

func parseOS2TypoAscender(data []byte) (float64, bool) {
	const (
		tableDirOffset = 12
		recordSize     = 16
		os2Tag         = "OS/2"
		typoAscOffset  = 68
	)
	if len(data) < tableDirOffset {
		return 0, false
	}
	numTables := int(binary.BigEndian.Uint16(data[4:6]))
	if numTables < 0 || len(data) < tableDirOffset+numTables*recordSize {
		return 0, false
	}
	for i := 0; i < numTables; i++ {
		recOff := tableDirOffset + i*recordSize
		tag := string(data[recOff : recOff+4])
		if tag != os2Tag {
			continue
		}
		tableOffset := int(binary.BigEndian.Uint32(data[recOff+8 : recOff+12]))
		tableLen := int(binary.BigEndian.Uint32(data[recOff+12 : recOff+16]))
		if tableOffset < 0 || tableLen < 0 || tableOffset+tableLen > len(data) || tableLen < typoAscOffset+2 {
			return 0, false
		}
		raw := int16(binary.BigEndian.Uint16(data[tableOffset+typoAscOffset : tableOffset+typoAscOffset+2]))
		return float64(raw), raw > 0
	}
	return 0, false
}
