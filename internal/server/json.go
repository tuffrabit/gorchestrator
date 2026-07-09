package server

import (
	"encoding/json"
	"net/http"
	"strings"
)

// parseRequestForm populates r.Form for both urlencoded and multipart POSTs.
// Calling ParseForm alone on multipart/form-data leaves Form non-nil but empty,
// which then prevents FormValue from running ParseMultipartForm.
func parseRequestForm(r *http.Request) error {
	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "multipart/form-data") {
		return r.ParseMultipartForm(32 << 20)
	}
	return r.ParseForm()
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func decodeJSON(r *http.Request, dst any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}
