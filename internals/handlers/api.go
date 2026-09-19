package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"github.com/robrohan/laffaire/internals/env"
	"github.com/robrohan/laffaire/internals/models"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// -------------------------------------------------------------------------
// Events

func APIGetEvents(e *env.Env) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userId, err := uuid.Parse(env.UserFromContext(r.Context()).UUID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "invalid user id")
			return
		}
		events, err := e.Repo.GetEventsByUserId(userId)
		if err != nil {
			e.Log.Error("GetEventsByUserId failed", "error", err)
			writeError(w, http.StatusInternalServerError, "could not retrieve events")
			return
		}
		writeJSON(w, http.StatusOK, events)
	}
}

func APICreateEvent(e *env.Env) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Title       string `json:"title"`
			Description string `json:"description"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if input.Title == "" {
			writeError(w, http.StatusBadRequest, "title is required")
			return
		}
		event := models.Event{
			UUID:        uuid.New().String(),
			UserId:      env.UserFromContext(r.Context()).UUID,
			Title:       input.Title,
			Description: input.Description,
		}
		if err := e.Repo.UpsertEvent(&event); err != nil {
			e.Log.Error("UpsertEvent failed", "error", err)
			writeError(w, http.StatusInternalServerError, "could not create event")
			return
		}
		writeJSON(w, http.StatusCreated, event)
	}
}

func APIGetEvent(e *env.Env) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		eventId, err := uuid.Parse(mux.Vars(r)["id"])
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid event id")
			return
		}
		event, err := e.Repo.GetEventById(eventId)
		if err != nil {
			e.Log.Error("GetEventById failed", "error", err)
			writeError(w, http.StatusNotFound, "event not found")
			return
		}
		if event.UserId != env.UserFromContext(r.Context()).UUID {
			writeError(w, http.StatusForbidden, "forbidden")
			return
		}
		writeJSON(w, http.StatusOK, event)
	}
}

func APIUpdateEvent(e *env.Env) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		eventId, err := uuid.Parse(mux.Vars(r)["id"])
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid event id")
			return
		}
		existing, err := e.Repo.GetEventById(eventId)
		if err != nil {
			writeError(w, http.StatusNotFound, "event not found")
			return
		}
		if existing.UserId != env.UserFromContext(r.Context()).UUID {
			writeError(w, http.StatusForbidden, "forbidden")
			return
		}

		var input struct {
			Title       string `json:"title"`
			Description string `json:"description"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if input.Title == "" {
			writeError(w, http.StatusBadRequest, "title is required")
			return
		}

		existing.Title = input.Title
		existing.Description = input.Description
		if err := e.Repo.UpsertEvent(existing); err != nil {
			e.Log.Error("UpsertEvent failed", "error", err)
			writeError(w, http.StatusInternalServerError, "could not update event")
			return
		}
		writeJSON(w, http.StatusOK, existing)
	}
}

func APIDeleteEvent(e *env.Env) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		eventId := mux.Vars(r)["id"]
		if _, err := uuid.Parse(eventId); err != nil {
			writeError(w, http.StatusBadRequest, "invalid event id")
			return
		}
		if err := e.Repo.DeleteEvent(eventId, env.UserFromContext(r.Context()).UUID); err != nil {
			e.Log.Error("DeleteEvent failed", "error", err)
			writeError(w, http.StatusInternalServerError, "could not delete event")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// -------------------------------------------------------------------------
// Entries

func APIGetEntries(e *env.Env) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		eventId, err := uuid.Parse(mux.Vars(r)["id"])
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid event id")
			return
		}
		// Verify the event belongs to this user
		event, err := e.Repo.GetEventById(eventId)
		if err != nil {
			writeError(w, http.StatusNotFound, "event not found")
			return
		}
		if event.UserId != env.UserFromContext(r.Context()).UUID {
			writeError(w, http.StatusForbidden, "forbidden")
			return
		}

		entries, err := e.Repo.GetEntriesByEventId(eventId)
		if err != nil {
			e.Log.Error("GetEntriesByEventId failed", "error", err)
			writeError(w, http.StatusInternalServerError, "could not retrieve entries")
			return
		}
		writeJSON(w, http.StatusOK, entries)
	}
}

func APICreateEntry(e *env.Env) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			EventId     string `json:"event_id"`
			Subject     string `json:"subject"`
			StartDate   string `json:"start_date"`
			StartTime   string `json:"start_time"`
			EndDate     string `json:"end_date"`
			EndTime     string `json:"end_time"`
			AllDayEvent bool   `json:"all_day_event"`
			Description string `json:"description"`
			Location    string `json:"location"`
			Private     bool   `json:"private"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if input.EventId == "" || input.Subject == "" {
			writeError(w, http.StatusBadRequest, "event_id and subject are required")
			return
		}

		eventId, err := uuid.Parse(input.EventId)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid event_id")
			return
		}
		event, err := e.Repo.GetEventById(eventId)
		if err != nil {
			writeError(w, http.StatusNotFound, "event not found")
			return
		}
		if event.UserId != env.UserFromContext(r.Context()).UUID {
			writeError(w, http.StatusForbidden, "forbidden")
			return
		}

		entry := models.Entry{
			UUID:        uuid.New().String(),
			EventId:     input.EventId,
			Subject:     input.Subject,
			StartDate:   input.StartDate,
			StartTime:   input.StartTime,
			EndDate:     input.EndDate,
			EndTime:     input.EndTime,
			AllDayEvent: input.AllDayEvent,
			Description: input.Description,
			Location:    input.Location,
			Private:     input.Private,
		}
		if err := e.Repo.UpsertEntry(&entry); err != nil {
			e.Log.Error("UpsertEntry failed", "error", err)
			writeError(w, http.StatusInternalServerError, "could not create entry")
			return
		}
		writeJSON(w, http.StatusCreated, entry)
	}
}

func APIGetEntry(e *env.Env) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		entryId, err := uuid.Parse(mux.Vars(r)["id"])
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid entry id")
			return
		}
		entry, err := e.Repo.GetEntryById(entryId)
		if err != nil {
			e.Log.Error("GetEntryById failed", "error", err)
			writeError(w, http.StatusNotFound, "entry not found")
			return
		}
		// Verify ownership via the parent event
		eventId, err := uuid.Parse(entry.EventId)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "invalid event reference")
			return
		}
		event, err := e.Repo.GetEventById(eventId)
		if err != nil || event.UserId != env.UserFromContext(r.Context()).UUID {
			writeError(w, http.StatusForbidden, "forbidden")
			return
		}
		writeJSON(w, http.StatusOK, entry)
	}
}

func APIUpdateEntry(e *env.Env) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		entryId, err := uuid.Parse(mux.Vars(r)["id"])
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid entry id")
			return
		}
		existing, err := e.Repo.GetEntryById(entryId)
		if err != nil {
			writeError(w, http.StatusNotFound, "entry not found")
			return
		}
		eventId, err := uuid.Parse(existing.EventId)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "invalid event reference")
			return
		}
		event, err := e.Repo.GetEventById(eventId)
		if err != nil || event.UserId != env.UserFromContext(r.Context()).UUID {
			writeError(w, http.StatusForbidden, "forbidden")
			return
		}

		var input struct {
			Subject     string `json:"subject"`
			StartDate   string `json:"start_date"`
			StartTime   string `json:"start_time"`
			EndDate     string `json:"end_date"`
			EndTime     string `json:"end_time"`
			AllDayEvent bool   `json:"all_day_event"`
			Description string `json:"description"`
			Location    string `json:"location"`
			Private     bool   `json:"private"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if input.Subject == "" {
			writeError(w, http.StatusBadRequest, "subject is required")
			return
		}

		existing.Subject = input.Subject
		existing.StartDate = input.StartDate
		existing.StartTime = input.StartTime
		existing.EndDate = input.EndDate
		existing.EndTime = input.EndTime
		existing.AllDayEvent = input.AllDayEvent
		existing.Description = input.Description
		existing.Location = input.Location
		existing.Private = input.Private

		if err := e.Repo.UpsertEntry(existing); err != nil {
			e.Log.Error("UpsertEntry failed", "error", err)
			writeError(w, http.StatusInternalServerError, "could not update entry")
			return
		}
		writeJSON(w, http.StatusOK, existing)
	}
}

func APIDeleteEntry(e *env.Env) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		entryId, err := uuid.Parse(mux.Vars(r)["id"])
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid entry id")
			return
		}
		existing, err := e.Repo.GetEntryById(entryId)
		if err != nil {
			writeError(w, http.StatusNotFound, "entry not found")
			return
		}
		eventId, err := uuid.Parse(existing.EventId)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "invalid event reference")
			return
		}
		event, err := e.Repo.GetEventById(eventId)
		if err != nil || event.UserId != env.UserFromContext(r.Context()).UUID {
			writeError(w, http.StatusForbidden, "forbidden")
			return
		}

		if err := e.Repo.DeleteEntry(entryId.String(), existing.EventId); err != nil {
			e.Log.Error("DeleteEntry failed", "error", err)
			writeError(w, http.StatusInternalServerError, "could not delete entry")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
