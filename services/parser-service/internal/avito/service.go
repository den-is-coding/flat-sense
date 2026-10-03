package avito

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"os"
	"strings"
	"sync"
	"time"
)

// ServiceConfig — поведение прогона парсинга.
type ServiceConfig struct {
	OnlyStudios  bool   // фаза поиска: сохранять и обогащать только студии
	MaxPages     int    // максимум страниц выдачи (0 = пока есть объявления, верхний предел 100)
	FetchDetails bool   // дополнительно запрашивать карточку каждого объявления
	SourceTask   string // метка задачи для трассировки записей в БД
}

// RunStats — итоги прогона.
type RunStats struct {
	RunID        int64          `json:"runId"`
	URL          string         `json:"url"`
	PagesFetched int            `json:"pagesFetched"`
	ItemsFound   int            `json:"itemsFound"`
	ItemsNew     int            `json:"itemsNew"`
	ItemsUpdated int            `json:"itemsUpdated"`
	Status       string         `json:"status"`
	Error        string         `json:"error,omitempty"`
	Duration     string         `json:"duration"`
	StartedAt    time.Time      `json:"startedAt"`
	Listings     []*ListingInfo `json:"listings,omitempty"` // краткая сводка (если запрошено)
}

// ListingInfo — краткое описание объявления для ответов API.
type ListingInfo struct {
	ID      int64  `json:"id"`
	Title   string `json:"title"`
	URL     string `json:"url"`
	Price   int64  `json:"price"`
	Address string `json:"address,omitempty"`
	IsNew   bool   `json:"isNew"`
}

// Service связывает клиент, хранилище и логику прогона.
type Service struct {
	client  *AvitoClient
	storage *Storage
	cfg     ServiceConfig
	rng     *rand.Rand
}

func NewService(client *AvitoClient, storage *Storage, cfg ServiceConfig) *Service {
	if cfg.MaxPages <= 0 {
		cfg.MaxPages = 100
	}
	return &Service{client: client, storage: storage, cfg: cfg, rng: rand.New(rand.NewSource(time.Now().UnixNano()))}
}

// RunParse обходит выдачу по фильтрам: страницы 1..N, по каждому объявлению
// опционально ходит в карточку и сохраняет всё в PostgreSQL.
func (s *Service) RunParse(ctx context.Context, f *SearchFilters, withListings bool) (*RunStats, error) {
	if err := f.Validate(); err != nil {
		return nil, err
	}
	start := time.Now()
	stats := &RunStats{StartedAt: start, Status: "ok"}

	if s.storage != nil {
		fj, _ := json.Marshal(f)
		runID, err := s.storage.CreateRun(ctx, fj)
		if err != nil {
			log.Printf("[avito] create run: %v", err)
		}
		stats.RunID = runID
	}

	defer func() {
		if s.storage != nil && stats.RunID > 0 {
			run := &ParseRun{
				ID: stats.RunID, RequestURL: stats.URL,
				PagesFetched: stats.PagesFetched, ItemsFound: stats.ItemsFound,
				ItemsNew: stats.ItemsNew, ItemsUpdated: stats.ItemsUpdated,
				Status: stats.Status, Error: stats.Error,
			}
			if err := s.storage.FinishRun(ctx, run); err != nil {
				log.Printf("[avito] finish run: %v", err)
			}
		}
		stats.Duration = time.Since(start).Round(time.Millisecond).String()
	}()

	var detailQueue []*Listing
	canonicalURL := ""
	for page := 1; page <= s.cfg.MaxPages; page++ {
		select {
		case <-ctx.Done():
			stats.Status = "partial"
			stats.Error = "context cancelled"
			return stats, ctx.Err()
		default:
		}

		pageURL := f.BuildURL(page)
		if canonicalURL != "" && page > 1 {
			// пагинация по каноническому URL первой страницы — без повторного редиректа
			pageURL = canonicalURL
			if q := pageURL + "&p=" + itoa(page); strings.Contains(pageURL, "?") {
				pageURL = q
			} else {
				pageURL = pageURL + "?p=" + itoa(page)
			}
		}
		if page == 1 {
			stats.URL = pageURL
		}
		var body []byte
		pageTries := 0
		for {
			if pageTries >= 3 {
				break
			}
			pageTries++
			b, finalURL, ferr := s.fetchFollowingRedirects(pageURL, 3)
			if ferr == nil && page == 1 && finalURL != pageURL {
				canonicalURL = finalURL
			}
			if ferr != nil {
				stats.Status = "partial"
				stats.Error = ferr.Error()
				if page == 1 && stats.PagesFetched == 0 && len(body) == 0 {
					return stats, ferr
				}
				body = nil
				break
			}
			if bytes.Contains(b, []byte("data-mfe-state")) || bytes.Contains(b, []byte("__initialData__")) {
				body = b
				break
			}
			// статус 200, но JSON-состояний нет: софт-блок Qrator — сохраняем
			// образец и повторяем через другой порт
			log.Printf("[avito] страница без JSON-состояний (%d байт) — софт-блок, попытка %d", len(b), pageTries)
			_ = os.WriteFile(fmt.Sprintf("/tmp/softblock-p%d-t%d.html", page, pageTries), b, 0o644)
			continue
		}
		if len(body) == 0 {
			if stats.PagesFetched == 0 {
				stats.Status = "partial"
				stats.Error = "page 1: soft-blocked on all tries"
				return stats, fmt.Errorf("page 1: soft-blocked")
			}
			break
		}
		stats.PagesFetched = page

		listings, err := ParseSearchPage(string(body), f)
		if err != nil {
			stats.Status = "partial"
			stats.Error = err.Error()
			if page == 1 {
				return stats, err
			}
			break
		}
		if len(listings) == 0 {
			break // выдача кончилась
		}
		stats.ItemsFound += len(listings)

		// Фаза 1: сразу сохраняем данные выдачи — они уже полноценны
		// (цена/комнаты/площадь/этаж/гео/продавец), фоновые потери не страшны.
		for _, l := range listings {
			if err := ctx.Err(); err != nil {
				stats.Status = "partial"
				stats.Error = "context cancelled"
				return stats, err
			}
			l.SourceTask = s.cfg.SourceTask
			if s.cfg.OnlyStudios && !l.Studio {
				continue // фильтр «только студии» на фазе поиска
			}
			if s.storage != nil {
				isNew, err := s.storage.UpsertListing(ctx, l)
				if err != nil {
					log.Printf("[avito] upsert %d: %v", l.ID, err)
					continue
				}
				if isNew {
					stats.ItemsNew++
				} else {
					stats.ItemsUpdated++
				}
			}
			detailQueue = append(detailQueue, l)
			if withListings {
				stats.Listings = append(stats.Listings, &ListingInfo{
					ID: l.ID, Title: l.Title, URL: l.URL, Price: l.Price, Address: l.Address, IsNew: l.IsNew,
				})
			}
		}
	}

	// Фаза 2: параллельное обогащение карточками (воркер на порт пула).
	if s.cfg.FetchDetails && len(detailQueue) > 0 {
		done := s.enrichParallel(ctx, detailQueue)
		for _, l := range detailQueue {
			if done[l.ID] {
				if s.storage != nil {
					if _, err := s.storage.UpsertListing(ctx, l); err != nil {
						log.Printf("[avito] upsert detail %d: %v", l.ID, err)
					}
				}
			}
		}
	}

	if stats.Status == "ok" && stats.PagesFetched > 0 && stats.ItemsFound == 0 {
		// Страницы отдались, но объявлений нет: пустой фильтр или «отравленный» ответ.
		stats.Status = "partial"
		stats.Error = "no listings found on fetched pages"
	}
	return stats, nil
}

// enrichDetail дополняет данные выдачи данными карточки (описание, продавец,
// счётчики, полный набор параметров).
// fetchFollowingRedirects получает страницу и, если Авито вернул SSR-заглушку
// с редиректом на канонический URL (фильтры упаковываются в слаг f=),
// переходит по ней. Возвращает (тело, финальный URL) — финальный URL
// используется для пагинации без повторных редиректов.
func (s *Service) fetchFollowingRedirects(u string, maxHops int) ([]byte, string, error) {
	body, err := s.client.Fetch(u)
	if err != nil {
		return nil, "", err
	}
	for hop := 0; hop < maxHops; hop++ {
		rd := SearchRedirect(string(body))
		if rd == "" {
			return body, u, nil
		}
		log.Printf("[avito] ssr-redirect -> %s", rd)
		u = absoluteURL(rd)
		if body, err = s.client.Fetch(u); err != nil {
			return nil, "", err
		}
	}
	return body, u, nil
}

func (s *Service) enrichDetail(ctx context.Context, l *Listing) error {
	if l.URL == "" {
		return fmt.Errorf("listing %d has no url", l.ID)
	}
	body, _, err := s.fetchFollowingRedirects(l.URL, 2)
	if err != nil {
		return err
	}
	full, err := ParseItemPage(string(body))
	if err != nil {
		return err
	}
	mergeDetails(l, full)
	return nil
}

// mergeDetails переносит поля карточки в listing с сохранением данных выдачи.
func mergeDetails(l *Listing, full *Listing) {
	if full == nil || full.ID == 0 {
		return
	}
	if full.Title != "" {
		l.Title = full.Title
	}
	l.Description = full.Description
	if full.Price > 0 {
		l.Price = full.Price
	}
	if full.PricePerM2 > 0 {
		l.PricePerM2 = full.PricePerM2
		l.PriceUnit = full.PriceUnit
	}
	if len(full.PriceMeta) > 0 {
		l.PriceMeta = full.PriceMeta
	}
	if full.Rooms > 0 {
		l.Rooms = full.Rooms
	}
	if full.Studio {
		l.Studio = true
	}
	if full.TotalArea > 0 {
		l.TotalArea = full.TotalArea
	}
	if full.LivingArea > 0 {
		l.LivingArea = full.LivingArea
	}
	if full.KitchenArea > 0 {
		l.KitchenArea = full.KitchenArea
	}
	if full.LandArea > 0 {
		l.LandArea = full.LandArea
	}
	if full.Floor > 0 {
		l.Floor = full.Floor
	}
	if full.FloorsTotal > 0 {
		l.FloorsTotal = full.FloorsTotal
	}
	setStr := func(dst *string, src string) {
		if src != "" {
			*dst = src
		}
	}
	setStr(&l.HouseType, full.HouseType)
	setStr(&l.Renovation, full.Renovation)
	setStr(&l.Balcony, full.Balcony)
	setStr(&l.Bathroom, full.Bathroom)
	if full.YearBuilt > 0 {
		l.YearBuilt = full.YearBuilt
	}
	setStr(&l.Address, full.Address)
	setStr(&l.Region, full.Region)
	setStr(&l.City, full.City)
	setStr(&l.District, full.District)
	setStr(&l.Metro, full.Metro)
	if full.Lat != 0 {
		l.Lat = full.Lat
	}
	if full.Lng != 0 {
		l.Lng = full.Lng
	}
	if len(full.Geo) > 0 {
		l.Geo = full.Geo
	}
	if full.SellerName != "" {
		l.SellerName = full.SellerName
	}
	if full.SellerType != "" {
		l.SellerType = full.SellerType
	}
	if full.SellerURL != "" {
		l.SellerURL = full.SellerURL
	}
	if full.SellerRating > 0 {
		l.SellerRating = full.SellerRating
	}
	if len(full.Seller) > 0 {
		l.Seller = full.Seller
	}
	if len(full.Images) > 0 {
		l.Images = full.Images
		l.ImageCount = full.ImageCount
	}
	if full.Views > 0 {
		l.Views = full.Views
	}
	if full.Contacts > 0 {
		l.Contacts = full.Contacts
	}
	if full.Favorites > 0 {
		l.Favorites = full.Favorites
	}
	if !full.PublishedAt.IsZero() {
		l.PublishedAt = full.PublishedAt
	}
	if !full.RefreshedAt.IsZero() {
		l.RefreshedAt = full.RefreshedAt
	}
	if len(full.Params) > 0 {
		l.Params = full.Params
	}
	if len(full.Raw) > 0 {
		l.Raw = full.Raw
	}
}

// ParseOne разбирает одну карточку объявления и сохраняет её.
func (s *Service) ParseOne(ctx context.Context, itemURL string) (*Listing, error) {
	body, err := s.client.Fetch(itemURL)
	if err != nil {
		return nil, err
	}
	l, err := ParseItemPage(string(body))
	if err != nil {
		return nil, err
	}
	// категорию/тип сделки выводим из URL карточки (/moskva/kvartiry/..._prodam_...)
	l.Category, l.DealType = inferCategoryDeal(itemURL)
	if s.storage != nil {
		if _, err := s.storage.UpsertListing(ctx, l); err != nil {
			return l, err
		}
	}
	return l, nil
}

// inferCategoryDeal определяет категорию и тип сделки по сегментам URL.
func inferCategoryDeal(u string) (string, DealKind) {
	for _, c := range []Category{CatFlats, CatRooms, CatHouses, CatLand, CatGarages, CatCommercial} {
		if containsSegment(u, string(c)) {
			kind := KindSale
			switch {
			case containsSegment(u, string(DealRentDaily)):
				kind = KindRentDaily
			case containsSegment(u, string(DealRentLong)), containsSegment(u, string(DealRentPlain)):
				kind = KindRentLong
			}
			return string(c), kind
		}
	}
	return "", KindSale
}

func containsSegment(u, seg string) bool {
	for _, p := range splitPath(u) {
		if p == seg {
			return true
		}
	}
	return false
}

func splitPath(u string) []string {
	var out []string
	cur := ""
	for _, r := range u {
		if r == '/' {
			if cur != "" {
				out = append(out, cur)
			}
			cur = ""
			continue
		}
		if r == '_' || r == '-' || r == '.' {
			if cur != "" {
				out = append(out, cur)
			}
			cur = ""
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func sleepJitter(r *rand.Rand, min, max time.Duration) {
	if max <= min {
		time.Sleep(min)
		return
	}
	time.Sleep(min + time.Duration(r.Int63n(int64(max-min))))
}

// ParseFiltersJSON — хелпер для API/CLI: фильтры из JSON.
func ParseFiltersJSON(data []byte) (*SearchFilters, error) {
	var f SearchFilters
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("bad filters json: %w", err)
	}
	return &f, nil
}

// String — человекочитаемое описание фильтров (для логов).
func (f *SearchFilters) String() string {
	return fmt.Sprintf("city=%s category=%s deal=%s page=%d pmin=%d pmax=%d rooms=%v sort=%s f=%s",
		f.City, f.Category, f.Deal, f.Page, f.PriceMin, f.PriceMax, f.Rooms, f.Sort, f.Advanced)
}

// enrichParallel обогащает объявления карточками параллельно: по воркеру
// на порт пула, каждый дёргает свой порт (согласованные куки/IP на порт).
func (s *Service) enrichParallel(ctx context.Context, listings []*Listing) map[int64]bool {
	n := s.client.PortCount()
	if n > 6 {
		n = 6 // бережём шлюз: не более 6 параллельных подключений
	}
	if n < 1 {
		n = 1
	}
	queue := make(chan *Listing)
	done := make(map[int64]bool)
	var mu sync.Mutex
	var wg sync.WaitGroup

	for w := 0; w < n; w++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			for l := range queue {
				if ctx.Err() != nil {
					return
				}
				body, err := s.client.FetchViaPort(idx, l.URL)
				if err != nil {
					log.Printf("[avito] detail %d via port %d: %v", l.ID, idx, err)
					sleepJitter(s.rng, 2*time.Second, 5*time.Second)
					continue
				}
				full, err := ParseItemPage(string(body))
				if err != nil {
					log.Printf("[avito] detail %d parse: %v", l.ID, err)
					continue
				}
				mergeDetails(l, full)
				mu.Lock()
				done[l.ID] = true
				mu.Unlock()
			}
		}(w)
	}
	for _, l := range listings {
		queue <- l
	}
	close(queue)
	wg.Wait()
	return done
}

func itoa(n int) string { return fmt.Sprintf("%d", n) }
