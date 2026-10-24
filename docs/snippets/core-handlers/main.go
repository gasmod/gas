// Package main shows DI-aware handlers and the request Context.
package main

import (
	"context"
	"encoding/xml"
	"log"
	"net/http"
	"sync"

	"github.com/gasmod/gas"
)

// Pages owns the routes the handler examples register.
type Pages struct {
	router *gas.Router
}

func NewPages(router *gas.Router) *Pages { return &Pages{router: router} }

func (s *Pages) Name() string                { return "pages" }
func (s *Pages) Close(context.Context) error { return nil }

// #region feed
type feed struct {
	XMLName xml.Name `xml:"rss"`
	Version string   `xml:"version,attr"`
}

// #endregion feed

func (s *Pages) Init(context.Context) error {
	// #region forms
	// Both handler forms register on the same router.

	// Plain: the standard library signature, passed through as is.
	s.router.Get(s, "/ping", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	// DI-aware: gas.Context first, dependencies after, one error result.
	s.router.Get(s, "/hello", func(ctx gas.Context) error {
		return ctx.Text(http.StatusOK, "hello")
	})
	// #endregion forms

	// #region responses
	s.router.Get(s, "/json", func(ctx gas.Context) error {
		return ctx.JSON(http.StatusOK, map[string]int{"count": 3})
	})
	s.router.Get(s, "/feed.xml", func(ctx gas.Context) error {
		body, err := xml.Marshal(feed{Version: "2.0"})
		if err != nil {
			return err
		}
		return ctx.Blob(http.StatusOK, "application/rss+xml; charset=utf-8", append([]byte(xml.Header), body...))
	})
	s.router.Get(s, "/page", func(ctx gas.Context) error {
		return ctx.HTML(http.StatusOK, "<h1>Notes</h1>")
	})
	s.router.Delete(s, "/notes/{id}", func(ctx gas.Context) error {
		return ctx.NoContent()
	})
	s.router.Get(s, "/old", func(ctx gas.Context) error {
		ctx.Redirect(http.StatusMovedPermanently, "/new")
		return nil
	})
	// #endregion responses

	return nil
}

// NoteStore is a singleton: one instance shared by every request.
type NoteStore struct {
	notes map[string]string
	mu    sync.RWMutex
}

func NewNoteStore() *NoteStore { return &NoteStore{notes: map[string]string{"1": "first"}} }

func (s *NoteStore) Get(id string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n, ok := s.notes[id]
	return n, ok
}

// AuditLog is scoped: each request gets its own instance.
type AuditLog struct{ entries []string }

func NewAuditLog() *AuditLog { return &AuditLog{} }

func (a *AuditLog) Record(entry string) { a.entries = append(a.entries, entry) }

// #region deps
// Dependencies are parameters. Each is resolved from the request's scope when
// the request arrives: the singleton store is shared, the scoped audit log is
// built for this request alone.
func showNote(ctx gas.Context, store *NoteStore, audit *AuditLog) error {
	id := ctx.Param("id")
	audit.Record("read " + id)

	note, ok := store.Get(id)
	if !ok {
		return gas.NotFound("note not found")
	}
	return ctx.JSON(http.StatusOK, map[string]string{"id": id, "note": note})
}

func wire() *gas.App {
	app := gas.NewApp(
		gas.WithSingletonService[*NoteStore](NewNoteStore),
		gas.WithScopedService[*AuditLog](NewAuditLog),
	)
	app.Router().Get(nil, "/notes/{id}", showNote)
	return app
}

// #endregion deps

// #region request
func search(ctx gas.Context) error {
	q := ctx.Query("q")                        // query string value
	trace := ctx.Header("X-Trace-Id")          // request header
	agent := ctx.Request().UserAgent()         // the *http.Request itself
	ctx.SetHeader("Cache-Control", "no-store") // response header

	return ctx.JSON(http.StatusOK, map[string]string{"q": q, "trace": trace, "agent": agent})
}

// #endregion request

// #region bind
// CreateNote is decoded from a JSON body and validated by its validate tags.
type CreateNote struct {
	Title string `json:"title" validate:"required,max=120"`
	Body  string `json:"body"`
}

// SignupForm is decoded from a form body by its form tags.
type SignupForm struct {
	Email string `form:"email" validate:"required,email"`
	Name  string `form:"name"`
}

func createNote(ctx gas.Context) error {
	var req CreateNote
	if err := ctx.BindJSON(&req); err != nil {
		return err // already a *gas.Error: 400, 413, or 422
	}
	return ctx.JSON(http.StatusCreated, req)
}

func signup(ctx gas.Context) error {
	var form SignupForm
	if err := ctx.BindForm(&form); err != nil {
		return err // already a *gas.Error: 400 or 422
	}
	ctx.Redirect(http.StatusSeeOther, "/welcome")
	return nil
}

// #endregion bind

// #region principal
func profile(ctx gas.Context) error {
	p, err := ctx.Principal()
	if err != nil {
		return err // a 401 *gas.Error when no principal is attached
	}
	return ctx.JSON(http.StatusOK, map[string]string{"subject": p.Subject()})
}

// #endregion principal

// #region plaincontext
// A plain handler can build a Context to use the response helpers. It does not
// get the router's form decoder or body limit unless it passes them.
func plainWithHelpers(w http.ResponseWriter, r *http.Request) {
	ctx := gas.NewContext(r.Context(), w, r)
	if err := ctx.JSON(http.StatusOK, map[string]bool{"ok": true}); err != nil {
		log.Printf("write response: %v", err)
	}
}

// #endregion plaincontext

// #region resolve
// A plain handler resolves from the request scope itself.
func plainWithDeps(w http.ResponseWriter, r *http.Request) {
	store, err := gas.ResolveFromRequestScope[*NoteStore](r)
	if err != nil {
		_ = gas.WriteError(w, r, err)
		return
	}
	note, _ := store.Get("1")
	_, _ = w.Write([]byte(note))
}

// #endregion resolve

func main() {
	app := wire()
	app.RegisterSingletonService[*Pages](NewPages)
	if err := app.Start(context.Background()); err != nil {
		log.Fatal(err)
	}
}
