package handlers_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/gorilla/mux"
	"github.com/jmoiron/sqlx"
	_ "github.com/mattn/go-sqlite3"
	migrate "github.com/rubenv/sql-migrate"

	"github.com/robrohan/laffaire/internals/env"
	"github.com/robrohan/laffaire/internals/handlers"
	"github.com/robrohan/laffaire/internals/models"
	"github.com/robrohan/laffaire/internals/repository"
)

const (
	testUserUUID  = "11111111-1111-1111-1111-111111111111"
	otherUserUUID = "22222222-2222-2222-2222-222222222222"
)

// newTestDB opens an in-memory SQLite database and runs schema migrations.
// The DB is closed automatically when the test finishes.
func newTestDB(t *testing.T) *sqlx.DB {
	t.Helper()
	db, err := sqlx.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}

	src := &migrate.MemoryMigrationSource{
		Migrations: []*migrate.Migration{
			{
				Id: "000000",
				Up: []string{
					`CREATE TABLE IF NOT EXISTS users (
						uuid TEXT primary key,
						email TEXT,
						username TEXT,
						picture TEXT,
						authid TEXT,
						salt TEXT,
						timezone TEXT DEFAULT 'UTC',
						UNIQUE(email)
					)`,
				},
			},
			{
				Id: "000001",
				Up: []string{
					`CREATE TABLE IF NOT EXISTS event (
						uuid TEXT primary key,
						user_uuid TEXT,
						title TEXT,
						description TEXT
					)`,
					`CREATE TABLE IF NOT EXISTS entry (
						uuid TEXT primary key,
						event_uuid TEXT,
						subject TEXT,
						start_date TEXT,
						start_time TEXT,
						end_date TEXT,
						end_time TEXT,
						all_day_event INTEGER,
						description TEXT,
						location TEXT,
						private INTEGER
					)`,
				},
			},
			{
				Id: "000002",
				Up: []string{
					`CREATE TABLE IF NOT EXISTS token (
						uuid       TEXT primary key,
						user_uuid  TEXT,
						name       TEXT,
						token      TEXT UNIQUE,
						created_at TEXT
					)`,
				},
			},
		},
	}
	if _, err := migrate.Exec(db.DB, "sqlite3", src, migrate.Up); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// newTestEnv creates an Env wired to db. The authenticated user is no
// longer part of Env (it's per-request, carried on context - see
// internals/env.WithUser) - apiRouter below attaches it per test router
// instead, mirroring what LoginVerify / APILoginVerify do in production.
func newTestEnv(t *testing.T, db *sqlx.DB) *env.Env {
	t.Helper()
	repo := repository.Attach("", db, "sqlite3")
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	return &env.Env{
		Db:   db,
		Log:  log,
		Repo: repo,
	}
}

func testUser(userUUID string) *models.User {
	return &models.User{UUID: userUUID, Email: userUUID + "@test.example"}
}

// apiRouter wires all JSON API routes for e, with every request
// authenticated as user via request context (no real auth middleware in
// tests, but the same mechanism production uses to attach it).
func apiRouter(e *env.Env, user *models.User) *mux.Router {
	router := mux.NewRouter()
	api := router.PathPrefix("/api/v1").Subrouter()
	api.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(env.WithUser(r.Context(), user)))
		})
	})
	api.HandleFunc("/events", handlers.APIGetEvents(e)).Methods("GET")
	api.HandleFunc("/events", handlers.APICreateEvent(e)).Methods("POST")
	api.HandleFunc("/events/{id}", handlers.APIGetEvent(e)).Methods("GET")
	api.HandleFunc("/events/{id}", handlers.APIUpdateEvent(e)).Methods("PUT")
	api.HandleFunc("/events/{id}", handlers.APIDeleteEvent(e)).Methods("DELETE")
	api.HandleFunc("/events/{id}/entries", handlers.APIGetEntries(e)).Methods("GET")
	api.HandleFunc("/entries", handlers.APICreateEntry(e)).Methods("POST")
	api.HandleFunc("/entries/{id}", handlers.APIGetEntry(e)).Methods("GET")
	api.HandleFunc("/entries/{id}", handlers.APIUpdateEntry(e)).Methods("PUT")
	api.HandleFunc("/entries/{id}", handlers.APIDeleteEntry(e)).Methods("DELETE")
	return router
}

// do fires method+path at router with an optional JSON body and returns the recorder.
func do(t *testing.T, router *mux.Router, method, path string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	var err error
	if body != nil {
		req, err = http.NewRequest(method, path, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req, err = http.NewRequest(method, path, nil)
	}
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func mustDecode[T any](t *testing.T, body []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("decode JSON: %v\nbody: %s", err, body)
	}
	return v
}

// -------------------------------------------------------------------------
// Events
// -------------------------------------------------------------------------

func TestAPIEvents(t *testing.T) {
	db := newTestDB(t)
	e := newTestEnv(t, db)
	router := apiRouter(e, testUser(testUserUUID))
	otherRouter := apiRouter(e, testUser(otherUserUUID))

	// eventID is populated by the "POST creates event" sub-test and used by
	// all subsequent sub-tests. Sub-tests run sequentially (no t.Parallel).
	var eventID string

	t.Run("GET returns empty list initially", func(t *testing.T) {
		rec := do(t, router, "GET", "/api/v1/events", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("want 200, got %d", rec.Code)
		}
		out := mustDecode[[]models.Event](t, rec.Body.Bytes())
		if len(out) != 0 {
			t.Fatalf("want 0 events, got %d", len(out))
		}
	})

	t.Run("POST missing title returns 400", func(t *testing.T) {
		rec := do(t, router, "POST", "/api/v1/events", []byte(`{"description":"no title"}`))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("want 400, got %d", rec.Code)
		}
	})

	t.Run("POST invalid JSON returns 400", func(t *testing.T) {
		rec := do(t, router, "POST", "/api/v1/events", []byte(`not json`))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("want 400, got %d", rec.Code)
		}
	})

	t.Run("POST creates event", func(t *testing.T) {
		rec := do(t, router, "POST", "/api/v1/events", []byte(`{"title":"My Calendar","description":"a description"}`))
		if rec.Code != http.StatusCreated {
			t.Fatalf("want 201, got %d: %s", rec.Code, rec.Body.String())
		}
		out := mustDecode[models.Event](t, rec.Body.Bytes())
		if out.UUID == "" {
			t.Fatal("expected non-empty id in response")
		}
		if out.Title != "My Calendar" {
			t.Fatalf("want title 'My Calendar', got %q", out.Title)
		}
		eventID = out.UUID
	})

	t.Run("GET lists created event", func(t *testing.T) {
		rec := do(t, router, "GET", "/api/v1/events", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("want 200, got %d", rec.Code)
		}
		out := mustDecode[[]models.Event](t, rec.Body.Bytes())
		if len(out) != 1 {
			t.Fatalf("want 1 event, got %d", len(out))
		}
		if out[0].UUID != eventID {
			t.Fatalf("want id %q, got %q", eventID, out[0].UUID)
		}
	})

	t.Run("GET by invalid UUID returns 400", func(t *testing.T) {
		rec := do(t, router, "GET", "/api/v1/events/not-a-uuid", nil)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("want 400, got %d", rec.Code)
		}
	})

	t.Run("GET by ID returns event", func(t *testing.T) {
		rec := do(t, router, "GET", "/api/v1/events/"+eventID, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("want 200, got %d", rec.Code)
		}
		out := mustDecode[models.Event](t, rec.Body.Bytes())
		if out.UUID != eventID {
			t.Fatalf("want id %q, got %q", eventID, out.UUID)
		}
	})

	t.Run("GET by ID as wrong user returns 403", func(t *testing.T) {
		rec := do(t, otherRouter, "GET", "/api/v1/events/"+eventID, nil)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("want 403, got %d", rec.Code)
		}
	})

	t.Run("PUT updates event", func(t *testing.T) {
		rec := do(t, router, "PUT", "/api/v1/events/"+eventID, []byte(`{"title":"Renamed","description":"updated"}`))
		if rec.Code != http.StatusOK {
			t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
		}
		out := mustDecode[models.Event](t, rec.Body.Bytes())
		if out.Title != "Renamed" {
			t.Fatalf("want title 'Renamed', got %q", out.Title)
		}
	})

	t.Run("PUT missing title returns 400", func(t *testing.T) {
		rec := do(t, router, "PUT", "/api/v1/events/"+eventID, []byte(`{"description":"no title"}`))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("want 400, got %d", rec.Code)
		}
	})

	t.Run("PUT as wrong user returns 403", func(t *testing.T) {
		rec := do(t, otherRouter, "PUT", "/api/v1/events/"+eventID, []byte(`{"title":"hijack"}`))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("want 403, got %d", rec.Code)
		}
	})

	t.Run("DELETE invalid UUID returns 400", func(t *testing.T) {
		rec := do(t, router, "DELETE", "/api/v1/events/not-a-uuid", nil)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("want 400, got %d", rec.Code)
		}
	})

	t.Run("DELETE removes event", func(t *testing.T) {
		rec := do(t, router, "DELETE", "/api/v1/events/"+eventID, nil)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("want 204, got %d", rec.Code)
		}
	})
}

// -------------------------------------------------------------------------
// Entries
// -------------------------------------------------------------------------

func TestAPIEntries(t *testing.T) {
	db := newTestDB(t)
	e := newTestEnv(t, db)
	router := apiRouter(e, testUser(testUserUUID))
	otherRouter := apiRouter(e, testUser(otherUserUUID))

	// Create a parent event for all entry sub-tests.
	setupRec := do(t, router, "POST", "/api/v1/events", []byte(`{"title":"Test Calendar"}`))
	if setupRec.Code != http.StatusCreated {
		t.Fatalf("setup: create event got %d: %s", setupRec.Code, setupRec.Body.String())
	}
	parentEvent := mustDecode[models.Event](t, setupRec.Body.Bytes())
	eventID := parentEvent.UUID

	var entryID string

	t.Run("GET entries returns empty list", func(t *testing.T) {
		rec := do(t, router, "GET", "/api/v1/events/"+eventID+"/entries", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("want 200, got %d", rec.Code)
		}
		out := mustDecode[[]models.Entry](t, rec.Body.Bytes())
		if len(out) != 0 {
			t.Fatalf("want 0 entries, got %d", len(out))
		}
	})

	t.Run("GET entries as wrong user returns 403", func(t *testing.T) {
		rec := do(t, otherRouter, "GET", "/api/v1/events/"+eventID+"/entries", nil)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("want 403, got %d", rec.Code)
		}
	})

	t.Run("POST entry missing event_id and subject returns 400", func(t *testing.T) {
		rec := do(t, router, "POST", "/api/v1/entries", []byte(`{"description":"orphan"}`))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("want 400, got %d", rec.Code)
		}
	})

	t.Run("POST entry with invalid event_id returns 400", func(t *testing.T) {
		rec := do(t, router, "POST", "/api/v1/entries", []byte(`{"event_id":"not-a-uuid","subject":"x"}`))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("want 400, got %d", rec.Code)
		}
	})

	t.Run("POST entry to another user's event returns 403", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{"event_id": eventID, "subject": "sneaky"})
		rec := do(t, otherRouter, "POST", "/api/v1/entries", body)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("want 403, got %d", rec.Code)
		}
	})

	t.Run("POST entry creates entry", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{
			"event_id":    eventID,
			"subject":     "Doctor Appointment",
			"start_date":  "2024-03-01",
			"start_time":  "09:00",
			"end_date":    "2024-03-01",
			"end_time":    "10:00",
			"description": "Annual checkup",
		})
		rec := do(t, router, "POST", "/api/v1/entries", body)
		if rec.Code != http.StatusCreated {
			t.Fatalf("want 201, got %d: %s", rec.Code, rec.Body.String())
		}
		out := mustDecode[models.Entry](t, rec.Body.Bytes())
		if out.UUID == "" {
			t.Fatal("expected non-empty id in response")
		}
		if out.Subject != "Doctor Appointment" {
			t.Fatalf("want subject 'Doctor Appointment', got %q", out.Subject)
		}
		if out.StartDate != "2024-03-01" {
			t.Fatalf("want start_date '2024-03-01', got %q", out.StartDate)
		}
		entryID = out.UUID
	})

	t.Run("GET entries lists created entry", func(t *testing.T) {
		rec := do(t, router, "GET", "/api/v1/events/"+eventID+"/entries", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("want 200, got %d", rec.Code)
		}
		out := mustDecode[[]models.Entry](t, rec.Body.Bytes())
		if len(out) != 1 {
			t.Fatalf("want 1 entry, got %d", len(out))
		}
		if out[0].UUID != entryID {
			t.Fatalf("want entry id %q, got %q", entryID, out[0].UUID)
		}
	})

	t.Run("GET entry by invalid UUID returns 400", func(t *testing.T) {
		rec := do(t, router, "GET", "/api/v1/entries/not-a-uuid", nil)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("want 400, got %d", rec.Code)
		}
	})

	t.Run("GET entry by ID returns entry", func(t *testing.T) {
		rec := do(t, router, "GET", "/api/v1/entries/"+entryID, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("want 200, got %d", rec.Code)
		}
		out := mustDecode[models.Entry](t, rec.Body.Bytes())
		if out.UUID != entryID {
			t.Fatalf("want id %q, got %q", entryID, out.UUID)
		}
	})

	t.Run("GET entry as wrong user returns 403", func(t *testing.T) {
		rec := do(t, otherRouter, "GET", "/api/v1/entries/"+entryID, nil)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("want 403, got %d", rec.Code)
		}
	})

	t.Run("PUT entry updates entry", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{
			"subject":    "Updated Appointment",
			"start_date": "2024-04-01",
			"end_date":   "2024-04-01",
		})
		rec := do(t, router, "PUT", "/api/v1/entries/"+entryID, body)
		if rec.Code != http.StatusOK {
			t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
		}
		out := mustDecode[models.Entry](t, rec.Body.Bytes())
		if out.Subject != "Updated Appointment" {
			t.Fatalf("want subject 'Updated Appointment', got %q", out.Subject)
		}
	})

	t.Run("PUT entry missing subject returns 400", func(t *testing.T) {
		rec := do(t, router, "PUT", "/api/v1/entries/"+entryID, []byte(`{"description":"no subject"}`))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("want 400, got %d", rec.Code)
		}
	})

	t.Run("PUT entry as wrong user returns 403", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{"subject": "hijack"})
		rec := do(t, otherRouter, "PUT", "/api/v1/entries/"+entryID, body)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("want 403, got %d", rec.Code)
		}
	})

	t.Run("DELETE entry invalid UUID returns 400", func(t *testing.T) {
		rec := do(t, router, "DELETE", "/api/v1/entries/not-a-uuid", nil)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("want 400, got %d", rec.Code)
		}
	})

	t.Run("DELETE entry as wrong user returns 403", func(t *testing.T) {
		rec := do(t, otherRouter, "DELETE", "/api/v1/entries/"+entryID, nil)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("want 403, got %d", rec.Code)
		}
	})

	t.Run("DELETE entry removes it", func(t *testing.T) {
		rec := do(t, router, "DELETE", "/api/v1/entries/"+entryID, nil)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("want 204, got %d", rec.Code)
		}
	})

	t.Run("GET entries after delete returns empty list", func(t *testing.T) {
		rec := do(t, router, "GET", "/api/v1/events/"+eventID+"/entries", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("want 200, got %d", rec.Code)
		}
		out := mustDecode[[]models.Entry](t, rec.Body.Bytes())
		if len(out) != 0 {
			t.Fatalf("want 0 entries after delete, got %d", len(out))
		}
	})
}
