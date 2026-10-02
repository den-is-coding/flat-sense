package avito

import (
	"errors"
	"fmt"
	"math/rand"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
	tlsclient "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
)

// Коды ответов, означающие блок антибота (Qrator/Curator).
var blockStatuses = map[int]bool{403: true, 429: true, 439: true}

// Признаки блокировки/капчи в теле ответа (статус может быть и 200).
var blockMarkers = regexp.MustCompile(
	`(?i)Доступ ограничен|Antibot Challenge Page|Доступ временно ограничен|подтвердите, что запросы отправляли вы`)

// Ошибки, которые возвращает клиент.
var (
	ErrBlocked  = errors.New("avito: all proxies blocked / captcha, aborting")
	ErrNotFound = errors.New("avito: page not found (404)")
)

// ClientConfig — настройка клиента.
type ClientConfig struct {
	// Proxies — список прокси вида "http://user:pass@host:port" (или без авторизации).
	// Использовать только российские резидентные/мобильные прокси: датацентр-IP
	// и зарубежные Авито блокирует почти сразу.
	Proxies []string

	// ProxyRotationURL — необязательный URL смены выходного IP (link-прокси
	// мобильных операторов); вызывается при блоке текущего прокси.
	ProxyRotationURL string

	// CookieString — стартовые куки для avito.ru (например, снятые из браузера
	// с тем же выходным IP; ключевая — qrator_jsid). Формат: "k1=v1; k2=v2".
	CookieString string

	MinDelay time.Duration // минимальная пауза между запросами (default 2s)
	MaxDelay time.Duration // максимальная пауза (default 6s)

	LongPauseEvery int           // длинная пауза каждые N запросов (default 20)
	LongPauseMin   time.Duration // нижняя граница длинной паузы (default 15s)
	LongPauseMax   time.Duration // верхняя (default 30s)

	MaxRotations   int           // максимум ротаций прокси при блоках на один запрос (default 8)
	BlockedCool    time.Duration // охлаждение прокси после блока (default 90s)
	TimeoutSeconds int           // http-таймаут (default 45)

	InsecureSkipVerify bool
}

// AvitoClient — HTTP-клиент с TLS-имперсонацией, пулом прокси и
// rotate-until-clean ретраями на 403/429/439.
type AvitoClient struct {
	cfg  ClientConfig
	pool []*proxyEntry
	mu   sync.Mutex
	cur  int
	rng  *rand.Rand

	totalReqs int
}

type proxyEntry struct {
	name         string // url прокси или "direct"
	client       tlsclient.HttpClient
	blockedUntil time.Time
	warmedUp     bool
}

// NewClient создаёт клиент. Каждый прокси получает собственный tls-client
// с изолированной cookie jar (куки Авито привязаны к IP).
func NewClient(cfg ClientConfig) (*AvitoClient, error) {
	if cfg.MinDelay <= 0 {
		cfg.MinDelay = 2 * time.Second
	}
	if cfg.MaxDelay < cfg.MinDelay {
		cfg.MaxDelay = cfg.MinDelay + 3*time.Second
	}
	if cfg.LongPauseEvery <= 0 {
		cfg.LongPauseEvery = 20
	}
	if cfg.LongPauseMin <= 0 {
		cfg.LongPauseMin = 15 * time.Second
	}
	if cfg.LongPauseMax < cfg.LongPauseMin {
		cfg.LongPauseMax = cfg.LongPauseMin + 15*time.Second
	}
	if cfg.MaxRotations <= 0 {
		cfg.MaxRotations = 8
	}
	if cfg.BlockedCool <= 0 {
		cfg.BlockedCool = 90 * time.Second
	}
	if cfg.TimeoutSeconds <= 0 {
		cfg.TimeoutSeconds = 45
	}

	c := &AvitoClient{cfg: cfg, rng: rand.New(rand.NewSource(time.Now().UnixNano()))}

	if len(cfg.Proxies) == 0 {
		pc, err := newProxyEntry("direct", "", cfg)
		if err != nil {
			return nil, err
		}
		c.pool = append(c.pool, pc)
	} else {
		for _, p := range cfg.Proxies {
			pc, err := newProxyEntry(p, p, cfg)
			if err != nil {
				return nil, fmt.Errorf("proxy %s: %w", maskProxy(p), err)
			}
			c.pool = append(c.pool, pc)
		}
	}
	return c, nil
}

func newProxyEntry(name, proxy string, cfg ClientConfig) (*proxyEntry, error) {
	jar := tlsclient.NewCookieJar()
	opts := []tlsclient.HttpClientOption{
		tlsclient.WithClientProfile(pickProfile().tls),
		tlsclient.WithCookieJar(jar),
		tlsclient.WithTimeoutSeconds(cfg.TimeoutSeconds),
	}
	if proxy != "" {
		opts = append(opts, tlsclient.WithProxyUrl(proxy))
	}
	if cfg.InsecureSkipVerify {
		opts = append(opts, tlsclient.WithInsecureSkipVerify())
	}
	cl, err := tlsclient.NewHttpClient(tlsclient.NewNoopLogger(), opts...)
	if err != nil {
		return nil, err
	}
	if cfg.CookieString != "" {
		u, _ := url.Parse("https://www.avito.ru")
		cl.SetCookies(u, parseCookieString(cfg.CookieString))
	}
	return &proxyEntry{name: name, client: cl}, nil
}

// browserProfile — согласованные набор TLS-профиля и заголовков десктопного Chrome.
type browserProfile struct {
	tls     profiles.ClientProfile
	version string
	ua      string
	secChUa string
}

var chromeProfiles = []struct {
	profile profiles.ClientProfile
	version string
}{
	{profiles.Chrome_146, "146"},
	{profiles.Chrome_150, "150"},
	{profiles.Chrome_152, "152"},
}

func pickProfile() browserProfile {
	p := chromeProfiles[rand.Intn(len(chromeProfiles))]
	ua := fmt.Sprintf("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/%s.0.0.0 Safari/537.36", p.version)
	secCh := fmt.Sprintf(`"Chromium";v="%s", "Google Chrome";v="%s", "Not-A.Brand";v="99"`, p.version, p.version)
	return browserProfile{tls: p.profile, version: p.version, ua: ua, secChUa: secCh}
}

// Fetch выполняет GET с обходом блокировок: пауза -> прогрев -> запрос;
// на 403/429/439 или капчу в теле — охлаждает прокси, (опционально) меняет IP
// и повторяет на следующем прокси, пока не получит чистый ответ.
func (c *AvitoClient) Fetch(target string) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt < c.cfg.MaxRotations; attempt++ {
		pc, prof := c.pick()
		if pc == nil {
			return nil, ErrBlocked
		}
		c.waitTurn(pc)
		if err := c.warmUp(pc, prof); err != nil {
			lastErr = err
			c.cooldown(pc)
			continue
		}
		body, status, err := c.doGet(pc, prof, target)
		if err != nil {
			lastErr = fmt.Errorf("%s: request: %w", maskProxy(pc.name), err)
			c.cooldown(pc)
			continue
		}
		if status == 404 {
			return nil, ErrNotFound
		}
		if blockStatuses[status] || blockMarkers.Match(body) {
			lastErr = fmt.Errorf("%s: blocked (status %d)", maskProxy(pc.name), status)
			c.cooldown(pc)
			continue
		}
		if status != 200 {
			lastErr = fmt.Errorf("%s: unexpected status %d", maskProxy(pc.name), status)
			c.cooldown(pc)
			continue
		}
		return body, nil
	}
	if lastErr == nil {
		lastErr = ErrBlocked
	}
	return nil, fmt.Errorf("%w: last error: %v", ErrBlocked, lastErr)
}

// pick выбирает следующий доступный (не в блоке) прокси из пула.
func (c *AvitoClient) pick() (*proxyEntry, browserProfile) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	n := len(c.pool)
	for i := 0; i < n; i++ {
		pc := c.pool[(c.cur+i)%n]
		if now.After(pc.blockedUntil) {
			c.cur = (c.cur + i + 1) % n
			return pc, pickProfile()
		}
	}
	return nil, browserProfile{}
}

// waitTurn соблюдает паузы между запросами: межстраничная задержка с джиттером
// и удлинённая пауза каждые LongPauseEvery запросов.
func (c *AvitoClient) waitTurn(pc *proxyEntry) {
	c.mu.Lock()
	n := c.totalReqs
	c.totalReqs++
	c.mu.Unlock()

	var d time.Duration
	if n > 0 && n%c.cfg.LongPauseEvery == 0 {
		d = c.cfg.LongPauseMin + time.Duration(c.rng.Int63n(int64(c.cfg.LongPauseMax-c.cfg.LongPauseMin)))
	} else {
		d = c.cfg.MinDelay + time.Duration(c.rng.Int63n(int64(c.cfg.MaxDelay-c.cfg.MinDelay)))
	}
	time.Sleep(d)
}

// warmUp один раз на прокси запрашивает главную, чтобы получить стартовые куки
// (sessid, u, v, avito_device_id и пр.).
func (c *AvitoClient) warmUp(pc *proxyEntry, prof browserProfile) error {
	if pc.warmedUp {
		return nil
	}
	_, status, err := c.doGet(pc, prof, "https://www.avito.ru/")
	if err != nil {
		return err
	}
	if blockStatuses[status] {
		return fmt.Errorf("warmup blocked (status %d)", status)
	}
	pc.warmedUp = true
	return nil
}

func (c *AvitoClient) doGet(pc *proxyEntry, prof browserProfile, target string) ([]byte, int, error) {
	req, err := fhttp.NewRequest(fhttp.MethodGet, target, nil)
	if err != nil {
		return nil, 0, err
	}
	applyHeaders(req, prof)
	resp, err := pc.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	buf := make([]byte, 0, 512*1024)
	tmp := make([]byte, 64*1024)
	for {
		n, rerr := resp.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if rerr != nil {
			break
		}
		if len(buf) > 32<<20 { // страховка от аномально больших страниц
			break
		}
	}
	return buf, resp.StatusCode, nil
}

func applyHeaders(req *fhttp.Request, prof browserProfile) {
	h := fhttp.Header{}
	h.Set("accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7")
	h.Set("accept-language", "ru-RU,ru;q=0.9,en-US;q=0.8,en;q=0.7")
	h.Set("cache-control", "no-cache")
	h.Set("pragma", "no-cache")
	h.Set("referer", "https://www.avito.ru/")
	h.Set("sec-ch-ua", prof.secChUa)
	h.Set("sec-ch-ua-mobile", "?0")
	h.Set("sec-ch-ua-platform", `"Windows"`)
	h.Set("sec-fetch-dest", "document")
	h.Set("sec-fetch-mode", "navigate")
	h.Set("sec-fetch-site", "same-origin")
	h.Set("sec-fetch-user", "?1")
	h.Set("upgrade-insecure-requests", "1")
	h.Set("user-agent", prof.ua)
	req.Header = h
	// tls-client/fhttp: порядок заголовков влияет на фингерпринт HTTP/2.
	req.Header[fhttp.HeaderOrderKey] = []string{
		"accept", "accept-language", "cache-control", "pragma", "referer",
		"sec-ch-ua", "sec-ch-ua-mobile", "sec-ch-ua-platform",
		"sec-fetch-dest", "sec-fetch-mode", "sec-fetch-site", "sec-fetch-user",
		"upgrade-insecure-requests", "user-agent",
	}
}

func (c *AvitoClient) cooldown(pc *proxyEntry) {
	c.mu.Lock()
	pc.blockedUntil = time.Now().Add(c.cfg.BlockedCool)
	c.mu.Unlock()

	// Мобильные link-прокси: просим сменить выходной IP, чтобы тот же прокси
	// вернулся в пул с новым адресом.
	if c.cfg.ProxyRotationURL != "" {
		go rotateProxy(c.cfg.ProxyRotationURL)
	}
}

func rotateProxy(rotationURL string) {
	cl, err := tlsclient.NewHttpClient(tlsclient.NewNoopLogger(),
		tlsclient.WithTimeoutSeconds(20), tlsclient.WithClientProfile(pickProfile().tls))
	if err != nil {
		return
	}
	req, err := fhttp.NewRequest(fhttp.MethodGet, rotationURL, nil)
	if err != nil {
		return
	}
	resp, err := cl.Do(req)
	if err == nil {
		resp.Body.Close()
	}
}

// parseCookieString разбирает "k1=v1; k2=v2" в слайс cookies.
func parseCookieString(s string) []*fhttp.Cookie {
	var out []*fhttp.Cookie
	for _, pair := range strings.Split(s, ";") {
		pair = strings.TrimSpace(pair)
		if pair == "" || !strings.Contains(pair, "=") {
			continue
		}
		k, v, _ := strings.Cut(pair, "=")
		out = append(out, &fhttp.Cookie{Name: strings.TrimSpace(k), Value: strings.TrimSpace(v)})
	}
	return out
}

// maskProxy прячет креды прокси в логах.
func maskProxy(p string) string {
	u, err := url.Parse(p)
	if err != nil || u.User == nil {
		return p
	}
	if _, ok := u.User.Password(); ok {
		return strings.Replace(p, u.User.String()+"@", "***@", 1)
	}
	return p
}
