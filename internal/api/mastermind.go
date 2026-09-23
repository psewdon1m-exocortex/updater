package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

func (s Server) mastermind(mux *http.ServeMux) {
	mux.HandleFunc("POST /v2/jobs/{id}/rollback-saved-spool", func(w http.ResponseWriter, r *http.Request) {
		job, ok := s.Store.Get(r.PathValue("id"))
		if !ok {
			writeError(w, 404, errors.New("job not found"))
			return
		}
		if err := s.authorize(r, job.HeadID); err != nil {
			writeError(w, 401, err)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		var input struct {
			SpoolID       string `json:"spool_id"`
			OperatorSaved bool   `json:"operator_saved"`
		}
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF {
			writeError(w, 400, errors.New("invalid saved-copy recovery request"))
			return
		}
		result, err := s.Engine.RollbackGroupSaved(job.ID, input.SpoolID, input.OperatorSaved)
		if err != nil {
			writeError(w, 409, err)
			return
		}
		writeJSON(w, 202, result)
	})
	mux.HandleFunc("POST /v1/heads/{head}/preparations", func(w http.ResponseWriter, r *http.Request) {
		if err := s.authorize(r, r.PathValue("head")); err != nil {
			writeError(w, 401, err)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		var input struct {
			RequestID  string `json:"request_id"`
			Version    string `json:"version"`
			RollbackOf string `json:"rollback_of"`
		}
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			writeError(w, 400, errors.New("invalid preparation request"))
			return
		}
		if decoder.Decode(new(any)) != io.EOF {
			writeError(w, 400, errors.New("trailing preparation input"))
			return
		}
		result, err := s.Engine.PrepareMastermind(r.Context(), r.PathValue("head"), input.RequestID, input.Version, input.RollbackOf)
		if err != nil {
			writeError(w, 409, err)
			return
		}
		writeJSON(w, 200, result)
	})
}
