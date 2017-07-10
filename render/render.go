package render

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/pkg/errors"
	"github.com/wirepair/gcd"
	"github.com/wirepair/gcd/gcdapi"
)

// ErrPageLoadTimeout is returned when the page did not fire the "load" event
// before the timeout expired
var ErrPageLoadTimeout = errors.New("timed out waiting for page load")

// Renderer is the interface implemented by renderers capable of
// fetching a webpage and returning the HTML after JavaScript has run
type Renderer interface {
	Render(string) (*Result, error)
	SetPageLoadTimeout(time.Duration)
	Close()
}

// Result describes the result of the rendering operation
type Result struct {
	URL      string
	HTML     string
	Status   int
	Etag     string
	Duration time.Duration
}

type chromeRenderer struct {
	debugger *gcd.Gcd
	timeout  time.Duration
}

// NewRenderer launches a headless Google Chrome instance
// ready to render pages
func NewRenderer() (Renderer, error) {
	chromePath := "/Applications/Google Chrome Canary.app/Contents/MacOS/Google Chrome Canary"
	if os.Getenv("CHROME_PATH") != "" {
		chromePath = os.Getenv("CHROME_PATH")
	}

	debugger := gcd.NewChromeDebugger()
	debugger.SetTerminationHandler(func(reason string) {
		log.Printf("chrome termination: %s\n", reason)
	})
	debugger.AddFlags([]string{"--headless", "--disable-gpu"})
	debugger.StartProcess(chromePath, os.TempDir(), "9222")

	return &chromeRenderer{
		debugger: debugger,
		timeout:  60 * time.Second,
	}, nil
}

func (r *chromeRenderer) SetPageLoadTimeout(t time.Duration) {
	r.timeout = t
}

func (r *chromeRenderer) Close() {
	r.debugger.ExitProcess()
}

func (r *chromeRenderer) Render(url string) (*Result, error) {
	start := time.Now()
	navigated := make(chan bool)
	res := Result{URL: url}
	var err error

	tab, err := r.debugger.NewTab()

	if err != nil {
		return nil, errors.Wrap(err, "creating new tab failed")
	}
	defer r.debugger.CloseTab(tab)
	// tab.Debug(true)

	tab.Subscribe("Page.loadEventFired", func(target *gcd.ChromeTarget, v []byte) {
		navigated <- true
	})

	tab.Subscribe("Network.responseReceived", func(target *gcd.ChromeTarget, v []byte) {
		event := &gcdapi.NetworkResponseReceivedEvent{}
		if err = json.Unmarshal(v, event); err != nil {
			err = errors.Wrap(err, "getting network response failed")
			//return
		}
		//log.Printf("finish url: %f - %s\n", event.Params.Response.Status, event.Params.Response.Url)

		r := event.Params.Response
		res.Status = int(r.Status)
		if etag, ok := r.Headers["Etag"]; ok {
			res.Etag = etag.(string)
		}
	})

	if _, err = tab.Page.Enable(); err != nil {
		return nil, errors.Wrap(err, "enabling tab page failed")
	}
	if _, err = tab.Network.Enable(-1, -1); err != nil {
		return nil, errors.Wrap(err, "enabling tab network failed")
	}
	if _, err = tab.Page.Navigate(url, "", ""); err != nil {
		return nil, errors.Wrap(err, "navigating to url failed: "+url)
	}

	var blockedUrls []string = []string{
		"google-analytics.com",
		"api.mixpanel.com",
		"fonts.googleapis.com",
		"stats.g.doubleclick.net",
		"mc.yandex.ru",
		"use.typekit.net",
		"beacon.tapfiliate.com",
		"js-agent.newrelic.com",
		"api.segment.io",
		"woopra.com",
		"static.olark.com",
		"static.getclicky.com",
		"fast.fonts.com",
		"youtube.com/embed",
		"cdn.heapanalytics.com",
		"googleads.g.doubleclick.net",
		"pagead2.googlesyndication.com",
		"fullstory.com/rec",
		"navilytics.com/nls_ajax.php",
		"log.optimizely.com/event",
		"hn.inspectlet.com",
		"tpc.googlesyndication.com",
		"partner.googleadservices.com",
		"static.hotjar.com",
		"www.google.com/recaptcha",
		"securepubads.g.doubleclick.net",
		"www.gstatic.com/recaptcha",
		"d31qbv1cthcecs.cloudfront.net",
		"sb.scorecardresearch.com",
		"www.googletagservices.com",
		"px.mooba.com.br",
		"data:image",
		"*.ttf","*.eot","*.woff","*.woff2","*.jpg", "*.png", "*.gif",
	}
	if _, err = tab.Network.SetBlockedURLs(blockedUrls); err != nil {
		return nil, errors.Wrap(err, "blocked urls failed: "+url)
	}

	networkParams := &gcdapi.NetworkEnableParams{
		MaxTotalBufferSize:    -1,
		MaxResourceBufferSize: -1,
	}
	if _, err := tab.Network.EnableWithParams(networkParams); err != nil {
		log.Fatal("error enabling network")
	}
	
	select {
	case <-time.After(r.timeout):
		return nil, ErrPageLoadTimeout
	case <-navigated:
	}

	// events may generate errors
	if err != nil {
		return nil, err
	}

	// page load event but no network response, assume bad DNS
	if res.Status == 0 {
		res.Status = http.StatusNotFound
	}

	if res.Status == http.StatusOK {
		doc, err := tab.DOM.GetDocument(1, false)
		if err != nil {
			return nil, errors.Wrap(err, "getting tab document failed")
		}
		html, err := tab.DOM.GetOuterHTML(doc.NodeId)
		if err != nil {
			return nil, errors.Wrap(err, "get outer html for document failed")
		}
		res.HTML = html

		if res.Etag == "" {
			hash := md5.Sum([]byte(res.HTML))
			res.Etag = hex.EncodeToString(hash[:])
		}
	}

	res.Duration = time.Since(start)
	return &res, nil
}
