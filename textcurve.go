package textcurve

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"

	"github.com/go-text/typesetting/di"
	gotextfont "github.com/go-text/typesetting/font"
	ot "github.com/go-text/typesetting/font/opentype"
	"github.com/go-text/typesetting/shaping"
	"github.com/golang/freetype/truetype"
	"github.com/unixpickle/model3d/model2d"
	xfont "golang.org/x/image/font"
	"golang.org/x/image/math/fixed"
)

var hbFeatureTags = struct {
	kern ot.Tag
}{
	kern: ot.MustNewTag("kern"),
}

type Contour []model2d.Coord
type Outlines []Contour

// HAlign controls horizontal text alignment.
//
// Available options:
//   - HAlignLeft: align to the text origin on the left.
//   - HAlignCenter: center around the text origin.
//   - HAlignRight: align to the text advance on the right.
type HAlign int

const (
	// HAlignLeft aligns text to the left of the anchor point.
	HAlignLeft HAlign = iota
	// HAlignCenter centers text around the anchor point.
	HAlignCenter
	// HAlignRight aligns text to the right of the anchor point.
	HAlignRight
)

// VAlign controls vertical text alignment.
//
// Available options:
//   - VAlignBaseline: keep the baseline at y=0.
//   - VAlignTop: align the top bound to y=0.
//   - VAlignCenter: center vertically around y=0.
//   - VAlignBottom: align the bottom bound to y=0.
type VAlign int

const (
	// VAlignBaseline aligns text to the baseline.
	VAlignBaseline VAlign = iota
	// VAlignTop aligns text to the top bound.
	VAlignTop
	// VAlignCenter centers text vertically.
	VAlignCenter
	// VAlignBottom aligns text to the bottom bound.
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
	Spacing   float64 // OpenSCAD-like spacing multiplier; 0 defaults to 1
}

// ParsedFont stores parsed TrueType data and auxiliary metrics/layout state.
type ParsedFont struct {
	TTFont *truetype.Font

	ascent float64
	hbFace *gotextfont.Face
}

// ParseTTF parses a TTF/OTF(TrueType outlines) font file.
func ParseTTF(ttfBytes []byte) (*ParsedFont, error) {
	ttf, err := truetype.Parse(ttfBytes)
	if err != nil {
		return nil, err
	}
	res := &ParsedFont{TTFont: ttf}
	if asc, ok := parseOS2TypoAscender(ttfBytes); ok && asc > 0 {
		res.ascent = asc
	}
	if hbFace, err := gotextfont.ParseTTF(bytes.NewReader(ttfBytes)); err == nil {
		res.hbFace = hbFace
	}
	return res, nil
}

// TextOutlines returns contours for each glyph, already positioned, scaled to Options.Size,
// and aligned per Options.Align.
func TextOutlines(parsed *ParsedFont, s string, opt Options) (Outlines, error) {
	if parsed == nil || parsed.TTFont == nil {
		return nil, errors.New("nil font")
	}
	ttFont := parsed.TTFont
	if opt.Size <= 0 {
		return nil, errors.New("Size must be > 0")
	}
	if opt.CurveSegs <= 0 {
		opt.CurveSegs = 8
	}
	if opt.Spacing == 0 {
		opt.Spacing = 1
	}
	if opt.Spacing < 0 {
		return nil, errors.New("Spacing must be >= 0")
	}

	// Scale: map font ascent (baseline->top) -> opt.Size in model units,
	// to match OpenSCAD's text(size=...).
	upem := float64(ttFont.FUnitsPerEm())
	ascent := parsed.ascent
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
	layoutAdvance := 0.0

	// Track overall bounds in model units for alignment.
	minX, minY := math.Inf(1), math.Inf(1)
	maxX, maxY := math.Inf(-1), math.Inf(-1)

	if hbGlyphs, hbAdvance, ok := shapeGlyphsWithHarfBuzz(parsed, s, opt); ok {
		layoutAdvance = hbAdvance
		for _, g := range hbGlyphs {
			gb = truetype.GlyphBuf{}
			if err := gb.Load(ttFont, fixedScale, g.index, xfont.HintingNone); err != nil {
				continue
			}
			contours := glyphContoursToPolylines(&gb, g.penX, scale, opt.CurveSegs)
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
		}
	} else {
		var prev truetype.Index
		hasPrev := false
		for _, r := range s {
			idx := ttFont.Index(r)

			if opt.Kerning && hasPrev {
				k := ttFont.Kern(fixedScale, prev, idx) // 26.6
				penX += (float64(k) / 64.0) * opt.Spacing
			}

			gb = truetype.GlyphBuf{}
			if err := gb.Load(ttFont, fixedScale, idx, xfont.HintingNone); err != nil {
				adv := ttFont.HMetric(fixedScale, idx).AdvanceWidth
				penX += (float64(adv) / 64.0) * opt.Spacing
				prev, hasPrev = idx, true
				continue
			}

			contours := glyphContoursToPolylines(&gb, penX, scale, opt.CurveSegs)
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
			adv := ttFont.HMetric(fixedScale, idx).AdvanceWidth
			penX += (float64(adv) / 64.0) * opt.Spacing
			prev, hasPrev = idx, true
		}
		layoutAdvance = penX
	}

	if len(outlines) == 0 {
		return nil, nil
	}

	// Alignment translation
	dx, dy := computeAlign(opt, minX, minY, maxX, maxY, layoutAdvance*scale)

	// Apply translation
	for i := range outlines {
		for j := range outlines[i] {
			outlines[i][j].X += dx
			outlines[i][j].Y += dy
		}
	}

	return outlines, nil
}

// OutlinesMesh converts text outlines into a single 2D mesh.
// Each contour is added as a closed polyline.
func OutlinesMesh(outlines Outlines) *model2d.Mesh {
	mesh := model2d.NewMesh()
	for _, contour := range outlines {
		if len(contour) < 2 {
			continue
		}
		for i := 1; i < len(contour); i++ {
			mesh.Add(&model2d.Segment{contour[i-1], contour[i]})
		}
		if contour[0] != contour[len(contour)-1] {
			mesh.Add(&model2d.Segment{contour[len(contour)-1], contour[0]})
		}
	}
	return mesh
}

// computeAlign uses the final bounds and (optionally) font vertical metrics.
// For simplicity, baseline means y=0 baseline, and top/bottom use outline bounds.
func computeAlign(opt Options, minX, minY, maxX, maxY, advanceWidth float64) (dx, dy float64) {
	width := maxX - minX

	switch opt.Align.HAlign {
	case HAlignRight:
		// Match OpenSCAD-like behavior: right alignment is relative to the
		// text origin plus total advance, not the outline's max X.
		dx = -advanceWidth
	case HAlignCenter:
		dx = -(minX + width/2)
	case HAlignLeft:
		// Match OpenSCAD-like behavior: left alignment is relative to the
		// text origin (pen start), not the outline's leftmost bound.
		dx = 0
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

type positionedGlyph struct {
	index truetype.Index
	penX  float64 // in font units
}

func shapeGlyphsWithHarfBuzz(parsed *ParsedFont, s string, opt Options) ([]positionedGlyph, float64, bool) {
	if parsed == nil || parsed.hbFace == nil || parsed.TTFont == nil {
		return nil, 0, false
	}
	ttFont := parsed.TTFont
	hbFace := parsed.hbFace

	runes := []rune(s)
	if len(runes) == 0 {
		return nil, 0, true
	}

	var features []shaping.FontFeature
	if !opt.Kerning {
		// Keep HarfBuzz defaults, but explicitly disable kerning when requested.
		features = append(features, shaping.FontFeature{Tag: hbFeatureTags.kern, Value: 0})
	}

	shaper := shaping.HarfbuzzShaper{}
	out := shaper.Shape(shaping.Input{
		Text:         runes,
		RunStart:     0,
		RunEnd:       len(runes),
		Direction:    di.DirectionLTR,
		Face:         hbFace,
		FontFeatures: features,
		Size:         fixed.I(int(ttFont.FUnitsPerEm())),
	})

	res := make([]positionedGlyph, 0, len(out.Glyphs))
	penX := 0.0
	for _, g := range out.Glyphs {
		xOffset := float64(out.ToFontUnit(g.XOffset))
		res = append(res, positionedGlyph{
			index: truetype.Index(g.GlyphID),
			penX:  penX + xOffset,
		})
		penX += float64(out.ToFontUnit(g.XAdvance)) * opt.Spacing
	}
	return res, penX, true
}
