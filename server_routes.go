package pot

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		var err error
		var content any

		// trim the leading slash as bucket paths are relative
		relPath := strings.TrimPrefix(r.URL.Path, "/")

		callOpts := []CallOpt{}
		if r.URL.Query().Has("batch") {
			callOpts = append(callOpts, WithBatch())
		}

		if r.URL.Query().Has("norewrite") {
			strDur := r.URL.Query().Get("norewrite")
			dur, err := time.ParseDuration(strDur)
			if err != nil {
				dur = time.Duration(0)
			}

			callOpts = append(callOpts, WithNoRewrite(dur))

			if r.URL.Query().Has("generation") {
				gen, err := strconv.ParseInt(r.URL.Query().Get("generation"), 10, 64)
				if err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}

				callOpts = append(callOpts, WithRewriteGeneration(gen))
			}
		}

		switch r.Method {
		case http.MethodGet:
			// if the path has a :list suffix then we want to list the keys
			if strings.HasSuffix(relPath, ":list") {
				content, err = s.ListPaths(r.Context(), strings.TrimSuffix(relPath, ":list"))
			} else {
				content, err = s.Get(r.Context(), relPath)
			}

		case http.MethodPost:
			content, err = s.Create(r.Context(), relPath, r.Body, callOpts...)
			if err == nil {
				w.WriteHeader(http.StatusCreated)
			}

		case http.MethodDelete:
			err = s.Remove(r.Context(), relPath, r.URL.Query()["key"]...)

		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}

		if err != nil {
			// norewrite violation returns
			if errors.Is(err, ErrNoRewriteViolated) {
				w.WriteHeader(http.StatusLocked)
			} else {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}

		// encode the content to the response
		if err := json.NewEncoder(w).Encode(content); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}

		if s.zip != "" && (r.Method == http.MethodPost || r.Method == http.MethodDelete) {
			if err := s.Zip(r.Context(), s.zip); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
		}
	})

	handler := otelhttp.NewHandler(mux, "/")
	return handler
}
