package main

import (
	"errors"
	"fmt"
	"net/http"
	"sync"

	"github.com/gasmod/gas"
)

var errExample = errors.New("something went wrong")

// NotesModule demonstrates Route() for pattern-scoped groups, Group() for
// inline middleware groups, per-route named middleware via Handle(), BindJSON
// for request body parsing, and an in-memory store.
type NotesModule struct {
	router *gas.Router

	mu    sync.RWMutex
	notes map[string]string // slug → body
}

type NotesModuleCtor func(*gas.Router) *NotesModule

func NewNotesModule() NotesModuleCtor {
	return func(router *gas.Router) *NotesModule {
		return &NotesModule{
			router: router,
			notes:  map[string]string{"hello": "This is the hello note."},
		}
	}
}

func (m *NotesModule) Name() string { return "notes-module" }

func (m *NotesModule) Init() error {
	// Register a named middleware for content-type checking.
	m.router.Register(m.Name(), "json-content-type", func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Content-Type") != "application/json" {
				http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
				return
			}
			next.ServeHTTP(w, r)
		})
	})

	// All note routes live under /notes using Route().
	m.router.Route("/notes", func(sub *gas.Router) {
		sub.Handle(m.Name(), http.MethodGet, "/", m.handleList)
		sub.Handle(m.Name(), http.MethodGet, "/{slug}", m.handleShow)

		// Group() for write endpoints — applies inline middleware to a subset of routes.
		sub.Group(func(write *gas.Router) {
			// Apply the named "json-content-type" middleware only to write routes.
			write.Use(gas.MiddlewareByName("json-content-type"))
			write.Handle(m.Name(), http.MethodPost, "/", m.handleCreate)
		})
	})

	return nil
}

func (m *NotesModule) Close() error { return nil }

// handleList — lists all note slugs as JSON.
func (m *NotesModule) handleList(ctx gas.Context, logger RequestLogger) error {
	m.mu.RLock()
	slugs := make([]string, 0, len(m.notes))
	for slug := range m.notes {
		slugs = append(slugs, slug)
	}
	m.mu.RUnlock()

	logger.Info("listing notes").Int("count", len(slugs)).Send()
	return ctx.JSON(http.StatusOK, slugs)
}

// handleShow — demonstrates Param() to fetch a single note.
func (m *NotesModule) handleShow(ctx gas.Context) error {
	slug := ctx.Param("slug")

	m.mu.RLock()
	body, ok := m.notes[slug]
	m.mu.RUnlock()

	if !ok {
		return ctx.Text(http.StatusNotFound, fmt.Sprintf("note %q not found", slug))
	}
	return ctx.JSON(http.StatusOK, map[string]string{"slug": slug, "body": body})
}

// handleCreate — demonstrates BindJSON and SetHeader.
func (m *NotesModule) handleCreate(ctx gas.Context, logger RequestLogger) error {
	var input struct {
		Slug string `json:"slug"`
		Body string `json:"body"`
	}
	if err := ctx.BindJSON(&input); err != nil {
		return ctx.Text(http.StatusBadRequest, "invalid JSON")
	}
	if input.Slug == "" || input.Body == "" {
		return ctx.Text(http.StatusBadRequest, "slug and body are required")
	}

	m.mu.Lock()
	m.notes[input.Slug] = input.Body
	m.mu.Unlock()

	logger.Info("note created").Str("slug", input.Slug).Send()
	ctx.SetHeader("Location", "/notes/"+input.Slug)
	return ctx.JSON(http.StatusCreated, map[string]string{"slug": input.Slug, "body": input.Body})
}
