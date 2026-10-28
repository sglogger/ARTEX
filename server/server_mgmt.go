package server

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/Autumn-27/artex/db"
)

// pgReady returns the PG handle, or writes 503 and returns nil if unavailable.
func (s *Server) pg(w http.ResponseWriter) *db.DB {
	if s.m.pg == nil {
		writeErr(w, 503, "admin data source (PostgreSQL) not connected")
		return nil
	}
	return s.m.pg
}

func pathInt(r *http.Request, name string) (int64, bool) {
	n, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	return n, err == nil
}

func decode(r *http.Request, v any) error { return json.NewDecoder(r.Body).Decode(v) }
