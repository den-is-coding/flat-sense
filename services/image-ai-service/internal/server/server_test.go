package server

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/yourusername/real-estate-analyzer/image-ai-service/internal/floorplan"
)

// store не нужен для /health и /api/detect — передаём nil.
func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	s := New(floorplan.NewHeuristicDetector(), nil)
	mux := http.NewServeMux()
	s.Register(mux)
	return httptest.NewServer(mux)
}

func TestHandleDetect(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	// Чертёжная заготовка: белая подложка + тонкие тёмные линии.
	img := image.NewRGBA(image.Rect(0, 0, 200, 150))
	for y := 0; y < 150; y++ {
		for x := 0; x < 200; x++ {
			img.SetRGBA(x, y, color.RGBA{250, 250, 250, 255})
		}
	}
	for x := 0; x < 200; x++ {
		img.SetRGBA(x, 40, color.RGBA{30, 30, 30, 255})
		img.SetRGBA(x, 110, color.RGBA{30, 30, 30, 255})
	}
	for y := 0; y < 150; y++ {
		img.SetRGBA(60, y, color.RGBA{30, 30, 30, 255})
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}

	resp, err := http.Post(ts.URL+"/api/detect", "image/jpeg", &buf)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var v floorplan.Verdict
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		t.Fatal(err)
	}
	if v.Method != floorplan.MethodHeuristic {
		t.Errorf("method = %q", v.Method)
	}
	if v.Confidence < 0 || v.Confidence > 1 {
		t.Errorf("confidence вне 0..1: %v", v.Confidence)
	}
}

func TestHandleDetectBadBody(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/detect", "image/jpeg", bytes.NewReader([]byte("not an image")))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestHandleFloorPlanRequiresAdID(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/floor-plan")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}
