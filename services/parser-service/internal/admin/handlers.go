package admin

import (
	"crypto/subtle"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Handler — обработчики /admin/*.
type Handler struct {
	store   AdminStore
	sess    *Sessions
	login   string
	password string
}

// Register вешает маршруты админки на mux. Все /admin/* отдают
// X-Robots-Tag: noindex и не попадают в sitemap (docs/SEO_NOTES.md).
func Register(mux *http.ServeMux, store AdminStore, sess *Sessions, login, password string) {
	h := &Handler{store: store, sess: sess, login: login, password: password}
	mux.Handle("/admin", h.noindex(h.requireSession(http.HandlerFunc(h.pageTable))))
	mux.Handle("GET /admin/login", h.noindex(http.HandlerFunc(h.pageLogin)))
	mux.Handle("POST /admin/login", h.noindex(http.HandlerFunc(h.doLogin)))
	mux.Handle("POST /admin/logout", h.noindex(h.requireSession(http.HandlerFunc(h.doLogout))))
	mux.Handle("GET /admin/listings", h.noindex(h.requireSessionAPI(http.HandlerFunc(h.apiList))))
	mux.Handle("GET /admin/listings/{id}", h.noindex(h.requireSessionAPI(http.HandlerFunc(h.apiGet))))
}

// noindex — служебные маршруты не индексируются.
func (h *Handler) noindex(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Robots-Tag", "noindex, nofollow")
		next.ServeHTTP(w, r)
	})
}

// requireSession — HTML-маршруты: без сессии редирект на логин.
func (h *Handler) requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := h.sess.FromRequest(r); !ok {
			http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requireSessionAPI — JSON API: без сессии 401.
func (h *Handler) requireSessionAPI(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := h.sess.FromRequest(r); !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// credsOk — проверка кредов в постоянном времени (защита от timing-атак).
func (h *Handler) credsOk(login, password string) bool {
	okLogin := subtle.ConstantTimeCompare([]byte(login), []byte(h.login)) == 1
	okPass := subtle.ConstantTimeCompare([]byte(password), []byte(h.password)) == 1
	return okLogin && okPass
}

func (h *Handler) doLogin(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if !h.credsOk(r.PostFormValue("login"), r.PostFormValue("password")) {
		w.WriteHeader(http.StatusUnauthorized)
		renderLogin(w, loginData{Error: "Неверный логин или пароль"})
		return
	}
	h.sess.SetCookie(w, h.sess.issue(h.login, time.Now()))
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func (h *Handler) doLogout(w http.ResponseWriter, r *http.Request) {
	h.sess.ClearCookie(w)
	http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
}

// filtersFromRequest разбирает query-параметры списка.
func filtersFromRequest(r *http.Request) ListingFilters {
	q := r.URL.Query()
	f := ListingFilters{
		City:     q.Get("city"),
		Complex:  q.Get("complex"),
		Search:   q.Get("q"),
		SortBy:   q.Get("sort"),
		SortDir:  strings.ToLower(q.Get("dir")),
		Rooms:    atoi(q.Get("rooms")),
		PriceMin: atoll(q.Get("priceMin")),
		PriceMax: atoll(q.Get("priceMax")),
		Limit:    atoi(q.Get("limit")),
		Page:     atoi(q.Get("page")),
	}
	if v := q.Get("hasCoords"); v != "" {
		b := v == "1" || v == "true"
		f.HasCoords = &b
	}
	if v := q.Get("has_roi"); v != "" {
		b := v == "1" || v == "true"
		f.HasROI = &b
	}
	// студии хранятся с rooms=NULL, поэтому «комнаты = студия» → фильтр studio
	if v := q.Get("rooms"); v == "0" {
		t := true
		f.Studio = &t
	} else {
		f.Rooms = atoi(v)
	}
	if v := q.Get("studio"); v == "1" {
		t := true
		f.Studio = &t
	} else if v == "0" {
		fal := false
		f.Studio = &fal
	}
	return f
}

func (h *Handler) apiList(w http.ResponseWriter, r *http.Request) {
	page, err := h.store.List(r.Context(), filtersFromRequest(r))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, page)
}

func (h *Handler) apiGet(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	row, err := h.store.Get(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if row == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	writeJSON(w, row)
}

func (h *Handler) pageLogin(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.sess.FromRequest(r); ok {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	renderLogin(w, loginData{})
}

func (h *Handler) pageTable(w http.ResponseWriter, r *http.Request) {
	f := filtersFromRequest(r)
	page, err := h.store.List(r.Context(), f)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	renderTable(w, f, page)
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

func atoll(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}
