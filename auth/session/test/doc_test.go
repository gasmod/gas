package session_test

import (
	"context"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/auth"
	"github.com/gasmod/gas/auth/internal/testutil"
	"github.com/gasmod/gas/auth/session"
)

// The example in the package documentation's Usage section: a login sets
// the cookie, a request with it reaches the handler with the principal,
// one without it gets a 401, and a cross-site POST a 403.
//
//nolint:funlen // it follows the documented example step by step.
func TestDoc_Usage(t *testing.T) {
	t.Parallel()

	e := sqliteEnv(t, func(s *session.Settings) { s.CookieSecure = false })
	sessions := e.svc
	mux := http.NewServeMux()
	appHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := gas.PrincipalFromContext(r.Context())
		if !ok {
			w.WriteHeader(299)

			return
		}

		_, _ = w.Write([]byte(p.Subject() + " " + p.Scheme()))
	})

	login := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// ... check the user's password ...
		sess, err := sessions.Create(r.Context(), "user-42", nil, r)
		if err != nil {
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)

			return
		}

		err = sessions.SetCookie(w, sess)
		if err != nil {
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)

			return
		}
	})

	sameOrigin := session.RequireSameOrigin()
	mux.Handle("POST /login", sameOrigin(login))
	mux.Handle("/", sameOrigin(auth.Middleware(sessions)(appHandler)))

	serve := func(r *http.Request) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, r)

		return rec
	}

	r := newRequest(http.MethodPost)

	r.URL.Path = "/login"
	r.Header.Set("Origin", "http://example.com")

	rec := serve(r)
	require.Equal(t, http.StatusOK, rec.Code)

	cookies := rec.Result().Cookies()
	require.Len(t, cookies, 1)
	assert.Equal(t, "session_id", cookies[0].Name)

	r = newRequest(http.MethodGet)
	r.AddCookie(cookies[0])

	rec = serve(r)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "user-42 session", rec.Body.String())

	rec = serve(newRequest(http.MethodGet))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)

	r = newRequest(http.MethodPost)
	r.AddCookie(cookies[0])
	r.Header.Set("Origin", "https://evil.example")
	r.Header.Set("Sec-Fetch-Site", "cross-site")

	rec = serve(r)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

// The error messages the package documentation lists are the ones the
// service returns, and it lists every message this test makes.
//
//nolint:funlen // one case per documented message.
func TestDoc_Errors(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// initErr returns the error of Init with cfg changed by mutate.
	initErr := func(mutate func(*session.Settings)) error {
		cfg := session.DefaultConfig()
		mutate(&cfg.Session)

		svc, err := session.New(session.WithConfig(cfg))(
			testutil.FakeDB{}, testutil.NewNopLogger(), nil, nil,
		)
		require.NoError(t, err)

		return svc.Init(ctx)
	}

	e := sqliteEnv(t, nil)
	sess := e.create(t, "alice", nil)
	expired := e.create(t, "bob", nil)

	e.setTimes(t, expired.ID, time.Now().Add(-time.Hour), time.Now().Add(-time.Minute))

	p, err := e.authenticate(ctx, sess.ID)
	require.NoError(t, err)

	const closed = "gas/auth/session: authenticate: gas/auth/session-store: get session: sql: database is closed"

	cases := map[string]func() error{
		"gas/auth/session: service not initialized": func() error {
			svc, err := session.New()(testutil.FakeDB{}, testutil.NewNopLogger(), nil, nil)
			require.NoError(t, err)

			_, err = svc.Authenticate(ctx, newRequestContext(ctx, http.MethodGet))

			return err
		},
		"gas/auth/session: logger is nil": func() error {
			_, err := session.New()(testutil.FakeDB{}, nil, nil, nil)

			return err
		},
		"gas/auth/session: no credentials": func() error {
			_, err := e.svc.Authenticate(ctx, newRequestContext(ctx, http.MethodGet))

			return err
		},
		"gas/auth/session: unauthenticated": func() error {
			_, err := e.authenticate(ctx, strings.Repeat("A", len(sess.ID)))

			return err
		},
		"gas/auth/session: unauthenticated: credentials expired": func() error {
			_, err := e.authenticate(ctx, expired.ID)

			return err
		},
		"gas/auth/session: create: empty subject": func() error {
			_, err := e.svc.Create(ctx, "", nil, newRequestContext(ctx, http.MethodPost))

			return err
		},
		"gas/auth/session: create: value too long: subject is longer than 255 bytes": func() error {
			_, err := e.svc.Create(
				ctx,
				strings.Repeat("s", 256),
				nil,
				newRequestContext(ctx, http.MethodPost),
			)

			return err
		},
		"gas/auth/session: rotate: request is nil": func() error {
			_, err := e.svc.Rotate(ctx, nil)

			return err
		},
		"gas/auth/session: set cookie: invalid session": func() error {
			return e.svc.SetCookie(httptest.NewRecorder(), nil)
		},
		"gas/auth/session: revoke: nil principal": func() error {
			return e.svc.Revoke(ctx, nil)
		},
		"gas/auth/session: revoke: principal scheme mismatch": func() error {
			return e.svc.Revoke(
				ctx,
				auth.NewPrincipal(p.Subject(), auth.SchemeJWT, p.CredentialID(), nil),
			)
		},
		"gas/auth/session: revoke session: invalid session": func() error {
			return e.svc.RevokeSession(ctx, nil)
		},
		"gas/auth/session: revoke by id: invalid session": func() error {
			return e.svc.RevokeByID(ctx, p.Subject(), "")
		},
		"gas/auth/session: invalid config: session TTL must not exceed the max lifetime": func() error {
			return initErr(func(s *session.Settings) { s.SessionTTL = s.MaxLifetime + time.Second })
		},
		"gas/auth/session: invalid config: cleanup interval must not be negative": func() error {
			return initErr(func(s *session.Settings) { s.CleanupInterval = -time.Second })
		},
		closed: func() error {
			require.NoError(t, e.db.Close())

			_, err := e.authenticate(ctx, sess.ID)

			return err
		},
	}

	assert.ElementsMatch(t,
		testutil.DocLines(t, "../doc.go", session.ServiceName+": "),
		slices.Collect(maps.Keys(cases)))

	// The case that closes the database runs last.
	for msg, produce := range cases {
		if msg != closed {
			require.EqualError(t, produce(), msg)
		}
	}

	require.EqualError(t, cases[closed](), closed)
}
