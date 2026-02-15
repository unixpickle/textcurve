package textcurve

import (
	"fmt"
	"image/color"
	"math"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/unixpickle/model3d/model2d"
	"github.com/unixpickle/model3d/model3d"
)

const (
	sampleCount          = 30000
	correlationThreshold = 0.87
	textSize             = 10.0
	curveSegs            = 16
	extrudeHeight        = 1.0
	rasterScale          = 5.0
)

var testStrings = []string{
	"Hi!",
	"Sp",
	"gj",
}

func TestOpenSCADAlignmentParity(t *testing.T) {
	openscadPath, err := exec.LookPath("openscad")
	if err != nil {
		t.Fatalf("openscad not found in PATH: %v", err)
	}

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working dir: %v", err)
	}
	fontPath := filepath.Join(cwd, "test_data", "LiberationSans-Regular.ttf")
	fontBytes, err := os.ReadFile(fontPath)
	if err != nil {
		t.Fatalf("read font %s: %v", fontPath, err)
	}
	ttFont, err := ParseTTF(fontBytes)
	if err != nil {
		t.Fatalf("parse font: %v", err)
	}

	tempDir := t.TempDir()

	aligns := []Align{}
	for _, h := range []HAlign{HAlignLeft, HAlignCenter, HAlignRight} {
		for _, v := range []VAlign{VAlignBaseline, VAlignTop, VAlignCenter, VAlignBottom} {
			aligns = append(aligns, Align{HAlign: h, VAlign: v})
		}
	}

	rng := rand.New(rand.NewSource(1))

	for _, s := range testStrings {
		for _, align := range aligns {
			name := fmt.Sprintf("%s-%s-%s", sanitizeName(s), scadHAlign(align.HAlign), scadVAlign(align.VAlign))
			t.Run(name, func(t *testing.T) {
				opt := Options{
					Size:      textSize,
					CurveSegs: curveSegs,
					Align:     align,
					Kerning:   true,
					Spacing:   1,
				}

				outlines, err := TextOutlines(ttFont, s, opt)
				if err != nil {
					t.Fatalf("TextOutlines: %v", err)
				}
				if len(outlines) == 0 {
					t.Fatalf("no outlines produced")
				}
				textSolid2d := outlinesToSolid(outlines)
				if textSolid2d == nil {
					t.Fatalf("failed to build 2d solid")
				}

				stlPath, err := renderOpenSCAD(openscadPath, tempDir, fontPath, s, opt)
				if err != nil {
					t.Fatalf("OpenSCAD render: %v", err)
				}

				textSolid3d, err := solidFromSTL(stlPath)
				if err != nil {
					t.Fatalf("read STL: %v", err)
				}

				corr := containmentCorrelation(textSolid2d, textSolid3d, sampleCount, rng)
				if corr < correlationThreshold {
					if pngPath, err := rasterizeMismatch(textSolid2d, textSolid3d, s, align); err != nil {
						t.Logf("failed to rasterize mismatch: %v", err)
					} else {
						t.Logf("wrote mismatch raster: %s", pngPath)
					}
					t.Fatalf("correlation %.4f below threshold %.2f", corr, correlationThreshold)
				}
			})
		}
	}
}

func TestOpenSCADSpacingParity(t *testing.T) {
	openscadPath, err := exec.LookPath("openscad")
	if err != nil {
		t.Fatalf("openscad not found in PATH: %v", err)
	}

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working dir: %v", err)
	}
	fontPath := filepath.Join(cwd, "test_data", "LiberationSans-Regular.ttf")
	fontBytes, err := os.ReadFile(fontPath)
	if err != nil {
		t.Fatalf("read font %s: %v", fontPath, err)
	}
	ttFont, err := ParseTTF(fontBytes)
	if err != nil {
		t.Fatalf("parse font: %v", err)
	}

	opt := Options{
		Size:      textSize,
		CurveSegs: curveSegs,
		Align:     Align{HAlign: HAlignLeft, VAlign: VAlignBaseline},
		Kerning:   true,
		Spacing:   1.5,
	}

	outlines, err := TextOutlines(ttFont, "Hi", opt)
	if err != nil {
		t.Fatalf("TextOutlines: %v", err)
	}
	if len(outlines) == 0 {
		t.Fatalf("no outlines produced")
	}
	textSolid2d := outlinesToSolid(outlines)
	if textSolid2d == nil {
		t.Fatalf("failed to build 2d solid")
	}

	stlPath, err := renderOpenSCAD(openscadPath, t.TempDir(), fontPath, "Hi", opt)
	if err != nil {
		t.Fatalf("OpenSCAD render: %v", err)
	}
	textSolid3d, err := solidFromSTL(stlPath)
	if err != nil {
		t.Fatalf("read STL: %v", err)
	}

	rng := rand.New(rand.NewSource(2))
	corr := containmentCorrelation(textSolid2d, textSolid3d, sampleCount, rng)
	if corr < correlationThreshold {
		if pngPath, err := rasterizeMismatch(textSolid2d, textSolid3d, "Hi_spacing_1_5", opt.Align); err == nil {
			t.Logf("wrote mismatch raster: %s", pngPath)
		}
		t.Fatalf("correlation %.4f below threshold %.2f", corr, correlationThreshold)
	}
}

func renderOpenSCAD(openscadPath, tempDir, fontPath, text string, opt Options) (string, error) {
	scadPath := filepath.Join(tempDir, fmt.Sprintf("text_%s_%s_%s.scad", sanitizeName(text), scadHAlign(opt.Align.HAlign), scadVAlign(opt.Align.VAlign)))
	stlPath := strings.TrimSuffix(scadPath, ".scad") + ".stl"
	spacing := opt.Spacing
	if spacing == 0 {
		spacing = 1
	}

	scad := fmt.Sprintf(`linear_extrude(height=%g, center=false)
text(%q, size=%g, font="Liberation Sans:style=Regular", halign=%q, valign=%q, spacing=%g);
`, extrudeHeight, text, opt.Size, scadHAlign(opt.Align.HAlign), scadVAlign(opt.Align.VAlign), spacing)

	if err := os.WriteFile(scadPath, []byte(scad), 0o600); err != nil {
		return "", err
	}

	cmd := exec.Command(openscadPath, "-o", stlPath, scadPath)
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("OPENSCAD_FONT_PATH=%s", filepath.Dir(fontPath)),
		fmt.Sprintf("FONTCONFIG_PATH=%s", filepath.Dir(fontPath)),
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("openscad failed: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return stlPath, nil
}

func solidFromSTL(path string) (model3d.Solid, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	tris, err := model3d.ReadSTL(f)
	if err != nil {
		return nil, err
	}
	if len(tris) == 0 {
		return nil, fmt.Errorf("no triangles in STL")
	}

	mesh := model3d.NewMeshTriangles(tris)
	return mesh.Solid(), nil
}

func outlinesToSolid(outlines Outlines) model2d.Solid {
	if len(outlines) == 0 {
		return nil
	}
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
	if mesh.NumSegments() == 0 {
		return nil
	}
	return mesh.Solid()
}

func containmentCorrelation(s2 model2d.Solid, s3 model3d.Solid, samples int, rng *rand.Rand) float64 {
	min2 := s2.Min()
	max2 := s2.Max()
	min3 := s3.Min()
	max3 := s3.Max()

	min := model2d.Coord{
		X: math.Min(min2.X, min3.X),
		Y: math.Min(min2.Y, min3.Y),
	}
	max := model2d.Coord{
		X: math.Max(max2.X, max3.X),
		Y: math.Max(max2.Y, max3.Y),
	}
	z := (min3.Z + max3.Z) / 2

	match := 0
	for i := 0; i < samples; i++ {
		x := min.X + rng.Float64()*(max.X-min.X)
		y := min.Y + rng.Float64()*(max.Y-min.Y)
		p2 := model2d.Coord{X: x, Y: y}
		p3 := model3d.Coord3D{X: x, Y: y, Z: z}
		if s2.Contains(p2) == s3.Contains(p3) {
			match++
		}
	}

	return float64(match) / float64(samples)
}

func rasterizeMismatch(s2 model2d.Solid, s3 model3d.Solid, text string, align Align) (string, error) {
	min2 := s2.Min()
	max2 := s2.Max()
	min3 := s3.Min()
	max3 := s3.Max()

	min := model2d.Coord{
		X: math.Min(min2.X, min3.X),
		Y: math.Min(min2.Y, min3.Y),
	}
	max := model2d.Coord{
		X: math.Max(max2.X, max3.X),
		Y: math.Max(max2.Y, max3.Y),
	}
	z := (min3.Z + max3.Z) / 2

	containsOpenSCAD := func(c model2d.Coord) bool {
		return s3.Contains(model3d.Coord3D{X: c.X, Y: c.Y, Z: z})
	}

	onlyOpenSCAD := model2d.CheckedFuncSolid(min, max, func(c model2d.Coord) bool {
		return containsOpenSCAD(c) && !s2.Contains(c)
	})
	onlyOurs := model2d.CheckedFuncSolid(min, max, func(c model2d.Coord) bool {
		return s2.Contains(c) && !containsOpenSCAD(c)
	})
	overlap := model2d.CheckedFuncSolid(min, max, func(c model2d.Coord) bool {
		return s2.Contains(c) && containsOpenSCAD(c)
	})

	midY := (min.Y + max.Y) / 2
	flip := model2d.JoinedTransform{
		&model2d.Translate{Offset: model2d.XY(0, -midY)},
		&model2d.VecScale{Scale: model2d.XY(1, -1)},
		&model2d.Translate{Offset: model2d.XY(0, midY)},
	}
	onlyOpenSCAD = model2d.TransformSolid(flip, onlyOpenSCAD)
	onlyOurs = model2d.TransformSolid(flip, onlyOurs)
	overlap = model2d.TransformSolid(flip, overlap)

	base := fmt.Sprintf("mismatch_%s_%s_%s.png", sanitizeName(text), scadHAlign(align.HAlign), scadVAlign(align.VAlign))
	path := filepath.Join(".", base)
	err := model2d.RasterizeColor(path, []any{onlyOpenSCAD, onlyOurs, overlap}, []color.Color{
		color.RGBA{R: 0, G: 0, B: 255, A: 255},
		color.RGBA{R: 255, G: 0, B: 0, A: 255},
		color.RGBA{R: 0, G: 255, B: 0, A: 255},
	}, rasterScale)
	if err != nil {
		return "", err
	}
	return path, nil
}

func scadHAlign(a HAlign) string {
	switch a {
	case HAlignLeft:
		return "left"
	case HAlignCenter:
		return "center"
	case HAlignRight:
		return "right"
	default:
		panic("unknown HAlign")
	}
}

func scadVAlign(a VAlign) string {
	switch a {
	case VAlignBaseline:
		return "baseline"
	case VAlignTop:
		return "top"
	case VAlignCenter:
		return "center"
	case VAlignBottom:
		return "bottom"
	default:
		panic("unknown VAlign")
	}
}

func sanitizeName(s string) string {
	const maxLen = 24
	b := strings.Builder{}
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
		if b.Len() >= maxLen {
			break
		}
	}
	res := strings.Trim(b.String(), "_")
	if res == "" {
		return "text"
	}
	return res
}
