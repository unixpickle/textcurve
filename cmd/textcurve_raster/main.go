package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/unixpickle/model3d/model2d"
	"github.com/unixpickle/textcurve"
)

func main() {
	fontPath := flag.String("font", "", "path to TTF/OTF font file")
	text := flag.String("text", "", "text to render")
	outPath := flag.String("out", "", "output image path (.png or .jpg)")
	size := flag.Float64("size", 10.0, "text size in model units")
	segs := flag.Int("segs", 16, "curve segments per quadratic")
	kerning := flag.Bool("kerning", true, "enable kerning")
	scale := flag.Float64("scale", 20.0, "pixels per model unit")
	flag.Parse()

	if *fontPath == "" || *text == "" || *outPath == "" {
		flag.Usage()
		os.Exit(2)
	}

	fontBytes, err := os.ReadFile(*fontPath)
	if err != nil {
		log.Fatalf("read font: %v", err)
	}
	font, err := textcurve.ParseTTF(fontBytes)
	if err != nil {
		log.Fatalf("parse font: %v", err)
	}

	outlines, err := textcurve.TextOutlines(font, *text, textcurve.Options{
		Size:      *size,
		CurveSegs: *segs,
		Align:     textcurve.Align{},
		Kerning:   *kerning,
	})
	if err != nil {
		log.Fatalf("TextOutlines: %v", err)
	}
	if len(outlines) == 0 {
		log.Fatalf("no outlines produced")
	}

	solid := outlinesToSolid(outlines)
	if solid == nil {
		log.Fatalf("failed to build solid")
	}

	if err := model2d.Rasterize(*outPath, solid, *scale); err != nil {
		log.Fatalf("rasterize: %v", err)
	}

	fmt.Printf("wrote %s\n", *outPath)
}

func outlinesToSolid(outlines textcurve.Outlines) model2d.Solid {
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
