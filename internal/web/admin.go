package web

import (
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/tonis/c3pkg/internal/store"
)

func (s *Server) requireAdmin(h http.HandlerFunc) http.HandlerFunc {
	return s.requireUser(func(w http.ResponseWriter, r *http.Request) {
		if !userFrom(r).IsAdmin {
			s.notFound(w, r) // do not reveal that an admin area exists
			return
		}
		h(w, r)
	})
}

type adminView struct {
	Q        string
	Reports  []store.Report
	Packages []store.AdminPackage
	Users    []store.User
}

func (s *Server) admin(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	v := adminView{Q: q}
	var err error
	if v.Reports, err = s.store.OpenReports(r.Context()); err == nil {
		if v.Packages, err = s.store.AdminPackages(r.Context(), q, 50); err == nil {
			v.Users, err = s.store.AdminUsers(r.Context(), q, 50)
		}
	}
	if err != nil {
		s.serverError(w, "admin", err)
		return
	}
	s.render(w, r, "admin", pageData{Title: "Admin", Flash: r.URL.Query().Get("flash"), Data: v})
}

func (s *Server) adminDone(w http.ResponseWriter, r *http.Request, action, target string, err error) {
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, "admin "+action, err)
		return
	}
	log.Printf("admin: %s %s %s", userFrom(r).Login, action, target)
	http.Redirect(w, r, "/admin?flash="+action+"+done", http.StatusSeeOther)
}

func (s *Server) adminRemovePackage(removed bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		action := "remove"
		if !removed {
			action = "restore"
		}
		name := r.PathValue("name")
		s.adminDone(w, r, action, "package "+name, s.store.SetPackageRemoved(r.Context(), name, removed))
	}
}

func (s *Server) adminBanUser(banned bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		login := r.PathValue("login")
		target, err := s.store.UserByLogin(r.Context(), login)
		if err != nil {
			s.adminDone(w, r, "ban", "user "+login, err)
			return
		}
		if banned && (target.IsAdmin || target.ID == userFrom(r).ID) {
			http.Error(w, "admins cannot be banned from the web UI", http.StatusForbidden)
			return
		}
		action := "ban"
		if !banned {
			action = "unban"
		}
		s.adminDone(w, r, action, "user "+login, s.store.SetUserBanned(r.Context(), login, banned))
	}
}

func (s *Server) adminResolveReport(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.notFound(w, r)
		return
	}
	s.adminDone(w, r, "resolve", "report "+r.PathValue("id"), s.store.ResolveReport(r.Context(), id))
}

// ---- reporting a package ----

func (s *Server) reportForm(w http.ResponseWriter, r *http.Request) {
	p, ok := s.livePackage(w, r)
	if !ok {
		return
	}
	s.render(w, r, "report", pageData{Title: "Report " + p.Name, Data: p})
}

func (s *Server) reportSubmit(w http.ResponseWriter, r *http.Request) {
	p, ok := s.livePackage(w, r)
	if !ok {
		return
	}
	reason := strings.TrimSpace(r.PostFormValue("reason"))
	if n := utf8.RuneCountInString(reason); n < 10 || n > 1000 {
		w.WriteHeader(http.StatusUnprocessableEntity)
		s.render(w, r, "report", pageData{Title: "Report " + p.Name, Data: p, Form: map[string]string{"reason": reason},
			Errors: map[string]string{"reason": "Please describe the problem in 10 to 1000 characters."}})
		return
	}
	if err := s.store.CreateReport(r.Context(), p.ID, userFrom(r).ID, reason); err != nil {
		s.serverError(w, "report", err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
	s.render(w, r, "message", pageData{Title: "Thanks for the report", Data: "A moderator will review " + p.Name + ". You can close this page."})
}
