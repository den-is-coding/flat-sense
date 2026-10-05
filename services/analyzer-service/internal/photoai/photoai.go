// Package photoai — адаптер PhotoFurnishingDetector поверх HTTP API
// image-ai-service (POST /api/furnishing, issue #110).
package photoai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/yourusername/real-estate-analyzer/analyzer-service/internal/evaluate"
)

// Client — детектор по фото через image-ai-service.
type Client struct {
	BaseURL string
	HTTP    *http.Client
}

func New(baseURL string) *Client {
	return &Client{BaseURL: baseURL, HTTP: &http.Client{Timeout: 10 * time.Second}}
}

type apiVerdict struct {
	Furnishing string  `json:"furnishing"`
	Confidence float64 `json:"confidence"`
}

// DetectByPhotos скачивает до 4 фото и агрегирует вердикты.
// «С мебелью» — только при КОНСЕНСУСЕ: ≥2 фото уверенно furnished
// (снятие надбавки — сильное действие, одиночный кадр не решает).
// Фото противоречат друг другу (и «меблированная», и «пустая») → unknown.
// Ошибки отдельных фото игнорируются; если не сработало ни одно — ошибка
// (вызывающая сторона деградирует в текстовый режим).
func (c *Client) DetectByPhotos(ctx context.Context, photoURLs []string) (evaluate.Furnishing, float64, error) {
	var (
		anyFurn, anyUnfurn float64
		furnCount          int
		seen               bool
	)
	limit := len(photoURLs)
	if limit > 4 {
		limit = 4
	}
	for _, u := range photoURLs[:limit] {
		v, err := c.detectOne(ctx, u)
		if err != nil {
			continue // одно фото не решает
		}
		seen = true
		switch v.Furnishing {
		case "furnished":
			furnCount++
			anyFurn = max(anyFurn, v.Confidence)
		case "unfurnished":
			anyUnfurn = max(anyUnfurn, v.Confidence)
		}
	}
	if !seen {
		return evaluate.Unknown, 0, fmt.Errorf("photo detector: no photo classified")
	}
	switch {
	case anyFurn > 0 && anyUnfurn == 0 && furnCount >= 2:
		return evaluate.Furnished, anyFurn, nil
	case anyUnfurn > 0 && anyFurn == 0:
		return evaluate.Unfurnished, anyUnfurn, nil
	default:
		return evaluate.Unknown, max(anyFurn, anyUnfurn), nil
	}
}

func (c *Client) detectOne(ctx context.Context, url string) (*apiVerdict, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch photo: status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
	if err != nil {
		return nil, err
	}
	apiReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(c.BaseURL, "/")+"/api/furnishing", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	apiResp, err := c.HTTP.Do(apiReq)
	if err != nil {
		return nil, err
	}
	defer apiResp.Body.Close()
	if apiResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("image-ai: status %d", apiResp.StatusCode)
	}
	var v apiVerdict
	if err := json.NewDecoder(apiResp.Body).Decode(&v); err != nil {
		return nil, err
	}
	return &v, nil
}

func max(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
