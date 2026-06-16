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

// errBadStatus marks a non-200 response from an upstream call (e.g. Google's
// OAuth endpoints), so the OAuth flow can fail closed without leaking specifics.
var errBadStatus = errors.New("unexpected upstream status")

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

// Request-body size caps. Plan posts carry referenced-file snapshots (up to
// ~200 KB each), so they get a larger allowance than ordinary endpoints.
const (
	maxBodyBytes     = 1 << 20 // 1 MiB: every endpoint except plan posts
	maxPlanPostBytes = 8 << 20 // 8 MiB: create plan / add version (file snapshots)
)

// readJSON decodes the request body into dst, writing a 400 and returning false
// on failure.
func readJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	return readJSONLimit(w, r, dst, maxBodyBytes)
}

// readJSONLimit is readJSON with an explicit body-size cap.
func readJSONLimit(w http.ResponseWriter, r *http.Request, dst any, limit int64) bool {
	if err := decodeJSONLimit(r, dst, limit); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return false
	}
	return true
}

// readJSONLimitOrEmpty decodes a request body like readJSONLimit, but reports an
// empty body separately so compatibility endpoints can preserve no-body
// behavior without duplicating decoder setup.
func readJSONLimitOrEmpty(w http.ResponseWriter, r *http.Request, dst any, limit int64) (ok, empty bool) {
	if err := decodeJSONLimit(r, dst, limit); err != nil {
		if errors.Is(err, io.EOF) {
			return false, true
		}
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return false, false
	}
	return true, false
}

func decodeJSONLimit(r *http.Request, dst any, limit int64) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, limit))
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
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
