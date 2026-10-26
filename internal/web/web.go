package web

import (
	"bytes"
	"context"
	"errors"
	"html/template"
	"io"
	"io/fs"
	"log"
	"net/http"
	"strings"

	"github.com/tonis/c3pkg/internal/auth"
	"github.com/tonis/c3pkg/internal/config"
	"github.com/tonis/c3pkg/internal/storage"
	"github.com/tonis/c3pkg/internal/store"
	webassets "github.com/tonis/c3pkg/web"
)

const (
	sessionCookie = "c3_session"
	csrfCookie    = "c3_csrf"
)

type Server struct {
	cfg     config.Config
	store   *store.Store
	storage storage.Storage
	limiter *limiter

	// LogRequests enables one access-log line per request.
	LogRequests bool
	github      *githubClient
	pages       map[string]*template.Template
	staticFS    http.Handler
}

// pageData is what every template receives.
type pageData struct {
	Title          string
	SearchQ        string // prefills the navbar search box
	NoLoginDialog  bool   // the page is itself a login/register form
	GitHub         bool   // GitHub login is configured
	Base           string // public base URL, for absolute links in copyable commands
	User           *store.User
	CSRF           string
	Errors         map[string]string
	Form           map[string]string
	Flash          string
	Data           any
}

type ctxKey int

const (
	userKey ctxKey = iota
	csrfKey
	tokenAuthKey // set when the user authenticated with an API token rather than a session
)

var pageNames = []string{"home", "register", "login", "dashboard", "settings", "message",
	"package", "user", "new_package", "publish", "edit_package", "admin", "report", "docs"}

func New(cfg config.Config, st *store.Store, stor storage.Storage) (*Server, error) {
	s := &Server{cfg: cfg, store: st, storage: stor, limiter: newLimiter(), github: newGitHubClient(cfg), pages: map[string]*template.Template{}}
	for _, name := range pageNames {
		t, err := template.New(name).Funcs(templateFuncs).ParseFS(webassets.FS, "templates/layout.html", "templates/partials.html", "templates/"+name+".html")
		if err != nil {
			return nil, err
		}
		s.pages[name] = t
	}
	static, err := fs.Sub(webassets.FS, "static")
	if err != nil {
		return nil, err
	}
	s.staticFS = http.StripPrefix("/static/", http.FileServerFS(static))
	return s, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /static/", s.staticFS)
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /docs/api", s.docs)
	mux.HandleFunc("GET /robots.txt", s.robots)
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/static/favicon.svg", http.StatusMovedPermanently)
	})
	mux.HandleFunc("GET /{$}", s.listing)
	mux.HandleFunc("GET /search", s.searchRedirect)

	mux.HandleFunc("GET /register", s.registerForm)
	mux.HandleFunc("POST /register", s.limit(ruleRegister, s.registerSubmit))
	mux.HandleFunc("GET /login", s.loginForm)
	mux.HandleFunc("POST /login", s.limit(ruleLogin, s.loginSubmit))
	mux.HandleFunc("POST /logout", s.logout)
	mux.HandleFunc("GET /auth/github/login", s.limit(ruleOAuth, s.githubLogin))
	mux.HandleFunc("GET /auth/github/callback", s.limit(ruleOAuth, s.githubCallback))

	mux.HandleFunc("GET /dashboard", s.requireUser(s.dashboard))
	mux.HandleFunc("GET /dashboard/packages/new", s.requireUser(s.newPackageForm))
	mux.HandleFunc("POST /dashboard/packages/new", s.requireUser(s.limit(rulePublish, s.newPackageSubmit)))
	mux.HandleFunc("GET /dashboard/packages/{name}/publish", s.requireUser(s.publishForm))
	mux.HandleFunc("POST /dashboard/packages/{name}/publish", s.requireUser(s.limit(rulePublish, s.publishSubmit)))
	mux.HandleFunc("GET /dashboard/packages/{name}/edit", s.requireUser(s.editForm))
	mux.HandleFunc("POST /dashboard/packages/{name}/edit", s.requireUser(s.limit(ruleWrite, s.editSubmit)))
	mux.HandleFunc("POST /dashboard/packages/{name}/versions/{version}/yank", s.requireUser(s.setYanked(true)))
	mux.HandleFunc("POST /dashboard/packages/{name}/versions/{version}/unyank", s.requireUser(s.setYanked(false)))

	mux.HandleFunc("POST /packages/{name}/star", s.requireUser(s.limit(ruleWrite, s.setStar(true))))
	mux.HandleFunc("POST /packages/{name}/unstar", s.requireUser(s.limit(ruleWrite, s.setStar(false))))
	mux.HandleFunc("GET /packages/{name}", s.packagePage)
	mux.HandleFunc("GET /packages/{name}/versions", s.versionsPage)
	mux.HandleFunc("GET /packages/{name}/{version}/download", s.limit(ruleDownload, s.download))
	mux.HandleFunc("GET /users/{login}", s.userPage)
	mux.HandleFunc("GET /packages/{name}/report", s.requireUser(s.reportForm))
	mux.HandleFunc("POST /packages/{name}/report", s.requireUser(s.limit(ruleReport, s.reportSubmit)))

	mux.HandleFunc("GET /admin", s.requireAdmin(s.admin))
	mux.HandleFunc("POST /admin/packages/{name}/remove", s.requireAdmin(s.adminRemovePackage(true)))
	mux.HandleFunc("POST /admin/packages/{name}/restore", s.requireAdmin(s.adminRemovePackage(false)))
	mux.HandleFunc("POST /admin/users/{login}/ban", s.requireAdmin(s.adminBanUser(true)))
	mux.HandleFunc("POST /admin/users/{login}/unban", s.requireAdmin(s.adminBanUser(false)))
	mux.HandleFunc("POST /admin/reports/{id}/resolve", s.requireAdmin(s.adminResolveReport))

	mux.HandleFunc("GET /settings", s.requireUser(s.settings))
	mux.HandleFunc("POST /settings/profile", s.requireUser(s.saveProfile))
	mux.HandleFunc("POST /settings/password", s.requireUser(s.limit(rulePassword, s.savePassword)))
	mux.HandleFunc("POST /settings/tokens", s.requireUser(s.limit(ruleTokens, s.createToken)))
	mux.HandleFunc("POST /settings/tokens/{id}/revoke", s.requireUser(s.revokeToken))

	mux.HandleFunc("GET /api/v1/packages", s.limit(ruleAPIRead, s.apiListPackages))
	mux.HandleFunc("GET /api/v1/packages/{name}", s.limit(ruleAPIRead, s.apiGetPackage))
	mux.HandleFunc("GET /api/v1/packages/{name}/versions", s.limit(ruleAPIRead, s.apiVersions))
	mux.HandleFunc("GET /api/v1/packages/{name}/{version}/download", s.limit(ruleDownload, s.download))
	mux.HandleFunc("PUT /api/v1/packages/{name}/versions/{version}", s.apiAuth(s.limit(rulePublish, s.apiPublish)))
	mux.HandleFunc("PUT /api/v1/packages/{name}/versions/{version}/yank", s.apiAuth(s.limit(ruleWrite, s.apiYank(true))))
	mux.HandleFunc("DELETE /api/v1/packages/{name}/versions/{version}/yank", s.apiAuth(s.limit(ruleWrite, s.apiYank(false))))
	mux.HandleFunc("PUT /api/v1/packages/{name}/readme", s.apiAuth(s.limit(ruleWrite, s.apiSetReadme)))
	mux.HandleFunc("PATCH /api/v1/packages/{name}", s.apiAuth(s.limit(ruleWrite, s.apiPatchPackage)))
	mux.HandleFunc("PUT /api/v1/packages/{name}/star", s.apiAuth(s.limit(ruleWrite, s.apiStar(true))))
	mux.HandleFunc("DELETE /api/v1/packages/{name}/star", s.apiAuth(s.limit(ruleWrite, s.apiStar(false))))
	mux.HandleFunc("GET /api/v1/users/{login}", s.limit(ruleAPIRead, s.apiUser))
	mux.HandleFunc("GET /api/v1/me", s.apiAuth(s.apiMe))
	mux.HandleFunc("PATCH /api/v1/me", s.apiAuth(s.limit(ruleWrite, s.apiPatchMe)))
	mux.HandleFunc("GET /api/v1/me/tokens", s.apiSessionOnly(s.apiListTokens))
	mux.HandleFunc("POST /api/v1/me/tokens", s.apiSessionOnly(s.limit(ruleTokens, s.apiCreateToken)))
	mux.HandleFunc("DELETE /api/v1/me/tokens/{id}", s.apiSessionOnly(s.limit(ruleTokens, s.apiRevokeToken)))
	mux.HandleFunc("GET /index/{name}/{file}", s.limit(ruleDownload, s.index))

	var h http.Handler = securityHeaders(s.hsts(s.loadUser(s.globalLimit(s.csrf(mux)))))
	if s.LogRequests {
		h = s.accessLog(h)
	}
	return recoverer(h)
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, name string, d pageData) {
	d.User = userFrom(r)
	d.CSRF, _ = r.Context().Value(csrfKey).(string)
	d.GitHub, d.Base = githubEnabled(s), s.cfg.BaseURL
	var buf bytes.Buffer
	if err := s.pages[name].ExecuteTemplate(&buf, "layout", d); err != nil {
		log.Printf("render %s: %v", name, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	buf.WriteTo(w)
}

// robots keeps crawlers out of account pages and machine endpoints but lets them index packages.
func (s *Server) robots(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	io.WriteString(w, "User-agent: *\nDisallow: /dashboard/\nDisallow: /dashboard\nDisallow: /settings\nDisallow: /admin\nDisallow: /auth/\nDisallow: /api/\nDisallow: /index/\nDisallow: /login\nDisallow: /register\nDisallow: /search\n")
}

func (s *Server) docs(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "docs", pageData{Title: "API"})
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DB.PingContext(r.Context()); err != nil {
		http.Error(w, "db unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Write([]byte("ok\n"))
}

func userFrom(r *http.Request) *store.User {
	u, _ := r.Context().Value(userKey).(*store.User)
	return u
}

// loadUser attaches the signed-in user (session cookie or bearer API token) to the request.
func (s *Server) loadUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var u *store.User
		if c, err := r.Cookie(sessionCookie); err == nil {
			u, _ = s.store.UserBySession(r.Context(), c.Value)
		}
		viaToken := false
		if tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok && u == nil {
			if u, _ = s.store.UserByAPIToken(r.Context(), tok); u != nil {
				viaToken = true
			}
		}
		if u != nil {
			ctx := context.WithValue(r.Context(), userKey, u)
			r = r.WithContext(context.WithValue(ctx, tokenAuthKey, viaToken))
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requireUser(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if userFrom(r) == nil {
			http.Redirect(w, r, "/login?next="+template.URLQueryEscaper(r.URL.RequestURI()), http.StatusSeeOther)
			return
		}
		h(w, r)
	}
}

// csrf issues a signed double-submit token in a cookie and checks it on unsafe methods.
// Requests that authenticate with a bearer token are not subject to browser CSRF.
func (s *Server) csrf(next http.Handler) http.Handler {
	secret := []byte(s.cfg.SessionSecret)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := ""
		if c, err := r.Cookie(csrfCookie); err == nil && auth.VerifyCSRF(secret, c.Value) {
			token = c.Value
		}
		if token == "" {
			t, err := auth.NewCSRF(secret)
			if err != nil {
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			token = t
			http.SetCookie(w, &http.Cookie{Name: csrfCookie, Value: token, Path: "/", HttpOnly: true,
				Secure: s.cfg.SecureCookies(), SameSite: http.SameSiteLaxMode})
		}
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		default:
			limit := int64(1 << 20)
			if strings.HasPrefix(r.URL.Path, "/dashboard/packages/") || (r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/api/v1/packages/")) {
				limit = (s.cfg.MaxUploadMB + 2) << 20 // the .c3l, an optional README and form fields
			}
			r.Body = http.MaxBytesReader(w, r.Body, limit)
			if err := r.ParseMultipartForm(8 << 20); err != nil && !errors.Is(err, http.ErrNotMultipart) {
				var tooBig *http.MaxBytesError
				if errors.As(err, &tooBig) {
					http.Error(w, "upload too large", http.StatusRequestEntityTooLarge)
				} else {
					http.Error(w, "malformed form data", http.StatusBadRequest)
				}
				return
			}
			// Browsers attach cookies automatically, so only cookie-authenticated requests need the
			// token. A bearer token is never sent by a forged cross-site form, and an API call with
			// no session cookie has nothing to hijack (it fails the auth check instead).
			bearer := strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ")
			_, noSession := r.Cookie(sessionCookie)
			apiWithoutSession := strings.HasPrefix(r.URL.Path, "/api/") && noSession != nil
			if !bearer && !apiWithoutSession {
				got := r.PostFormValue("csrf")
				if r.Header.Get("X-CSRF-Token") != "" {
					got = r.Header.Get("X-CSRF-Token")
				}
				if got == "" || got != token {
					http.Error(w, "invalid CSRF token", http.StatusForbidden)
					return
				}
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), csrfKey, token)))
	})
}

func (s *Server) setSession(w http.ResponseWriter, r *http.Request, userID int64) error {
	raw, exp, err := s.store.CreateSession(r.Context(), userID)
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: raw, Path: "/", Expires: exp, HttpOnly: true,
		Secure: s.cfg.SecureCookies(), SameSite: http.SameSiteLaxMode})
	return nil
}

func (s *Server) clearSession(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true,
		Secure: s.cfg.SecureCookies(), SameSite: http.SameSiteLaxMode})
}

// safeNext returns next only if it is a same-site relative path.
func safeNext(next string) string {
	if strings.HasPrefix(next, "/") && !strings.HasPrefix(next, "//") && !strings.Contains(next, "\\") {
		return next
	}
	return "/dashboard"
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' https: data:; style-src 'self'; script-src 'self'; form-action 'self'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}
