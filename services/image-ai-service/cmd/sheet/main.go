// Command sheet — одноразовый инструмент разметки (issue #58):
// строит контакт-листы всех фото из каталога выгрузки парсера
// (images/<listing>/<nnn>_<w>x<h>.jpg) с порядковыми индексами,
// чтобы вручную разметить план/не-план и собрать fixtures.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"sort"

	xdraw "golang.org/x/image/draw"
)

const (
	cell     = 180 // размер клетки
	thumbPad = 2
	cols     = 10
	rows     = 7
)

func main() {
	root := flag.String("root", "", "каталог images/<listing>/*.jpg")
	out := flag.String("out", "sheets", "каталог для листов и manifest.tsv")
	flag.Parse()
	if *root == "" {
		fmt.Fprintln(os.Stderr, "-root обязателен")
		os.Exit(2)
	}

	var photos []string
	entries, err := os.ReadDir(*root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		files, _ := filepath.Glob(filepath.Join(*root, e.Name(), "*.jpg"))
		sort.Strings(files)
		photos = append(photos, files...)
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	// manifest.tsv: idx -> путь относительно root.
	mf, err := os.Create(filepath.Join(*out, "manifest.tsv"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	mw := bufio.NewWriter(mf)
	for i, p := range photos {
		rel, _ := filepath.Rel(*root, p)
		fmt.Fprintf(mw, "%d\t%s\n", i+1, rel)
	}
	mw.Flush()
	mf.Close()

	// Листы: сетка cell x cell, в каждой — миниатюра и индекс.
	per := cols * rows
	for sheetIdx := 0; sheetIdx*per < len(photos); sheetIdx++ {
		img := image.NewRGBA(image.Rect(0, 0, cols*cell, rows*cell))
		start := sheetIdx * per
		for k := 0; k < per && start+k < len(photos); k++ {
			cx, cy := (k%cols)*cell, (k/cols)*cell
			f, err := os.Open(photos[start+k])
			if err != nil {
				fmt.Println("open:", err)
				continue
			}
			src, err := jpeg.Decode(f)
			f.Close()
			if err != nil {
				fmt.Println("decode:", photos[start+k], err)
				continue
			}
			sb := src.Bounds()
			s := float64(cell-2*thumbPad) / float64(max(sb.Dx(), sb.Dy()))
			dw, dh := max(1, int(float64(sb.Dx())*s)), max(1, int(float64(sb.Dy())*s))
			dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
			xdraw.ApproxBiLinear.Scale(dst, dst.Bounds(), src, sb, xdraw.Src, nil)
			draw.Draw(img, image.Rect(cx+thumbPad, cy+thumbPad, cx+thumbPad+dw, cy+thumbPad+dh),
				dst, image.Point{}, draw.Src)
			drawNum(img, start+k+1, cx+4, cy+4)
		}
		// Серые линии сетки.
		grid := color.RGBA{200, 200, 200, 255}
		for x := 0; x <= cols; x++ {
			vline(img, x*cell, grid)
		}
		for y := 0; y <= rows; y++ {
			hline(img, y*cell, grid)
		}

		name := fmt.Sprintf("sheet-%02d.png", sheetIdx+1)
		outf, err := os.Create(filepath.Join(*out, name))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if err := png.Encode(outf, img); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		outf.Close()
		fmt.Printf("%s: индексы %d..%d\n", name, start+1, min(start+per, len(photos)))
	}
	fmt.Printf("всего фото: %d, manifest: %s\n", len(photos), filepath.Join(*out, "manifest.tsv"))
}

// digit3x5 — пиксельные глифы цифр 0-9 (5 строк по 3 бита).
var digit3x5 = [10][5]byte{
	{0b111, 0b101, 0b101, 0b101, 0b111}, // 0
	{0b010, 0b110, 0b010, 0b010, 0b111}, // 1
	{0b111, 0b001, 0b111, 0b100, 0b111}, // 2
	{0b111, 0b001, 0b111, 0b001, 0b111}, // 3
	{0b101, 0b101, 0b111, 0b001, 0b001}, // 4
	{0b111, 0b100, 0b111, 0b001, 0b111}, // 5
	{0b111, 0b100, 0b111, 0b101, 0b111}, // 6
	{0b111, 0b001, 0b010, 0b010, 0b010}, // 7
	{0b111, 0b101, 0b111, 0b101, 0b111}, // 8
	{0b111, 0b101, 0b111, 0b001, 0b111}, // 9
}

// drawNum рисует число в левом верхнем углу: красные пиксели 3x5,
// увеличенные x3, с белой подложкой для читаемости.
func drawNum(img *image.RGBA, num, x0, y0 int) {
	var digits []byte
	for _, c := range fmt.Sprintf("%d", num) {
		digits = append(digits, byte(c-'0'))
	}
	const scale = 3
	const dw = 3 * scale
	// Подложка.
	for y := 0; y < 5*scale+2; y++ {
		for x := 0; x < len(digits)*(dw+scale)+scale; x++ {
			img.Set(x0+x, y0+y, color.RGBA{255, 255, 255, 255})
		}
	}
	red := color.RGBA{220, 0, 0, 255}
	ox := x0 + scale
	for di, d := range digits {
		g := digit3x5[d]
		for row := 0; row < 5; row++ {
			for col := 0; col < 3; col++ {
				if g[row]&(0b100>>col) == 0 {
					continue
				}
				for sy := 0; sy < scale; sy++ {
					for sx := 0; sx < scale; sx++ {
						img.Set(ox+di*(dw+scale)+col*scale+sx, y0+scale+row*scale+sy, red)
					}
				}
			}
		}
	}
}

func vline(img *image.RGBA, x int, c color.RGBA) {
	for y := 0; y < img.Bounds().Dy(); y++ {
		img.Set(x, y, c)
	}
}

func hline(img *image.RGBA, y int, c color.RGBA) {
	for x := 0; x < img.Bounds().Dx(); x++ {
		img.Set(x, y, c)
	}
}
