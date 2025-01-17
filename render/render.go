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
	"github.com/orcaman/concurrent-map"
	"sync"
)

// ErrPageLoadTimeout is returned when the page did not fire the "load" event
// before the timeout expired
var ErrPageLoadTimeout = errors.New("timed out waiting for page load")

const WAIT_AFTER_LAST_REQUEST = 400 * time.Millisecond
const PAGE_DONE_CHECK_INTERVAL = 200 * time.Millisecond
const PAGE_LOAD_TIMEOUT = 20 * time.Second

// Renderer is the interface implemented by renderers capable of
// fetching a webpage and returning the HTML after JavaScript has run
type Renderer interface {
	Render(*http.Request) (*Result, error)
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
	Cached	 bool
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

	var timeout time.Duration
	if os.Getenv("PAGE_LOAD_TIMEOUT") != "" {
		timeout, _ = time.ParseDuration(os.Getenv("PAGE_LOAD_TIMEOUT")+"ms")
	}else {
		timeout = PAGE_LOAD_TIMEOUT
	}

	return &chromeRenderer{
		debugger: debugger,
		timeout:  timeout,
	}, nil
}

func (r *chromeRenderer) SetPageLoadTimeout(t time.Duration) {
	r.timeout = t
}

func (r *chromeRenderer) Close() {
	r.debugger.ExitProcess()
}

func (r *chromeRenderer) Render(req *http.Request) (*Result, error) {
	start := time.Now()
	navigated := make(chan bool)
	url := req.URL.String()
	res := Result{URL: url}
	var err error

	var requests = cmap.New()
	var requestsSuccess = cmap.New()
	var lastRequestReceivedAt = time.Now()

	var wg sync.WaitGroup
	tab := startTarget(r.debugger)
	wg.Add(1)

	if err != nil {
		return nil, errors.Wrap(err, "creating new tab failed")
	}
	defer r.debugger.CloseTab(tab)

	network := tab.Network
	page := tab.Page

	/*
	//TODO setVisibleSize
	Emulation.setVisibleSize({
				width: 1440,
				height: 718
			}, (err, res) => {
				if (err) {
					console.log('Unable to setVisibleSize of page on this version of Chrome. Make sure you are up to date.')
				}
			});
	 */

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
		"data:image*",
		"*.ttf","*.eot","*.woff","*.woff2","*.jpg", "*.png", "*.gif",
	}
	if _, err = tab.Network.SetBlockedURLs(blockedUrls); err != nil {
		return nil, errors.Wrap(err, "blocked urls failed: "+url)
	}

	//when a request enters the execution queue here
	tab.Subscribe("Network.requestWillBeSent", func(target *gcd.ChromeTarget, v []byte) {
		event := &gcdapi.NetworkRequestWillBeSentEvent{}
		if err = json.Unmarshal(v, event); err != nil {
			err = errors.Wrap(err, "getting network response failed")
			return
		}

		if event.Params.RequestId != "" && event.Params.RequestId != event.Params.LoaderId {
			requests.Set(event.Params.RequestId, event.Params.Request.Url)
		}
	})

	//when each request ends with a response, enter this event
	tab.Subscribe("Network.responseReceived", func(target *gcd.ChromeTarget, v []byte) {
		event := &gcdapi.NetworkResponseReceivedEvent{}
		if err = json.Unmarshal(v, event); err != nil {
			//err = errors.Wrap(err, "getting network response failed")
			//return
			log.Printf("getting network response failed: %s", err)
		}

		lastRequestReceivedAt = time.Now()
		if event.Params.RequestId != event.Params.LoaderId {
			requestsSuccess.Set(event.Params.RequestId, event.Params.Response.Url)
		}else{
			r := event.Params.Response
			res.Status = int(r.Status)
			if etag, ok := r.Headers["Etag"]; ok {
				res.Etag = etag.(string)
			}
		}
	})

	//when a request fails, usually through url blocking it enters this event
	//when a redirect happens and we call Page.stopLoading,
	//all outstanding requests will fire this event
	tab.Subscribe("Network.loadingFailed", func(target *gcd.ChromeTarget, v []byte) {
		event := &gcdapi.NetworkLoadingFailedEvent{}
		if err = json.Unmarshal(v, event); err != nil {
			err = errors.Wrap(err, "getting network response failed")
			return
		}

		requestsSuccess.Set(event.Params.RequestId, "empty")
	})

	//when the main page and its directly connected elements are loaded
	tab.Subscribe("Page.loadEventFired", func(target *gcd.ChromeTarget, v []byte) {
		wg.Done()
	})
	/*
	tab.Subscribe("Page.domContentEventFired", func(target *gcd.ChromeTarget, v []byte) {
		wg.Done()
	})
	*/

	//set header x-prerender
	if _, err = network.SetExtraHTTPHeaders(map[string]interface{}{"X-Prerender": "1"}); err != nil {
		log.Printf("error extra http header: %s", err)
	}

	if _, err = network.SetUserAgentOverride(req.UserAgent()); err != nil {
		log.Printf("error change user agent: %s", err)
	}

	if _, err = page.Navigate(url, "", ""); err != nil {
		return nil, errors.Wrap(err, "navigating to url failed: "+url)
	}

	stopLoading := time.AfterFunc(r.timeout, func(){
		if _, err = tab.Page.StopLoading(); err != nil {
			log.Printf("error stop loading: %s : %s", err, url)
		}else{
			wg.Done()
			res.Status = http.StatusGatewayTimeout
		}
	})

	wg.Wait()
	tab.Unsubscribe("Page.loadEventFired")

	if res.Status==http.StatusGatewayTimeout {
		stopLoading.Stop()
		res.Duration = time.Since(start)
		return &res, nil
	}

	wg.Add(1)

	_ = navigated
	ticker := time.NewTicker(PAGE_DONE_CHECK_INTERVAL)
	quit := make(chan struct{})
	go func() {
		for {
			select {
			case <- ticker.C:
				//printRequestsInFlight(requests, requestsSuccess)
				if requests.Count()<=requestsSuccess.Count() && lastRequestReceivedAt.Add(WAIT_AFTER_LAST_REQUEST).Before(time.Now()) {
					ticker.Stop()
					stopLoading.Stop()
					wg.Done()
				}
			case <- quit:
				ticker.Stop()
				return
			}
		}
	}()

	wg.Wait()

	// events may generate errors
	if err != nil {
		return nil, err
	}

	// page load event but no network response, assume bad DNS
	if res.Status == 0 {
		res.Status = http.StatusNotFound
	}

	if res.Status == http.StatusNotModified {
		res.Status = http.StatusOK
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

func startTarget(debugger *gcd.Gcd) *gcd.ChromeTarget {
	target, err := debugger.NewTab()
	if err != nil {
		log.Fatalf("error getting new tab: %s\n", err)
	}
	//target.Debug(true)
	//target.DebugEvents(true)
	target.DOM.Enable()
	target.Page.Enable()
	//target.Network.Enable(-1, -1)
	//target.Debugger.Enable()

	networkParams := &gcdapi.NetworkEnableParams{
		//MaxTotalBufferSize:    -1,
		//MaxResourceBufferSize: -1,
	}
	if _, err := target.Network.EnableWithParams(networkParams); err != nil {
		log.Fatal("error enabling network")
	}

	return target
}

func printRequestsInFlight(requests cmap.ConcurrentMap, success cmap.ConcurrentMap) {
	log.Printf("numRequestsInFlight %d:%d\n", requests.Count(), success.Count())

	requests.Items()
	for index, element := range requests.Items() {
		tmp, ok := success.Get(index)
		_ = tmp
		if ok == false {
			log.Printf("InFlight: %s - %s", index, element)
		}
	}
}