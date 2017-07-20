package main

import (
	"fmt"
	"net/http"
	"net/url"
	"time"
	"strings"
	"regexp"
	"strconv"
	"os"

	log "github.com/Sirupsen/logrus"
	"github.com/Mixelito/prerender/render"
	_ "github.com/Mixelito/prerender/cache"
)

func handle(w http.ResponseWriter, r *http.Request) (*render.Result) {
	reqURL := r.URL.String()[1:]

	//if decoded url has two query params from a decoded escaped fragment for hashbang URLs
	if strings.Index("?", reqURL) != strings.LastIndex("?", reqURL) {
		reqURL = reqURL[0:strings.LastIndex("?", reqURL)] + "&" + reqURL[strings.LastIndex("?", reqURL)+1:]
	}

	if reqURL == "" {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, "url is required")
		return nil
	}

	reqURLFinal, err := url.QueryUnescape(reqURL)
	if err != nil {
		reqURLFinal = reqURL
	}

	u, err := url.Parse(reqURLFinal)
	if err != nil || !u.IsAbs() {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, "Invalid URL")
		return nil
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
	return res
}

func getData(r *http.Request) (*render.Result, error) {
	cache := getCache(r.Context())
	if cache != nil && r.Method != "POST" {
		res, err := cache.Check(r)
		if err != nil || res != nil {
			res.Cached = true
			return res, err
		}
	}

	renderer := getRenderer(r.Context())
	res, err := renderer.Render(r)
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
		//prerender-status-code
		if os.Getenv("PLUGIN_STATUS_CODE") != "false" {
			statusMatch, _ := regexp.Compile("<meta[^<>]*(?:name=['\"]prerender-status-code['\"][^<>]*content=['\"]([0-9]{3})['\"]|content=['\"]([0-9]{3})['\"][^<>]*name=['\"]prerender-status-code['\"])[^<>]*>")
			headerMatch, _ := regexp.Compile("<meta[^<>]*(?:name=['\"]prerender-header['\"][^<>]*content=['\"]([^'\"]*?): ?([^'\"]*?)['\"]|content=['\"]([^'\"]*?): ?([^'\"]*?)['\"][^<>]*name=['\"]prerender-header['\"])[^<>]*>")
			head := strings.Split(res.HTML, "</head>")[0]

			var match2 [][]string = headerMatch.FindAllStringSubmatch(head, -1)
			if match2 != nil {
				for index, element := range match2 {
					_ = index
					var headerName string
					var headerValue string

					if element[1] != "" {
						headerName = element[1]
					} else if element[3] != "" {
						headerName = element[3]
					}

					if element[2] != "" {
						headerValue = element[2]
					} else if element[4] != "" {
						headerValue = element[4]
					}

					w.Header().Add(headerName, headerValue)
					res.HTML = strings.Replace(res.HTML, element[0], "", -1)
				}
			}

			var match []string = statusMatch.FindStringSubmatch(head)
			if match != nil {
				var finalMatch string
				if match[1] != "" {
					finalMatch = match[1]
				} else if match[2] != "" {
					finalMatch = match[2]
				}

				statusCode, err := strconv.ParseInt(finalMatch, 10, 64)
				_ = err
				if statusCode != 0 && statusCode != 200 {
					w.WriteHeader(int(statusCode))
				}
				res.HTML = strings.Replace(res.HTML, match[0], "", -1)
			}
		}

		//removeScriptTags
		if os.Getenv("PLUGIN_SCRIPT_TAGS") != "false" {
			scriptMatch := regexp.MustCompile(`(?i)<script(?:.*?)>(?:[\S\s]*?)<\/script>`)
			var match3 [][]string = scriptMatch.FindAllStringSubmatch(res.HTML, -1)
			if match3 != nil {
				for index, element := range match3 {
					_ = index
					for index2, element2 := range element {
						_ = element2
						if strings.Index(element[index2], "application/ld+json") == -1 {
							res.HTML = strings.Replace(res.HTML, element[index2], "", -1)
						}
					}
				}
			}
		}

		fmt.Fprint(w, res.HTML)

	}
}
