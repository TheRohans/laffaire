package main

import (
	"context"
	"crypto/md5"
	"encoding/json"
	"expvar"
	"fmt"
	"io"
	"log"
	"log/slog"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ardanlabs/conf"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	_ "github.com/lib/pq"
	_ "github.com/mattn/go-sqlite3"
	"github.com/pkg/errors"
	"github.com/robrohan/laffaire/internals/env"
	"github.com/robrohan/laffaire/internals/handlers"
	"github.com/robrohan/laffaire/internals/models"
	"github.com/robrohan/laffaire/internals/repository"
	"golang.org/x/oauth2"
)

// will be replaced with git hash
var build = "develop"

var cookieName = "WB_AT"

func main() {
	if err := run(); err != nil {
		log.Println("error :", err)
		os.Exit(1)
	}
}

func run() error {
	// =========================================================================
	// Logging
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug, AddSource: false}))

	// =========================================================================
	// Configuration
	cfg := models.Config{}

	if err := conf.Parse(os.Args[1:], "WB", &cfg); err != nil {
		if err == conf.ErrHelpWanted {
			usage, err := conf.Usage("WB", &cfg)
			if err != nil {
				return errors.Wrap(err, "generating config usage")
			}
			fmt.Println(usage)
			return nil
		}
		return errors.Wrap(err, "parsing config")
	}

	var endpoint = oauth2.Endpoint{
		AuthURL:   cfg.Auth.AuthURL,
		TokenURL:  cfg.Auth.TokenURL,
		AuthStyle: oauth2.AuthStyle(cfg.Auth.AuthStyle),
	}

	oauthConfig := &oauth2.Config{
		RedirectURL:  cfg.Auth.RedirectURL,
		ClientID:     cfg.Auth.ClientID,
		ClientSecret: cfg.Auth.ClientSecret,
		Scopes:       cfg.Auth.Scopes,
		Endpoint:     endpoint,
	}

	// log.Printf("%v", cfg.Auth.ClientID)

	// =========================================================================
	// App Starting
	expvar.NewString("build").Set(build)
	log.Info("started : application initializing", "version", build)
	defer log.Info("Completed")

	out, err := conf.String(&cfg)
	if err != nil {
		return errors.Wrap(err, "generating config for output")
	}
	log.Debug(out)

	// =========================================================================
	// Start Database
	log.Info("initializing database support")

	db, err := repository.OpenDatabase(
		cfg.DB.Driver, cfg.DB.Connection, cfg.Base.Root)
	if err != nil {
		log.Error(err.Error())
	}
	defer func() {
		log.Info("database stopping", "connection", cfg.DB.Connection)
		db.Close()
	}()

	// =========================================================================
	// Start Debug Service
	//
	// /debug/pprof - Added to the default mux by importing the net/http/pprof package.
	// /debug/vars - Added to the default mux by importing the expvar package.
	//
	// Not concerned with shutting this down when the application is shutdown.
	log.Info("initializing debugging support")
	go func() {
		log.Info("debug listening", "address", cfg.Web.DebugHost)
		log.Info("debug listener closed", "host", http.ListenAndServe(cfg.Web.DebugHost, http.DefaultServeMux))
	}()

	// Put the API on top of the connection
	repo := repository.Attach(cfg.Base.Root, db, cfg.DB.Driver)

	// =========================================================================
	// Setup template handling
	templates := handlers.TemplateInit()

	// =========================================================================
	// Start API Service
	log.Info("initializing API support")

	router := mux.NewRouter() // .StrictSlash(true)

	// This is just a string we send to auth provider to
	// see if they are the one sending the response
	v := rand.Int()
	randState := fmt.Sprintf("%x", v)

	env := &env.Env{
		Db:        db,
		Log:       log,
		Router:    router,
		Cfg:       &cfg,
		RandState: randState,
		Repo:      repo,
	}

	// Routes
	{
		router.PathPrefix("/static").Handler(
			http.StripPrefix("/static/", http.FileServer(http.Dir("./static/"))))

		// URLs that start with /-/ will require login
		// Note: they are still defined down below as well
		secure := router.PathPrefix("/-/").Subrouter()
		secure.Use(LoginVerify(env, repo))

		//////////////////////////
		// Non-logged in pages
		router.HandleFunc("/", handlers.ServePage(env, templates))
		router.HandleFunc("/about", handlers.ServePage(env, templates))

		router.HandleFunc("/ical/{id}", handlers.IcalPage(env, templates)).Methods("GET")
		//////////////////////////
		// Routes needed for auth
		router.HandleFunc("/login", handleLogin(env, oauthConfig)).Methods("GET")
		router.HandleFunc("/callback", handleCallback(env, oauthConfig, repo)).Methods("GET")
		/////////////////////////
		// Secure pages... "the app"
		secure.HandleFunc("/logout", handleLogout(env)).Methods("GET")
		secure.HandleFunc("/home", handlers.ServePage(env, templates)).Methods("GET")

		secure.HandleFunc("/events", handlers.EventsPage(env, templates)).Methods("GET")
		secure.HandleFunc("/event", handlers.EventPage(env, templates)).Methods("GET", "POST", "DELETE")

		secure.HandleFunc("/entries", handlers.EntriesPage(env, templates)).Methods("GET")
		secure.HandleFunc("/entry", handlers.EntryPage(env, templates)).Methods("GET", "POST", "DELETE")

		secure.HandleFunc("/settings", handlers.SettingsPage(env, templates)).Methods("GET", "DELETE", "POST")
		secure.HandleFunc("/token", handlers.TokenPage(env, templates)).Methods("GET", "POST")

		/////////////////////////
		// JSON API v1 — returns 401 JSON on auth failure (no redirect)
		api := router.PathPrefix("/api/v1").Subrouter()
		api.Use(APILoginVerify(env, repo))

		api.HandleFunc("/events", handlers.APIGetEvents(env)).Methods("GET")
		api.HandleFunc("/events", handlers.APICreateEvent(env)).Methods("POST")
		api.HandleFunc("/events/{id}", handlers.APIGetEvent(env)).Methods("GET")
		api.HandleFunc("/events/{id}", handlers.APIUpdateEvent(env)).Methods("PUT")
		api.HandleFunc("/events/{id}", handlers.APIDeleteEvent(env)).Methods("DELETE")

		api.HandleFunc("/events/{id}/entries", handlers.APIGetEntries(env)).Methods("GET")
		api.HandleFunc("/entries", handlers.APICreateEntry(env)).Methods("POST")
		api.HandleFunc("/entries/{id}", handlers.APIGetEntry(env)).Methods("GET")
		api.HandleFunc("/entries/{id}", handlers.APIUpdateEntry(env)).Methods("PUT")
		api.HandleFunc("/entries/{id}", handlers.APIDeleteEntry(env)).Methods("DELETE")
	}

	api := http.Server{
		Addr:         cfg.Web.APIHost,
		Handler:      router,
		ReadTimeout:  cfg.Web.ReadTimeout,
		WriteTimeout: cfg.Web.WriteTimeout,
	}

	// =========================================================================
	// Nice Shutdown

	shutdown := make(chan os.Signal, 1)
	signal.Notify(shutdown, syscall.SIGINT, syscall.SIGTERM)
	serverErrors := make(chan error, 1)
	go func() {
		log.Info("API listening", "address", api.Addr)
		serverErrors <- api.ListenAndServe()
	}()

	select {
	case err := <-serverErrors:
		return fmt.Errorf("server error: %w", err)

	case sig := <-shutdown:
		log.Info(sig.String())
		defer log.Info(sig.String())

		ctx, cancel := context.WithTimeout(context.Background(), cfg.Web.ShutdownTimeout)
		defer cancel()

		if err := api.Shutdown(ctx); err != nil {
			api.Close()
			return fmt.Errorf("could not stop server gracefully: %w", err)
		}
	}

	return nil
}

func handleLogin(env *env.Env, oauth *oauth2.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// TODO: randState should be unique
		url := oauth.AuthCodeURL(env.RandState)
		http.Redirect(w, r, url, http.StatusTemporaryRedirect)
	}
}

func handleLogout(env *env.Env) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_ = env
		addCookie(w, cookieName, "", 0)
		http.Redirect(w, r, "/", http.StatusTemporaryRedirect)
	}
}

func handleCallback(env *env.Env, oauth *oauth2.Config, repo *repository.DataRepository) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.FormValue("state") != env.RandState {
			env.Log.Error("state is not valid")
			http.Redirect(w, r, "/", http.StatusTemporaryRedirect)
			return
		}

		token, err := oauth.Exchange(context.Background(), r.FormValue("code"))
		if err != nil {
			env.Log.Error(fmt.Sprintf("could not get token %v\n", err.Error()))
			http.Redirect(w, r, "/", http.StatusTemporaryRedirect)
			return
		}

		tokenURL := env.Cfg.Auth.AccessTokenURL + token.AccessToken
		resp, err := http.Get(tokenURL)
		if err != nil {
			env.Log.Error(fmt.Sprintf("could not create access token %v\n", err.Error()))
			http.Redirect(w, r, "/", http.StatusTemporaryRedirect)
			return
		}
		defer resp.Body.Close()

		content, err := io.ReadAll(resp.Body)
		if err != nil {
			env.Log.Error(fmt.Sprintf("could not parse response %v\n", err.Error()))
			http.Redirect(w, r, "/", http.StatusTemporaryRedirect)
			return
		}

		// We have user data in content
		userInfo := models.UserInfo{}
		json.Unmarshal(content, &userInfo)

		v := rand.Int()
		salt := fmt.Sprintf("%x", v)

		// Add user to our local database
		user := models.NewUser(userInfo.Id, userInfo.Email, userInfo.Picture)
		repo.UpsertUser(user, salt)
		user, err = repo.GetUser(userInfo.Email)
		if err != nil {
			env.Log.Error("could not get user", "error", err.Error())
			return
		}

		// make an entry in the users table?
		hash := md5.Sum([]byte(user.Email + user.AuthId + salt))
		addCookie(w, cookieName, fmt.Sprintf("%s:%x", user.UUID, hash), 30*24*time.Hour)

		http.Redirect(w, r, "/-/home", http.StatusFound)
	}
}

// addCookie will apply a new cookie to the response of a http request with the key/value specified.
func addCookie(w http.ResponseWriter, name, value string, ttl time.Duration) {
	expire := time.Now().Add(ttl)
	cookie := http.Cookie{
		Name:     name,
		Value:    value,
		Expires:  expire,
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	}
	http.SetCookie(w, &cookie)
}

func apiUnauthorized(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	w.Write([]byte(`{"error":"unauthorized"}`))
}

func APILoginVerify(e *env.Env, repo *repository.DataRepository) mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Bearer token takes priority — allows API clients to authenticate
			// without a browser session. The HTML UI is unaffected: it uses the
			// separate LoginVerify middleware on the /-/ subrouter.
			if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
				tokenValue := strings.TrimPrefix(auth, "Bearer ")
				user, err := repo.GetUserByToken(tokenValue)
				if err != nil {
					e.Log.Error("bearer token not found", "error", err)
					apiUnauthorized(w)
					return
				}
				next.ServeHTTP(w, r.WithContext(env.WithUser(r.Context(), user)))
				return
			}

			// Fall back to the session cookie set by the browser OAuth login.
			cookie, err := r.Cookie(cookieName)
			if err != nil || cookie.Value == "" {
				apiUnauthorized(w)
				return
			}

			parts := strings.Split(cookie.Value, ":")
			uid, err := uuid.Parse(parts[0])
			if err != nil {
				apiUnauthorized(w)
				return
			}

			user, err := repo.GetUserById(uid)
			if err != nil {
				apiUnauthorized(w)
				return
			}

			hashString := fmt.Sprintf("%s%s%s", user.Email, user.AuthId, *user.Salt)
			hash := md5.Sum([]byte(hashString))
			if fmt.Sprintf("%x", hash) != parts[1] {
				apiUnauthorized(w)
				return
			}

			next.ServeHTTP(w, r.WithContext(env.WithUser(r.Context(), user)))
		})
	}
}

func LoginVerify(e *env.Env, repo *repository.DataRepository) mux.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cookie, err := r.Cookie(cookieName)
			// No cookie at all...
			if err != nil {
				e.Log.Error("missing auth token")
				http.Redirect(w, r, "/login", http.StatusForbidden)
				return
			}

			// Cookie, but no value
			if cookie.Value == "" {
				e.Log.Error("missing auth token")
				http.Redirect(w, r, "/login", http.StatusForbidden)
				return
			}

			// Cookie with value, but not the user id key
			parts := strings.Split(cookie.Value, ":")
			uuid, err := uuid.Parse(parts[0])
			if err != nil {
				e.Log.Error("UUID malformed")
				http.Redirect(w, r, "/login", http.StatusForbidden)
				return
			}

			user, err := repo.GetUserById(uuid)
			if err != nil {
				e.Log.Error("UUID not found")
				http.Redirect(w, r, "/login", http.StatusForbidden)
				return
			}

			// Well formatted cookie, but hash changed for some reason
			hashString := fmt.Sprintf("%s%s%s", user.Email, user.AuthId, *user.Salt)
			hash := md5.Sum([]byte(hashString))
			if fmt.Sprintf("%x", hash) != parts[1] {
				e.Log.Error("hashes don't match. login might have expired")
				http.Redirect(w, r, "/login", http.StatusForbidden)
				return
			}

			// Ok, not thing wrong, move on
			next.ServeHTTP(w, r.WithContext(env.WithUser(r.Context(), user)))
		})
	}
}
