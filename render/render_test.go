package render

import (
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var r Renderer

func TestMain(m *testing.M) {
	var err error
	r, err = NewRenderer()
	if err != nil {
		log.Fatal(err)
	}
	code := m.Run()
	r.Close()
	os.Exit(code)
}

func TestNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	res, err := r.Render(server.URL)
	require.NoError(t, err)
	assert.Equal(t, res.Status, http.StatusNotFound)
	assert.Empty(t, res.HTML)
}

func TestOriginEtag(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("ETag", "randometag")
		w.Header().Add("Content-Type", "text/html")
		fmt.Fprint(w, "<body>data</body>")
	}))
	defer server.Close()

	res, err := r.Render(server.URL)
	require.NoError(t, err)
	assert.Equal(t, res.Status, http.StatusOK)
	// Chrome adds html tags
	assert.Equal(t, res.HTML, "<html><head></head><body>data</body></html>")
	assert.Equal(t, res.Etag, "randometag")
}

func TestEtagGenerate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Content-Type", "text/html")
		fmt.Fprint(w, "<body>data</body>")
	}))
	defer server.Close()

	res, err := r.Render(server.URL)
	require.NoError(t, err)
	assert.Equal(t, res.Status, http.StatusOK)
	// Chrome adds html tags
	assert.Equal(t, res.HTML, "<html><head></head><body>data</body></html>")
	assert.Equal(t, res.Etag, "2d52742649958b6126ae9a9789c61c7e")
}

func TestTimeout(t *testing.T) {
	r.SetPageLoadTimeout(10 * time.Millisecond)
	defer r.SetPageLoadTimeout(60 * time.Second)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	_, err := r.Render(server.URL)
	assert.Equal(t, err, ErrPageLoadTimeout)
}
