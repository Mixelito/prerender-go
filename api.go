package main

import (
	"fmt"
	"net/http"
	"net/url"
	"time"
	"strings"

	log "github.com/Sirupsen/logrus"
	"./render"
	_ "github.com/Mixelito/prerender/cache"
)

func handle(w http.ResponseWriter, r *http.Request) {
	reqURL := r.URL.String()[1:]

	//if decoded url has two query params from a decoded escaped fragment for hashbang URLs
	if strings.Index("?", reqURL) != strings.LastIndex("?", reqURL) {
		reqURL = reqURL[0:strings.LastIndex("?", reqURL)] + "&" + reqURL[strings.LastIndex("?", reqURL)+1:]
	}

	if reqURL == "" {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, "url is required")
		return
	}

	u, err := url.Parse(reqURL)
	if err != nil || !u.IsAbs() {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, "Invalid URL")
		return
	}

	//http://www.example.com?_escaped_fragment_=key1=value1%26key2=value2
	//to http://www.example.com#!key1=value1&key2=value2
	// Remove the _escaped_fragment_ query parameter
	urlQuery := u.Query()
	if urlQuery != nil && urlQuery["_escaped_fragment_"] != nil {

		if urlQuery.Get("_escaped_fragment_") != "" {
			u.Path = "#!" + urlQuery.Get("_escaped_fragment_")
		}

		urlQuery.Del("_escaped_fragment_")
		u.RawQuery = urlQuery.Encode()
	}

	r.URL = u

	res, err := getData(r)
	writeResult(res, err, w)
}

func getData(r *http.Request) (*render.Result, error) {
	cache := getCache(r.Context())
	if cache != nil && r.Method != "POST" {
		res, err := cache.Check(r)
		if err != nil || res != nil {
			return res, err
		}
	}

	renderer := getRenderer(r.Context())
	res, err := renderer.Render(r.URL.String())
	if err == nil && res.Status == http.StatusOK && cache != nil {
		err = cache.Save(res, 24*time.Hour)
	}
	return res, err
}

func writeResult(res *render.Result, err error, w http.ResponseWriter) {
	if err != nil {
		if err == render.ErrPageLoadTimeout {
			w.WriteHeader(http.StatusGatewayTimeout)
		} else {
			w.WriteHeader(http.StatusInternalServerError)
			log.WithError(err).Errorf("error rendering")
		}
		return
	}

	if res.Status != http.StatusOK {
		w.WriteHeader(res.Status)
		return
	}
	if res.Etag != "" {
		w.Header().Add("Etag", res.Etag)
	}
	if res.HTML != "" {
		fmt.Fprint(w, res.HTML)
	}
}
