package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"updater/internal/config"
	"updater/internal/state"
)

type progressingBody struct {
	reader   io.Reader
	response *http.ResponseController
}

func (p progressingBody) Read(bytes []byte) (int, error) {
	// net/http supports this on the real Unix-socket server; test recorders need not.
	_ = p.response.SetReadDeadline(time.Now().Add(60 * time.Second))
	return p.reader.Read(bytes)
}

func (s Server) backupSpools(mux *http.ServeMux) {
	authorize := func(w http.ResponseWriter, r *http.Request) bool {
		if err := s.authorize(r, r.PathValue("head")); err != nil {
			writeError(w, 401, err)
			return false
		}
		head, err := config.LoadHead(s.Runtime, r.PathValue("head"))
		if err != nil || head.Service != "mastermind" {
			writeError(w, 403, errors.New("this head does not support Mastermind backup spools"))
			return false
		}
		return true
	}
	failure := func(w http.ResponseWriter, err error) {
		status := 400
		if errors.Is(err, state.ErrSpoolBusy) {
			status = 409
		}
		if errors.Is(err, state.ErrSpoolQuota) {
			status = 507
		}
		// Filesystem errors must not return host paths or deployment details to clients.
		writeError(w, status, errors.New("backup spool could not complete this operation"))
	}
	mux.HandleFunc("DELETE /v1/heads/{head}/backup-spools/{spool}", func(w http.ResponseWriter, r *http.Request) {
		if !authorize(w, r) {
			return
		}
		// An application can discard only its unclaimed preparation. Once handed
		// off, cleanup belongs to the privileged job, including after a lost reply.
		if err := s.Store.ReleaseSpool(r.PathValue("head"), r.PathValue("spool"), ""); err != nil {
			failure(w, err)
			return
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("POST /v1/heads/{head}/backup-spools", func(w http.ResponseWriter, r *http.Request) {
		if !authorize(w, r) {
			return
		}
		var input struct {
			RequestID string `json:"request_id"`
			Filename  string `json:"filename"`
			Size      int64  `json:"size"`
			SHA256    string `json:"sha256"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			failure(w, err)
			return
		}
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			failure(w, state.ErrSpoolInvalid)
			return
		}
		item, err := s.Store.CreateSpool(r.PathValue("head"), input.RequestID, input.Filename, input.Size, input.SHA256, time.Now())
		if err != nil {
			failure(w, err)
			return
		}
		writeJSON(w, 201, item)
	})
	mux.HandleFunc("PUT /v1/heads/{head}/backup-spools/{spool}/content", func(w http.ResponseWriter, r *http.Request) {
		if !authorize(w, r) {
			return
		}
		if r.ContentLength <= 0 || r.ContentLength > state.MaximumSpoolBytes || r.Header.Get("Content-Encoding") != "" {
			failure(w, state.ErrSpoolInvalid)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, state.MaximumSpoolBytes)
		ctx, cancel := context.WithTimeout(r.Context(), time.Hour)
		defer cancel()
		done := make(chan struct{})
		defer close(done)
		go func() {
			select {
			case <-ctx.Done():
				r.Body.Close()
			case <-done:
			}
		}()
		err := s.Store.UploadSpool(ctx, r.PathValue("head"), r.PathValue("spool"), progressingBody{r.Body, http.NewResponseController(w)}, r.ContentLength)
		if err != nil {
			failure(w, err)
			return
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("POST /v1/heads/{head}/backup-spools/{spool}/seal", func(w http.ResponseWriter, r *http.Request) {
		if !authorize(w, r) {
			return
		}
		if r.ContentLength > 0 {
			failure(w, state.ErrSpoolInvalid)
			return
		}
		item, err := s.Store.SealSpool(r.PathValue("head"), r.PathValue("spool"))
		if err != nil {
			failure(w, err)
			return
		}
		writeJSON(w, 200, item)
	})
}
