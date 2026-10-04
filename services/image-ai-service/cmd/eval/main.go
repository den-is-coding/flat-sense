// Command eval — тюнинг детектора планировок (issue #58):
// считает Metrics для всех фото fixtures, печатает CSV и итог
// текущего классификатора (precision/recall, список ошибок).
package main

import (
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"image/jpeg"
	"os"
	"path/filepath"

	"github.com/yourusername/real-estate-analyzer/image-ai-service/internal/floorplan"
)

type manifestEntry struct {
	PhotoRef string `json:"photo_ref"`
	File     string `json:"file"`
	Label    string `json:"label"`
	Listing  string `json:"listing"`
}

func main() {
	fixtures := flag.String("fixtures", "internal/floorplan/testdata/fixtures", "каталог fixtures с manifest.json")
	csvOut := flag.String("csv", "", "файл для дампа метрик (CSV)")
	flag.Parse()

	blob, err := os.ReadFile(filepath.Join(*fixtures, "manifest.json"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	var entries []manifestEntry
	if err := json.Unmarshal(blob, &entries); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	var w *csv.Writer
	if *csvOut != "" {
		f, err := os.Create(*csvOut)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		defer f.Close()
		w = csv.NewWriter(f)
		w.Write([]string{"file", "label", "light_frac", "dark_frac", "colorfulness", "sat_frac",
			"unique_colors", "edge_frac", "straight_frac", "noise", "p_plan"})
	}

	det := floorplan.NewHeuristicDetector()
	tp, fp, tn, fn := 0, 0, 0, 0
	var misses []string
	for _, e := range entries {
		f, err := os.Open(filepath.Join(*fixtures, e.File))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		img, err := jpeg.Decode(f)
		f.Close()
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", e.File, err)
			os.Exit(1)
		}
		m := floorplan.Analyze(img)
		v := det.Detect(img)
		if w != nil {
			w.Write([]string{e.File, e.Label,
				fmt.Sprintf("%.4f", m.LightFrac), fmt.Sprintf("%.4f", m.DarkFrac),
				fmt.Sprintf("%.4f", m.Colorfulness), fmt.Sprintf("%.4f", m.SatFrac),
				fmt.Sprintf("%.1f", m.UniqueColors), fmt.Sprintf("%.4f", m.EdgeFrac),
				fmt.Sprintf("%.4f", m.StraightFrac), fmt.Sprintf("%.3f", m.Noise),
				fmt.Sprintf("%.4f", v.Confidence)})
		}
		want := e.Label == "plan"
		got := v.IsPlan
		switch {
		case want && got:
			tp++
		case !want && got:
			fp++
			misses = append(misses, fmt.Sprintf("FP %s p=%.2f %s", e.File, v.Confidence, m))
		case want && !got:
			fn++
			misses = append(misses, fmt.Sprintf("FN %s p=%.2f %s", e.File, v.Confidence, m))
		default:
			tn++
		}
	}
	if w != nil {
		w.Flush()
	}
	precision := 0.0
	if tp+fp > 0 {
		precision = float64(tp) / float64(tp+fp)
	}
	recall := 0.0
	if tp+fn > 0 {
		recall = float64(tp) / float64(tp+fn)
	}
	fmt.Printf("всего=%d TP=%d FP=%d TN=%d FN=%d precision=%.3f recall=%.3f\n",
		len(entries), tp, fp, tn, fn, precision, recall)
	for _, m := range misses {
		fmt.Println(m)
	}
}
