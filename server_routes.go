package pot

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gorilla/mux/otelmux"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

func (s *Server) Routes() http.Handler {
	mux := mux.NewRouter()

	if s.TracingOptions.Enabled {
		mux.Use(otelmux.Middleware("pot-server"))
	}

	mux.
		Methods(http.MethodGet).
		PathPrefix("/").
		HandlerFunc(s.routeGetFunc)

	mux.
		Methods(http.MethodPost).
		PathPrefix("/").
		HandlerFunc(s.routePostFunc)

	mux.
		Methods(http.MethodDelete).
		PathPrefix("/").
		HandlerFunc(s.routeDeleteFunc)

	return mux
}

func (s *Server) routeGetFunc(w http.ResponseWriter, r *http.Request) {
	var err error
	var content any

	relPath := strings.TrimPrefix(r.URL.Path, "/")

	// if the path has a :list suffix then we want to list the keys
	if strings.HasSuffix(relPath, ":list") {
		content, err = s.ListPaths(r.Context(), strings.TrimSuffix(relPath, ":list"))
	} else {
		content, err = s.Get(r.Context(), relPath)
	}

	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// encode the content to the response
	if err := json.NewEncoder(w).Encode(content); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}

	if s.MetricsOptions.Enabled {
		if strings.HasSuffix(relPath, ":list") {
			s.MetricsOptions.PotLists.Add(r.Context(), 1, metric.WithAttributes(attribute.String("path", relPath)))
		} else {
			s.MetricsOptions.PotReads.Add(r.Context(), 1, metric.WithAttributes(attribute.String("path", relPath)))
		}
	}
}

func (s *Server) routePostFunc(w http.ResponseWriter, r *http.Request) {
	var err error
	var content any

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

	content, err = s.Create(r.Context(), relPath, r.Body, callOpts...)
	if err == nil {
		w.WriteHeader(http.StatusCreated)
	}
	if err != nil {
		// norewrite violation returns
		if errors.Is(err, ErrNoRewriteViolated) {
			w.WriteHeader(http.StatusLocked)
			return
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	err = s.triggerZip(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// encode the content to the response
	if err := json.NewEncoder(w).Encode(content); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if s.MetricsOptions.Enabled {
		s.MetricsOptions.PotWrites.Add(r.Context(), 1, metric.WithAttributes(attribute.String("path", relPath)))
	}
}

func (s *Server) routeDeleteFunc(w http.ResponseWriter, r *http.Request) {
	var err error

	relPath := strings.TrimPrefix(r.URL.Path, "/")

	err = s.Remove(r.Context(), relPath, r.URL.Query()["key"]...)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	err = s.triggerZip(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if s.MetricsOptions.Enabled {
		s.MetricsOptions.PotRemoves.Add(r.Context(), 1, metric.WithAttributes(attribute.String("path", relPath)))
	}
}

func (s *Server) triggerZip(ctx context.Context) error {
	if s.zip != "" {
		return s.Zip(ctx, s.zip)
	}

	return nil
}
