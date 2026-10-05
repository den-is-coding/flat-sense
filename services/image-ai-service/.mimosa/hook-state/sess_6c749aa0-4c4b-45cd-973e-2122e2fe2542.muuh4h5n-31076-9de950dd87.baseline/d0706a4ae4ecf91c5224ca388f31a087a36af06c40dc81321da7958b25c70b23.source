// Package server — HTTP API image-ai-service (issue #58):
//
//	GET  /health                     — liveness
//	POST /api/detect                 — тело: изображение (jpeg/png); ответ: Verdict
//	POST /api/ads/check              — {adId, photos?: [url]}: проверить фото объявления
//	                                   (по умолчанию — images из avito_listings), пропуская
//	                                   уже проверенные; вердикты пишутся в ad_floor_plans
//	GET  /api/floor-plan?ad_id=123   — основная планировка объявления или null
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/yourusername/real-estate-analyzer/image-ai-service/internal/floorplan"
	"github.com/yourusername/real-estate-analyzer/image-ai-service/internal/storage"
)

const maxImageBytes = 20 << 20 // 20 МБ

// Server — обработчики API.
type Server struct {
	det     floorplan.Detector
	store   *storage.Storage
	fetcher *http.Client
}

func New(det floorplan.Detector, store *storage.Storage) *Server {
	return &Server{
		det:   det,
		store: store,
		fetcher: &http.Client{
			Timeout: 20 * time.Second,
		},
	}
}

// Register вешает маршруты на mux.
func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("OK"))
	})
	mux.HandleFunc("POST /api/detect", s.handleDetect)
	mux.HandleFunc("POST /api/ads/check", s.handleAdCheck)
	mux.HandleFunc("GET /api/floor-plan", s.handleFloorPlan)
}

// handleDetect — быстрый разовый прогон одного изображения без записи в БД.
func (s *Server) handleDetect(w http.ResponseWriter, r *http.Request) {
	img, err := decodeImage(r.Body)
	if err != nil {
		httpError(w, http.StatusBadRequest, "%v", err)
		return
	}
	writeJSON(w, http.StatusOK, s.det.Detect(img))
}

// checkRequest — тело POST /api/ads/check.
type checkRequest struct {
	AdID   int64    `json:"adId"`
	Photos []string `json:"photos,omitempty"` // необязательно; иначе images из БД
}

// checkResponse — результат проверки объявления.
type checkResponse struct {
	AdID    int64            `json:"adId"`
	Checked int              `json:"checked"` // проверено сейчас
	Skipped int              `json:"skipped"` // уже проверено ранее (не пересматривались)
	Plans   int              `json:"plans"`   // найдено планировок всего (включая прошлые прогоны)
	Verdict []storage.Record `json:"verdicts"`
}

// handleAdCheck — связка с объявлением: детекция всех непроверенных фото
// + запись вердиктов в ad_floor_plans (идемпотентно).
func (s *Server) handleAdCheck(w http.ResponseWriter, r *http.Request) {
	var req checkRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		httpError(w, http.StatusBadRequest, "bad json: %v", err)
		return
	}
	if req.AdID == 0 {
		httpError(w, http.StatusBadRequest, "adId is required")
		return
	}
	res, err := s.CheckAd(r.Context(), req.AdID, req.Photos)
	if err != nil {
		httpError(w, http.StatusBadGateway, "check ad %d: %v", req.AdID, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// CheckAd — ядро обработки объявления (переиспользуется CLI-режимом).
func (s *Server) CheckAd(ctx context.Context, adID int64, photos []string) (*checkResponse, error) {
	if len(photos) == 0 {
		var err error
		if photos, err = s.store.AdImages(ctx, adID); err != nil {
			return nil, err
		}
	}
	checked, err := s.store.CheckedRefs(ctx, adID)
	if err != nil {
		return nil, err
	}

	res := &checkResponse{AdID: adID}
	for _, ref := range photos {
		if checked[ref] {
			res.Skipped++
			continue
		}
		img, err := s.fetchImage(ctx, ref)
		if err != nil {
			log.Printf("ad %d: fetch %s: %v", adID, ref, err)
			continue // сбой одного фото не валит объявление
		}
		v := s.det.Detect(img)
		if err := s.store.SaveVerdict(ctx, adID, ref, v); err != nil {
			return nil, err
		}
		res.Checked++
		res.Verdict = append(res.Verdict, storage.Record{
			AdID: adID, PhotoRef: ref, IsPlan: v.IsPlan,
			Confidence: v.Confidence, Method: v.Method,
		})
	}
	plan, err := s.store.MainPlan(ctx, adID)
	if err != nil {
		return nil, err
	}
	if plan != nil {
		res.Plans = 1 // основная планировка найдена; точное число планов — через таблицу
	}
	return res, nil
}

// handleFloorPlan — GET /api/floor-plan?ad_id=123: основная планировка или null.
func (s *Server) handleFloorPlan(w http.ResponseWriter, r *http.Request) {
	var adID int64
	if _, err := fmt.Sscanf(r.URL.Query().Get("ad_id"), "%d", &adID); err != nil || adID == 0 {
		httpError(w, http.StatusBadRequest, "query param 'ad_id' (number) is required")
		return
	}
	plan, err := s.store.MainPlan(r.Context(), adID)
	if err != nil {
		httpError(w, http.StatusInternalServerError, "main plan: %v", err)
		return
	}
	if plan == nil {
		writeJSON(w, http.StatusOK, nil)
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

func (s *Server) fetchImage(ctx context.Context, ref string) (image.Image, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ref, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.fetcher.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	return decodeImage(io.LimitReader(resp.Body, maxImageBytes))
}

func decodeImage(rd io.Reader) (image.Image, error) {
	img, _, err := image.Decode(rd)
	if err != nil {
		return nil, fmt.Errorf("decode image: %w", err)
	}
	return img, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}

func httpError(w http.ResponseWriter, status int, format string, args ...any) {
	writeJSON(w, status, map[string]string{"error": fmt.Sprintf(format, args...)})
}
