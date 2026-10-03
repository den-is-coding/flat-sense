package admin

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// fakeStore — подменяемое хранилище для тестов обработчиков.
type fakeStore struct {
	page *ListingPage
}

func (f *fakeStore) List(ctx context.Context, fl ListingFilters) (*ListingPage, error) {
	if f.page == nil {
		return &ListingPage{Items: []ListingRow{}}, nil
	}
	return f.page, nil
}

func (f *fakeStore) Get(ctx context.Context, id int64) (*ListingRow, error) {
	return &ListingRow{ID: id}, nil
}

const (
	testLogin  = "admin"
	testPass   = "s3cret"
	testSecret = "test-secret"
)

var noRedirect = &http.Client{
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func newTestServer(t *testing.T, st AdminStore) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	Register(mux, st, NewSessions(testSecret, time.Hour), testLogin, testPass)
	return httptest.NewServer(mux)
}

func loginForm(t *testing.T, srv *httptest.Server) *http.Client {
	t.Helper()
	resp, err := noRedirect.PostForm(srv.URL+"/admin/login",
		url.Values{"login": {testLogin}, "password": {testPass}})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("login: status = %d, want 303", resp.StatusCode)
	}
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookie {
			client.Jar = newJar(c)
		}
	}
	if client.Jar == nil {
		t.Fatal("session cookie not set after login")
	}
	return client
}

// newJar — минимальный cookie-jar, отдающий фиксированную cookie.
type jar struct{ cookie *http.Cookie }

func newJar(c *http.Cookie) http.CookieJar { return &jar{c} }

func (j *jar) Cookies(u *url.URL) []*http.Cookie   { return []*http.Cookie{j.cookie} }
func (j *jar) SetCookies(*url.URL, []*http.Cookie) {}

func TestLogin_Logout(t *testing.T) {
	srv := newTestServer(t, &fakeStore{})
	defer srv.Close()

	// неверные креды → 401 и страница с ошибкой
	resp, err := noRedirect.PostForm(srv.URL+"/admin/login",
		url.Values{"login": {testLogin}, "password": {"wrong"}})
	if err != nil {
		t.Fatal(err)
	}
	body := readAll(t, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad creds: status = %d, want 401", resp.StatusCode)
	}
	if !strings.Contains(body, "Неверный логин или пароль") {
		t.Fatal("error message not rendered")
	}

	client := loginForm(t, srv)

	// с сессией таблица доступна
	resp2, err := client.Get(srv.URL + "/admin")
	if err != nil {
		t.Fatal(err)
	}
	body2 := readAll(t, resp2.Body)
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("GET /admin with session: status = %d", resp2.StatusCode)
	}
	if !strings.Contains(body2, "flat-sense admin") {
		t.Fatal("table page not rendered")
	}

	// выход: cookie сбрасывается
	resp3, err := client.PostForm(srv.URL+"/admin/logout", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp3.Body.Close()
	if resp3.StatusCode != http.StatusSeeOther {
		t.Fatalf("logout: status = %d", resp3.StatusCode)
	}
}

func TestAdmin_RequiresSession(t *testing.T) {
	srv := newTestServer(t, &fakeStore{})
	defer srv.Close()

	// HTML: без сессии редирект на логин
	resp, err := noRedirect.Get(srv.URL + "/admin")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("GET /admin w/o session: status = %d, want 303", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/admin/login" {
		t.Fatalf("redirect target = %q, want /admin/login", loc)
	}

	// API: без сессии 401
	resp2, err := noRedirect.Get(srv.URL + "/admin/listings")
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("GET /admin/listings w/o session: status = %d, want 401", resp2.StatusCode)
	}
}

func TestAdmin_Noindex(t *testing.T) {
	srv := newTestServer(t, &fakeStore{})
	defer srv.Close()
	for _, path := range []string{"/admin/login", "/admin", "/admin/listings"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if tag := resp.Header.Get("X-Robots-Tag"); !strings.Contains(tag, "noindex") {
			t.Fatalf("%s: X-Robots-Tag = %q, want noindex", path, tag)
		}
	}
}

func TestAdmin_ListWithSession(t *testing.T) {
	st := &fakeStore{page: &ListingPage{
		Total: 1, Page: 1, Limit: 50,
		Items: []ListingRow{{
			ID: 42, Title: "Студия 25 м²", Price: i64p(7_900_000),
			TotalArea: f64p(25.4), HasCoords: true, City: "Санкт-Петербург",
		}},
	}}
	srv := newTestServer(t, st)
	defer srv.Close()
	client := loginForm(t, srv)

	resp, err := client.Get(srv.URL + "/admin/listings?city=Санкт-Петербург&hasCoords=1&sort=price&dir=asc&page=2")
	if err != nil {
		t.Fatal(err)
	}
	body := readAll(t, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list: status = %d", resp.StatusCode)
	}
	if !strings.Contains(body, `"total": 1`) || !strings.Contains(body, "Студия 25 м²") {
		t.Fatalf("unexpected listing payload: %s", body)
	}
	if !strings.Contains(body, `"hasCoords": true`) {
		t.Fatal("hasCoords not in payload")
	}
}

func TestSessions_Verify(t *testing.T) {
	s := NewSessions(testSecret, time.Hour)
	now := time.Now()
	tok := s.issue("admin", now)
	if login, ok := s.Verify(tok, now); !ok || login != "admin" {
		t.Fatalf("valid token rejected: %q %v", login, ok)
	}
	if _, ok := s.Verify(tok, now.Add(2*time.Hour)); ok {
		t.Fatal("expired token accepted")
	}
	if _, ok := s.Verify(tok+"x", now); ok {
		t.Fatal("tampered token accepted")
	}
	other := NewSessions("another-secret", time.Hour)
	if _, ok := other.Verify(tok, now); ok {
		t.Fatal("token signed with another secret accepted")
	}
}

func TestFilters_BuildQuery(t *testing.T) {
	tfalse := false
	f := ListingFilters{
		City: "Санкт-Петербург", Complex: "ЖК Меридиан", Rooms: 2,
		PriceMin: 7_000_000, PriceMax: 9_000_000, HasCoords: &tfalse,
		Search: "студия", SortBy: "price", SortDir: "asc", Limit: 25, Page: 3,
	}
	listSQL, countSQL, args := f.buildQuery()
	for _, frag := range []string{
		"city = $1", "residential_complex = $2", "rooms = $3",
		"price >= $4", "price <= $5", "(lat IS NULL OR lng IS NULL)",
		"ORDER BY price ASC NULLS LAST", "LIMIT 25 OFFSET 50",
	} {
		if !strings.Contains(listSQL, frag) {
			t.Fatalf("list SQL missing %q:\n%s", frag, listSQL)
		}
	}
	if !strings.Contains(countSQL, "count(*)") || strings.Contains(countSQL, "ORDER BY") {
		t.Fatalf("bad count SQL: %s", countSQL)
	}
	if len(args) != 6 {
		t.Fatalf("args = %v, want 6 positional params", args)
	}
	if !strings.Contains(listSQL, "address ILIKE") {
		t.Fatal("search filter missing")
	}
	// белый список сортировки: неизвестная колонка → дефолт
	f2 := ListingFilters{SortBy: "drop table", Limit: 10, Page: 1}
	l2, _, _ := f2.buildQuery()
	if strings.Contains(l2, "drop table") {
		t.Fatal("sort column injection not blocked")
	}
	if !strings.Contains(l2, "ORDER BY last_seen_at DESC") {
		t.Fatalf("default sort missing: %s", l2)
	}
}

// readAll — тело ответа строкой.
func readAll(t *testing.T, r io.Reader) string {
	t.Helper()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func i64p(v int64) *int64     { return &v }
func f64p(v float64) *float64 { return &v }
