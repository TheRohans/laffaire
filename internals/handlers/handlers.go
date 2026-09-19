package handlers

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"github.com/robrohan/laffaire/internals/env"
	"github.com/robrohan/laffaire/internals/ical"
	"github.com/robrohan/laffaire/internals/models"
)

type pageData struct {
	Title       string
	CompanyName string
	User        *models.User
}

type eventListPageData struct {
	pageData
	Events *[]models.Event
}

type eventPageData struct {
	pageData
	Event *models.Event
}

type entriesListPageData struct {
	pageData
	EventUUID *uuid.UUID
	Entries   *[]models.Entry
}

type entryPageData struct {
	pageData
	Entry *models.Entry
}

func TemplateInit() *template.Template {
	t, err := template.ParseGlob("./templates/*")
	if err != nil {
		slog.Default().Error("cannot parse templates", "error", err)
		os.Exit(-1)
	}

	return t
}

func createDateTime(date string, time string) string {
	if date == "" {
		return ""
	}
	date = strings.Replace(date, "-", "", -1)
	if time != "" {
		time = strings.Replace(time, ":", "", -1)
		date = date + "T" + time + "00"
	} else {
		date = date + "T000000"
	}
	return date
}

func IcalPage(e *env.Env, t *template.Template) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		vars := mux.Vars(r)
		e.Log.Debug("ical request", "id", vars["id"])

		eventUuid, _ := uuid.Parse(vars["id"])

		event, err := e.Repo.GetEventById(eventUuid)
		if err != nil {
			e.Log.Error("cannot get the event from the db", "error", err)
			return
		}

		timezone := "UTC"
		ownerUuid, err := uuid.Parse(event.UserId)
		if err == nil {
			owner, err := e.Repo.GetUserById(ownerUuid)
			if err == nil && owner != nil && owner.Timezone != nil && *owner.Timezone != "" {
				timezone = *owner.Timezone
			}
		}

		entries, err := e.Repo.GetEntriesByEventId(eventUuid)
		if err != nil {
			e.Log.Error("cannot get the entries from the db", "error", err)
			return
		}

		e.Log.Debug("fetched entries", "count", len(*entries))

		calendarName := event.Title
		prodIdName := strings.ReplaceAll(calendarName, "//", "-")
		var ics bytes.Buffer
		e.Log.Debug("creating prolog")
		ical.Prolog(&ics, calendarName, fmt.Sprintf("-//Laffaire/%v//EN", prodIdName), timezone)
		for i := 0; i < len(*entries); i++ {
			e := (*entries)[i]

			start := createDateTime(e.StartDate, e.StartTime)
			end := createDateTime(e.EndDate, e.EndTime)
			calid := strings.Split(e.UUID, "-")[0]
			timestamp := int32(time.Now().Unix())

			if end == "" {
				end = start
			}

			if start != "" {
				ics.WriteString("BEGIN:VEVENT\r\n")
				fmt.Fprintf(&ics, "DTSTAMP:%v\r\n", start)
				fmt.Fprintf(&ics, "UID:R-%v-%v\r\n", calid, timestamp)
				if e.AllDayEvent {
					startDateOnly := strings.Replace(e.StartDate, "-", "", -1)
					endDateOnly := strings.Replace(e.EndDate, "-", "", -1)
					if endDateOnly == "" {
						endDateOnly = startDateOnly
					}
					fmt.Fprintf(&ics, "DTSTART;VALUE=DATE:%v\r\n", startDateOnly)
					fmt.Fprintf(&ics, "DTEND;VALUE=DATE:%v\r\n", endDateOnly)
				} else {
					fmt.Fprintf(&ics, "DTSTART;TZID=%v:%v\r\n", timezone, start)
					fmt.Fprintf(&ics, "DTEND;TZID=%v:%v\r\n", timezone, end)
				}
				fmt.Fprintf(&ics, "SUMMARY:%v\r\n", e.Subject)
				fmt.Fprintf(&ics, "DESCRIPTION:%v\r\n", e.Description)
				fmt.Fprintf(&ics, "CATEGORIES:%v\r\n", calendarName)
				ics.WriteString("END:VEVENT\r\n")
			}
		}
		e.Log.Debug("writing epilog")
		ical.Epilog(&ics)

		w.Header().Set("Content-Type", "text/calendar")
		w.Write(ics.Bytes())
	}
}

func EntriesPage(e *env.Env, t *template.Template) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		page := "entries.html"
		pd := entriesListPageData{
			pageData{
				"Laffaire Home",
				"Laffaire",
				env.UserFromContext(r.Context()),
			},
			nil,
			nil,
		}

		eventId := r.URL.Query().Get("event")
		eventUuid, _ := uuid.Parse(eventId)
		entries, err := e.Repo.GetEntriesByEventId(eventUuid)
		if err != nil {
			e.Log.Error("cannot get entries from the db", "error", err)
			return
		}

		pd.Entries = entries
		pd.EventUUID = &eventUuid

		if t.Lookup(page) != nil {
			t.ExecuteTemplate(w, page, pd)
		}
	}
}

func EntryPage(e *env.Env, t *template.Template) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		page := "entry.html"
		pd := entryPageData{
			pageData{
				"Laffaire Home",
				"Laffaire",
				env.UserFromContext(r.Context()),
			},
			nil,
		}

		// We should always have an event id
		eventUuid := r.FormValue("event_uuid")
		if eventUuid == "" {
			eventUuid = r.URL.Query().Get("event")
		}
		e.Log.Debug("have event uuid", "uuid", eventUuid)

		switch r.Method {
		case "POST":
			r.ParseForm()
			entryUuid := r.FormValue("entry_uuid")

			e.Log.Debug("entry uuid from form", "uuid", entryUuid)

			allday := r.FormValue("all_day_event")
			private := r.FormValue("private")

			if entryUuid != "" {
				entry := models.Entry{
					UUID:        entryUuid,
					EventId:     eventUuid,
					Subject:     r.FormValue("subject"),
					StartDate:   r.FormValue("start_date"),
					StartTime:   r.FormValue("start_time"),
					EndDate:     r.FormValue("end_date"),
					EndTime:     r.FormValue("end_time"),
					AllDayEvent: (allday == "on"),
					Description: r.FormValue("description"),
					Location:    r.FormValue("location"),
					Private:     (private == "on"),
				}
				err := e.Repo.UpsertEntry(&entry)
				if err != nil {
					e.Log.Error("upsert error", "error", err)
					return
				}

				http.Redirect(w, r, "/-/entries?event="+eventUuid, http.StatusFound)
			}
		case "GET":
			entryUuid := r.URL.Query().Get("entry")
			if entryUuid != "" {
				entryId, err := uuid.Parse(entryUuid)
				if err != nil {
					e.Log.Error("cannot parse entry uuid", "error", err)
					return
				}
				entry, err := e.Repo.GetEntryById(entryId)
				if err != nil {
					e.Log.Error("cannot get entry from the db", "error", err)
					return
				}
				pd.Entry = entry
			} else {
				entry := models.Entry{
					UUID:    uuid.New().String(),
					EventId: eventUuid,
				}
				pd.Entry = &entry
			}
		case "DELETE":
			e.Log.Debug("delete entry")
			entryUuid := r.URL.Query().Get("entry")
			eventUuid := r.URL.Query().Get("event")
			e.Log.Debug("delete entry", "entry", entryUuid, "event", eventUuid)

			err := e.Repo.DeleteEntry(entryUuid, eventUuid)
			if err != nil {
				e.Log.Error("cannot delete the entry from the db", "error", err)
				return
			}
			http.Redirect(w, r, "/-/entries?event="+eventUuid, http.StatusTemporaryRedirect)
			return
		}

		if t.Lookup(page) != nil {
			t.ExecuteTemplate(w, page, pd)
		}
	}
}

func EventsPage(e *env.Env, t *template.Template) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		page := "events.html"

		user := env.UserFromContext(r.Context())
		userId, _ := uuid.Parse(user.UUID)
		events, err := e.Repo.GetEventsByUserId(userId)
		if err != nil {
			e.Log.Error("events query errored", "error", err)
			return
		}

		pd := eventListPageData{
			pageData{
				"Laffaire Home",
				"Laffaire",
				user,
			},
			events,
		}
		if t.Lookup(page) != nil {
			t.ExecuteTemplate(w, page, pd)
		}
	}
}

func EventPage(e *env.Env, t *template.Template) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		page := "event.html"

		user := env.UserFromContext(r.Context())
		pd := eventPageData{
			pageData{
				"Laffaire Home",
				"Laffaire",
				user,
			},
			nil,
		}

		switch r.Method {
		case "POST":
			r.ParseForm()
			eventUuid := r.FormValue("event_uuid")
			e.Log.Debug("event uuid from form", "uuid", eventUuid)

			if eventUuid != "" {
				event := models.Event{
					UUID:        eventUuid,
					UserId:      user.UUID,
					Title:       r.FormValue("title"),
					Description: r.FormValue("description"),
				}
				e.Repo.UpsertEvent(&event)

				http.Redirect(w, r, "/-/events", http.StatusFound)
			}
		case "GET":
			eventUuid := r.URL.Query().Get("event")
			if eventUuid != "" {
				eventId, err := uuid.Parse(eventUuid)
				if err != nil {
					e.Log.Error("cannot parse event uuid", "error", err)
					return
				}
				event, err := e.Repo.GetEventById(eventId)
				if err != nil {
					e.Log.Error("cannot get the event from the db", "error", err)
					return
				}
				pd.Event = event
			} else {
				event := models.Event{
					UUID: uuid.New().String(),
				}
				pd.Event = &event
			}
		}

		if t.Lookup(page) != nil {
			t.ExecuteTemplate(w, page, pd)
		}
	}
}

var availableTimezones = []string{
	"UTC",
	"America/New_York", "America/Chicago", "America/Denver", "America/Los_Angeles",
	"America/Toronto", "America/Vancouver", "America/Sao_Paulo",
	"Europe/London", "Europe/Paris", "Europe/Berlin", "Europe/Amsterdam",
	"Africa/Cairo", "Africa/Johannesburg",
	"Asia/Dubai", "Asia/Kolkata", "Asia/Singapore", "Asia/Tokyo", "Asia/Shanghai",
	"Australia/Sydney", "Australia/Perth",
	"Pacific/Auckland", "Pacific/Honolulu",
}

type tokenListPageData struct {
	pageData
	Tokens          *[]models.Token
	Timezones       []string
	CurrentTimezone string
}

type tokenPageData struct {
	pageData
	NewToken *models.Token // nil = show form; non-nil = show newly created token
}

func SettingsPage(e *env.Env, t *template.Template) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		page := "settings.html"
		user := env.UserFromContext(r.Context())

		if r.Method == "DELETE" {
			tokenUuid := r.URL.Query().Get("token")
			if err := e.Repo.DeleteToken(tokenUuid, user.UUID); err != nil {
				e.Log.Error("delete token error", "error", err)
			}
			http.Redirect(w, r, "/-/settings", http.StatusTemporaryRedirect)
			return
		}

		if r.Method == "POST" {
			r.ParseForm()
			tz := r.FormValue("timezone")
			if tz != "" {
				if err := e.Repo.UpdateUserTimezone(user.UUID, tz); err != nil {
					e.Log.Error("update timezone error", "error", err)
				} else {
					user.Timezone = &tz
				}
			}
		}

		userId, _ := uuid.Parse(user.UUID)
		tokens, err := e.Repo.GetTokensByUserId(userId)
		if err != nil {
			e.Log.Error("tokens query errored", "error", err)
			return
		}

		currentTimezone := "UTC"
		if user.Timezone != nil && *user.Timezone != "" {
			currentTimezone = *user.Timezone
		}

		pd := tokenListPageData{
			pageData{"Laffaire Settings", "Laffaire", user},
			tokens,
			availableTimezones,
			currentTimezone,
		}
		if t.Lookup(page) != nil {
			t.ExecuteTemplate(w, page, pd)
		}
	}
}

func TokenPage(e *env.Env, t *template.Template) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		page := "token.html"
		user := env.UserFromContext(r.Context())
		pd := tokenPageData{
			pageData{"Laffaire Token", "Laffaire", user},
			nil,
		}

		if r.Method == "POST" {
			r.ParseForm()
			name := r.FormValue("name")
			if name != "" {
				b := make([]byte, 32)
				if _, err := rand.Read(b); err != nil {
					e.Log.Error("failed to generate token", "error", err)
					return
				}
				token := models.Token{
					UUID:      uuid.New().String(),
					UserId:    user.UUID,
					Name:      name,
					Token:     fmt.Sprintf("%x", b),
					CreatedAt: time.Now().UTC().Format(time.RFC3339),
				}
				if err := e.Repo.CreateToken(&token); err != nil {
					e.Log.Error("create token error", "error", err)
					return
				}
				pd.NewToken = &token
			}
		}

		if t.Lookup(page) != nil {
			t.ExecuteTemplate(w, page, pd)
		}
	}
}

func ServePage(e *env.Env, t *template.Template) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		routeMatch, _ := regexp.Compile(`\/(\w+)`)
		pd := pageData{
			"Laffaire Home",
			"Laffaire",
			env.UserFromContext(r.Context()),
		}

		matches := routeMatch.FindStringSubmatch(r.URL.Path)

		e.Log.Debug("request", "path", r.URL.Path)
		e.Log.Debug("request", "match", matches)

		if len(matches) >= 1 {
			page := matches[1] + ".html"
			if t.Lookup(page) != nil {
				w.WriteHeader(200)
				t.ExecuteTemplate(w, page, pd)
				return
			}
		} else if r.URL.Path == "/" {
			w.WriteHeader(200)
			t.ExecuteTemplate(w, "index.html", pd)
			return
		}

		w.WriteHeader(404)
		w.Write([]byte("Not Found"))
	}
}
