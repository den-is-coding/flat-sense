// Command roomeval — подбор и замер фото-детектора меблировки (#110):
// считает признаки по fixtures, печатает CSV и confusion-матрицу.
package main

import (
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"image/jpeg"
	"os"
	"path/filepath"

	"github.com/yourusername/real-estate-analyzer/image-ai-service/internal/roomfurnishing"
)

func main() {
	fixtures := flag.String("fixtures", "internal/roomfurnishing/testdata/fixtures", "каталог fixtures")
	csvOut := flag.String("csv", "", "файл для CSV-дампа признаков")
	flag.Parse()

	var w *csv.Writer
	if *csvOut != "" {
		f, err := os.Create(*csvOut)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		defer f.Close()
		w = csv.NewWriter(f)
		w.Write([]string{"file", "label", "edge_density", "colorfulness", "sat_frac", "unique_colors", "light_frac", "flat_frac", "p_furnished"}) //nolint:errcheck
	}

	det := roomfurnishing.NewHeuristicDetector()
	tp, fp, tn, fn := 0, 0, 0, 0
	unknown := 0
	var misses []string
	for _, cls := range []string{"furnished", "unfurnished"} {
		entries, _ := os.ReadDir(filepath.Join(*fixtures, cls))
		for _, e := range entries {
			if filepath.Ext(e.Name()) != ".jpg" {
				continue
			}
			f, err := os.Open(filepath.Join(*fixtures, cls, e.Name()))
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			img, err := jpeg.Decode(f)
			f.Close()
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s: %v\n", e.Name(), err)
				os.Exit(1)
			}
			feat := roomfurnishing.Analyze(img)
			v := det.Detect(img)
			if w != nil {
				w.Write([]string{cls + "/" + e.Name(), cls,
					fmt.Sprintf("%.4f", feat.EdgeDensity), fmt.Sprintf("%.4f", feat.Colorfulness),
					fmt.Sprintf("%.4f", feat.SatFrac), fmt.Sprintf("%.1f", feat.UniqueColors),
					fmt.Sprintf("%.4f", feat.LightFrac), fmt.Sprintf("%.4f", feat.FlatFrac),
					fmt.Sprintf("%.4f", v.Confidence)})
			}
			switch {
			case v.Furnishing == "unknown":
				unknown++
				misses = append(misses, fmt.Sprintf("UNKNOWN %s p=%.2f", cls+"/"+e.Name(), v.Confidence))
			case cls == "furnished" && v.Furnishing == "furnished":
				tp++
			case cls == "furnished":
				fn++
				misses = append(misses, fmt.Sprintf("FN %s p=%.2f", cls+"/"+e.Name(), v.Confidence))
			case v.Furnishing == "unfurnished":
				tn++
			default:
				fp++
				misses = append(misses, fmt.Sprintf("FP %s p=%.2f", cls+"/"+e.Name(), v.Confidence))
			}
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
	fmt.Printf("всего=%d TP=%d FP=%d TN=%d FN=%d UNKNOWN=%d precision=%.3f recall=%.3f\n",
		tp+fp+tn+fn+unknown, tp, fp, tn, fn, unknown, precision, recall)
	for _, m := range misses {
		fmt.Println(m)
	}
	_ = json.Marshal
}
