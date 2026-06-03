package web

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"

	"planner/internal/store"
)

// errBadVersion is returned when a {n} path value is neither an integer nor
// "latest".
var errBadVersion = errors.New("bad version")

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("encode response: %v", err)
	}
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// readJSON decodes the request body into dst, writing a 400 and returning false
// on failure.
func readJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20)) // 1 MiB cap
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return false
	}
	return true
}

func writeServerError(w http.ResponseWriter, err error) {
	log.Printf("server error: %v", err)
	writeJSONError(w, http.StatusInternalServerError, "internal server error")
}

func writeNotFoundOr(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeJSONError(w, http.StatusNotFound, "not found")
		return
	}
	writeServerError(w, err)
}

// writeVersionErr handles errors from resolveVersionNumber: a malformed version
// is a 400, a missing latest version is a 404.
func writeVersionErr(w http.ResponseWriter, err error) {
	if errors.Is(err, errBadVersion) {
		writeJSONError(w, http.StatusBadRequest, "bad version")
		return
	}
	writeNotFoundOr(w, err)
}
