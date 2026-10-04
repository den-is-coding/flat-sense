// Command fixtures — сборка размеченного тестового набора (issue #58):
// читает manifest.tsv (индекс → путь фото из выгрузки парсера) и
// labels.tsv (индекс → plan|photo), копирует фото в
// testdata/fixtures/{plan,photo}/ и пишет manifest.json с ground truth.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type entry struct {
	PhotoRef string `json:"photo_ref"` // относительный путь в выгрузке парсера
	File     string `json:"file"`      // файл в fixtures
	Label    string `json:"label"`     // plan | photo
	Listing  string `json:"listing"`   // id объявления Авито
}

func main() {
	sheetManifest := flag.String("manifest", "", "manifest.tsv из cmd/sheet (idx -> путь)")
	labels := flag.String("labels", "", "labels.tsv (idx -> plan|photo)")
	srcRoot := flag.String("src", "", "корень выгрузки images/")
	dstDir := flag.String("out", "internal/floorplan/testdata/fixtures", "каталог fixtures")
	flag.Parse()
	if *sheetManifest == "" || *labels == "" || *srcRoot == "" {
		fmt.Fprintln(os.Stderr, "-manifest, -labels, -src обязательны")
		os.Exit(2)
	}

	refs := map[string]string{}
	readTSV(*sheetManifest, 2, func(f []string) { refs[f[0]] = f[1] })
	lab := map[string]string{}
	readTSV(*labels, 2, func(f []string) { lab[f[0]] = f[1] })

	var out []entry
	for idx, l := range lab {
		ref, ok := refs[idx]
		if !ok {
			fmt.Fprintf(os.Stderr, "нет фото для индекса %s\n", idx)
			os.Exit(1)
		}
		listing := strings.SplitN(ref, "/", 2)[0]
		base := filepath.Base(ref)
		dstRel := filepath.Join(l, listing+"_"+base)
		data, err := os.ReadFile(filepath.Join(*srcRoot, ref))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if err := os.MkdirAll(filepath.Join(*dstDir, l), 0o755); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if err := os.WriteFile(filepath.Join(*dstDir, dstRel), data, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		out = append(out, entry{PhotoRef: ref, File: dstRel, Label: l, Listing: listing})
	}
	blob, _ := json.MarshalIndent(out, "", "  ")
	if err := os.WriteFile(filepath.Join(*dstDir, "manifest.json"), blob, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("fixtures: %d фото -> %s\n", len(out), *dstDir)
}

func readTSV(path string, cols int, fn func([]string)) {
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		f := strings.Fields(line)
		if len(f) < cols {
			continue
		}
		fn(f)
	}
}
