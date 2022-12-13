package sentinel

import (
	"arkeo/common"
	"arkeo/sentinel/conf"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"

	"github.com/gorilla/handlers"
)

type Proxy struct {
	Metadata   Metadata
	Config     conf.Configuration
	MemStore   *MemStore
	ClaimStore *ClaimStore
}

func NewProxy(config conf.Configuration) Proxy {
	claimStore, err := NewClaimStore(config.ClaimStoreLocation)
	if err != nil {
		panic(err)
	}
	return Proxy{
		Metadata:   NewMetadata(config),
		Config:     config,
		MemStore:   NewMemStore(config.SourceChain),
		ClaimStore: claimStore,
	}
}

// Serve a reverse proxy for a given url
func (p Proxy) serveReverseProxy(w http.ResponseWriter, r *http.Request, host string) {
	// parse the url
	url, _ := url.Parse(fmt.Sprintf("http://%s", host))
	fmt.Println("Proxy Redirect:", url)

	// create the reverse proxy
	proxy := NewSingleHostReverseProxy(url)

	// Note that ServeHttp is non blocking and uses a go routine under the hood
	proxy.ServeHTTP(w, r)
}

func NewSingleHostReverseProxy(target *url.URL) *httputil.ReverseProxy {
	targetQuery := target.RawQuery
	director := func(req *http.Request) {
		req.URL.Scheme = target.Scheme
		req.URL.Host = target.Host
		req.URL.Path, req.URL.RawPath = joinURLPath(target, req.URL)
		if targetQuery == "" || req.URL.RawQuery == "" {
			req.URL.RawQuery = targetQuery + req.URL.RawQuery
		} else {
			req.URL.RawQuery = targetQuery + "&" + req.URL.RawQuery
		}
		if _, ok := req.Header["User-Agent"]; !ok {
			// explicitly disable User-Agent so it's not set to default value
			req.Header.Set("User-Agent", "")
		}
		passwd, ok := req.URL.User.Password()
		fmt.Printf("Password: %s, Bool: %+v\n", passwd, ok)
		if ok {
			req.SetBasicAuth(req.URL.User.Username(), passwd)
		}
	}
	return &httputil.ReverseProxy{Director: director}
}

func joinURLPath(a, b *url.URL) (path, rawpath string) {
	if a.RawPath == "" && b.RawPath == "" {
		return singleJoiningSlash(a.Path, b.Path), ""
	}
	// Same as singleJoiningSlash, but uses EscapedPath to determine
	// whether a slash should be added
	apath := a.EscapedPath()
	bpath := b.EscapedPath()

	aslash := strings.HasSuffix(apath, "/")
	bslash := strings.HasPrefix(bpath, "/")

	switch {
	case aslash && bslash:
		return a.Path + b.Path[1:], apath + bpath[1:]
	case !aslash && !bslash:
		return a.Path + "/" + b.Path, apath + "/" + bpath
	}
	return a.Path + b.Path, apath + bpath
}

func singleJoiningSlash(a, b string) string {
	aslash := strings.HasSuffix(a, "/")
	bslash := strings.HasPrefix(b, "/")
	switch {
	case aslash && bslash:
		return a + b[1:]
	case !aslash && !bslash:
		return a + "/" + b
	}
	return a + b
}

// Given a request send it to the appropriate url
func (p Proxy) handleRequestAndRedirect(w http.ResponseWriter, r *http.Request) {
	// remove arkauth query arg
	values := r.URL.Query()
	values.Del(QueryArkAuth)
	r.URL.RawQuery = values.Encode()

	parts := strings.Split(r.URL.Path, "/")
	host := parts[1]
	parts = append(parts[:1], parts[1+1:]...)
	r.URL.Path = strings.Join(parts, "/")

	switch host { // nolint
	case "btc-mainnet-fullnode":
		// add username/password to request
		host = fmt.Sprintf("thorchain:password@%s:8332", host)
		r.URL.User = url.UserPassword("thorchain", "password")
	}

	p.serveReverseProxy(w, r, host)
}

func (p Proxy) handleMetadata(w http.ResponseWriter, r *http.Request) {
	r.Header.Set("Content-Type", "application/json")

	d, _ := json.Marshal(p.Metadata)
	_, _ = w.Write(d)
}

func (p Proxy) handleOpenClaims(w http.ResponseWriter, r *http.Request) {
	r.Header.Set("Content-Type", "application/json")

	open_claims := make([]Claim, 0)
	for _, claim := range p.ClaimStore.List() {
		fmt.Printf("Claim: %+v\n", claim)
		if claim.Claimed {
			fmt.Println("already claimed")
			continue
		}
		contract, err := p.MemStore.Get(claim.Key())
		if err != nil {
			fmt.Println("bad fetch")
			continue
		}

		if contract.IsClose(p.MemStore.GetHeight()) {
			_ = p.ClaimStore.Remove(claim.Key()) // clear expired
			fmt.Println("expired")
			continue
		}

		open_claims = append(open_claims, claim)

	}

	d, _ := json.Marshal(open_claims)
	_, _ = w.Write(d)
}

func (p Proxy) handleContract(w http.ResponseWriter, r *http.Request) {
	r.Header.Set("Content-Type", "application/json")
	path := r.URL.Path

	parts := strings.Split(path, "/")
	if len(parts) < 5 {
		http.Error(w, "not enough parameters", http.StatusBadRequest)
		return
	}

	providerPK, err := common.NewPubKey(parts[2])
	if err != nil {
		log.Print(err.Error())
		http.Error(w, fmt.Sprintf("bad provider pubkey: %s", err), http.StatusBadRequest)
		return
	}

	chain, err := common.NewChain(parts[3])
	if err != nil {
		log.Print(err.Error())
		http.Error(w, fmt.Sprintf("bad provider pubkey: %s", err), http.StatusBadRequest)
		return
	}

	spenderPK, err := common.NewPubKey(parts[4])
	if err != nil {
		log.Print(err.Error())
		http.Error(w, "Invalid spender pubkey", http.StatusBadRequest)
		return
	}

	key := p.MemStore.Key(providerPK.String(), chain.String(), spenderPK.String())
	contract, err := p.MemStore.Get(key)
	if err != nil {
		log.Print(err.Error())
		http.Error(w, fmt.Sprintf("fetch contract error: %s", err), http.StatusBadRequest)
		return
	}

	d, _ := json.Marshal(contract)
	_, _ = w.Write(d)
}

func (p Proxy) handleClaim(w http.ResponseWriter, r *http.Request) {
	r.Header.Set("Content-Type", "application/json")
	path := r.URL.Path

	parts := strings.Split(path, "/")
	if len(parts) < 5 {
		http.Error(w, "not enough parameters", http.StatusBadRequest)
		return
	}

	providerPK, err := common.NewPubKey(parts[2])
	if err != nil {
		log.Print(err.Error())
		http.Error(w, fmt.Sprintf("bad provider pubkey: %s", err), http.StatusBadRequest)
		return
	}

	chain, err := common.NewChain(parts[3])
	if err != nil {
		log.Print(err.Error())
		http.Error(w, fmt.Sprintf("bad provider pubkey: %s", err), http.StatusBadRequest)
		return
	}

	spenderPK, err := common.NewPubKey(parts[4])
	if err != nil {
		log.Print(err.Error())
		http.Error(w, "Invalid spender pubkey", http.StatusBadRequest)
		return
	}

	claim := NewClaim(providerPK, chain, spenderPK, 0, 0, "")
	claim, err = p.ClaimStore.Get(claim.Key())
	if err != nil {
		log.Print(err.Error())
		http.Error(w, fmt.Sprintf("fetch contract error: %s", err), http.StatusBadRequest)
		return
	}

	d, _ := json.Marshal(claim)
	_, _ = w.Write(d)
}

func (p Proxy) Run() {
	log.Println("Starting Sentinel (reverse proxy)....")
	p.Config.Print()

	go p.EventListener(p.Config.EventStreamHost)

	mux := http.NewServeMux()

	// start server
	mux.Handle("/metadata.json", handlers.LoggingHandler(os.Stdout, http.HandlerFunc(p.handleMetadata)))
	mux.Handle("/contract/", handlers.LoggingHandler(os.Stdout, http.HandlerFunc(p.handleContract)))
	mux.Handle("/claim/", handlers.LoggingHandler(os.Stdout, http.HandlerFunc(p.handleClaim)))
	mux.Handle("/open_claims/", handlers.LoggingHandler(os.Stdout, http.HandlerFunc(p.handleOpenClaims)))
	mux.Handle("/", p.auth(
		handlers.LoggingHandler(
			os.Stdout,
			handlers.ProxyHeaders(
				http.HandlerFunc(p.handleRequestAndRedirect),
			),
		),
	))

	log.Fatal(http.ListenAndServe(fmt.Sprintf(":%s", p.Config.Port), mux))
}
