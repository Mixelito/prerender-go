package main

import (
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"context"

	log "github.com/Sirupsen/logrus"
	"github.com/Mixelito/prerender/cache"
	"github.com/Mixelito/prerender/render"
	"github.com/felixge/httpsnoop"
)

func main() {
	var renderer render.Renderer
	var res *render.Result
	var err error
	renderer, err = render.NewRenderer()
	if err != nil {
		log.Fatal(err)
	}
	defer renderer.Close()
	if os.Getenv("RENDER_TIMEOUT") != "" {
		if t, perr := time.ParseDuration(os.Getenv("RENDER_TIMEOUT")); perr == nil {
			renderer.SetPageLoadTimeout(t)
		}
	}

	// a custom handler is necessary because ServeMux redirects // to /
	// in all urls, regardless of escaping
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := setRenderer(r.Context(), renderer)
		ctx = setCache(ctx, cache.NewCache())
		res = handle(w, r.WithContext(ctx))
	})
	wrappedHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m := httpsnoop.CaptureMetrics(handler, w, r)
		log.WithFields(log.Fields{
			"method":   r.Method,
			"path":     r.URL.Path,
			"cached":	res.Cached,
			"status":   m.Code,
			"duration": m.Duration.Nanoseconds(),
			"durationH": m.Duration.String(),
			"size":     m.Written,
		}).Infof("Completed request")
	})

	port := os.Getenv("PORT")
	if port == "" {
		port = "8000"
	}
	l := fmt.Sprintf(":%s", port)
	log.Printf("listening on %s", l)
	server := http.Server{Addr: l, Handler: wrappedHandler}

	go func() {
		c := make(chan os.Signal, 1)
		signal.Notify(c, os.Interrupt, syscall.SIGTERM)
		<-c
		log.Info("signal caught, shutting down")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		server.Shutdown(ctx)
	}()

	err = server.ListenAndServe()
	if err != http.ErrServerClosed {
		log.Error(err)
	}
}
