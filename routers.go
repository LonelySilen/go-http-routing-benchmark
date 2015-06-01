// Copyright 2014 Julien Schmidt. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be found
// in the LICENSE file.

package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"

	"github.com/ant0ine/go-json-rest/rest"
	"github.com/astaxie/beego"
	"github.com/bmizerany/pat"
	"github.com/dimfeld/httptreemux"
	"github.com/go-martini/martini"
	"github.com/gocraft/web"
	"github.com/gorilla/mux"
	"github.com/julienschmidt/httprouter"
	"github.com/naoina/denco"
	"github.com/naoina/kocha-urlrouter"
	_ "github.com/naoina/kocha-urlrouter/doublearray"
	"github.com/pilu/traffic"
	"github.com/rcrowley/go-tigertonic"
	goji "github.com/zenazn/goji/web"
)

type route struct {
	method string
	path   string
}

type mockResponseWriter struct{}

func (m *mockResponseWriter) Header() (h http.Header) {
	return http.Header{}
}

func (m *mockResponseWriter) Write(p []byte) (n int, err error) {
	return len(p), nil
}

func (m *mockResponseWriter) WriteString(s string) (n int, err error) {
	return len(s), nil
}

func (m *mockResponseWriter) WriteHeader(int) {}

var nullLogger *log.Logger

func init() {

	// makes logging 'webscale' (ignores them)
	log.SetOutput(new(mockResponseWriter))
	nullLogger = log.New(new(mockResponseWriter), "", 0)

	martini.Env = martini.Prod
	traffic.SetVar("env", "bench")
	beego.RunMode = "prod"
}

// Common
func httpHandlerFunc(w http.ResponseWriter, r *http.Request) {}

type beegoRouter struct {
	routers map[string]*beego.Tree
}

func (br *beegoRouter) Handle(method, pattern string, handler http.HandlerFunc) {
	if t, ok := br.routers[method]; ok {
		t.AddRouter(pattern, handler)
	} else {
		t := beego.NewTree()
		t.AddRouter(pattern, handler)
		br.routers[method] = t
	}
}

func (br *beegoRouter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if t, ok := br.routers[r.Method]; ok {
		runObject, params := t.Match(r.URL.Path)
		if f, ok := runObject.(http.HandlerFunc); ok {
			r.ParseForm()
			for k, v := range params {
				r.Form.Add(k, v)
			}
			f(w, r)
		} else {
			panic("wrong http func")
		}
	}
}

// beego router
func beegoHandler(w http.ResponseWriter, r *http.Request) {}

func beegoHandlerWrite(w http.ResponseWriter, r *http.Request) {
	io.WriteString(w, r.FormValue(":name"))
}

func loadBeego(routes []route) http.Handler {
	router := &beegoRouter{
		routers: make(map[string]*beego.Tree),
	}
	for _, route := range routes {
		router.Handle(route.method, route.path, beegoHandler)
	}
	return router
}

func loadBeegoSingle(method, path string, handle http.HandlerFunc) http.Handler {
	router := &beegoRouter{
		routers: make(map[string]*beego.Tree),
	}
	router.Handle(method, path, handle)
	return router
}

// Denco
type dencoHandler struct {
	routerMap map[string]*denco.Router
	params    []denco.Param
}

func (h *dencoHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	router, found := h.routerMap[r.Method]
	if !found {
		panic("Unknown HTTP method: " + r.Method)
	}
	meth, params, found := router.Lookup(r.URL.Path)
	if !found {
		panic("Router not found: " + r.URL.Path)
	}
	h.params = params
	meth.(http.HandlerFunc).ServeHTTP(w, r)
}

func (h *dencoHandler) Get(w http.ResponseWriter, r *http.Request)    {}
func (h *dencoHandler) Post(w http.ResponseWriter, r *http.Request)   {}
func (h *dencoHandler) Put(w http.ResponseWriter, r *http.Request)    {}
func (h *dencoHandler) Patch(w http.ResponseWriter, r *http.Request)  {}
func (h *dencoHandler) Delete(w http.ResponseWriter, r *http.Request) {}
func (h *dencoHandler) dencoHandlerWrite(w http.ResponseWriter, r *http.Request) {
	var name string
	for _, param := range h.params {
		if param.Name == "name" {
			name = param.Value
			break
		}
	}
	io.WriteString(w, name)
}

func loadDenco(routes []route) http.Handler {
	handler := &dencoHandler{routerMap: map[string]*denco.Router{
		"GET":    denco.New(),
		"POST":   denco.New(),
		"PUT":    denco.New(),
		"PATCH":  denco.New(),
		"DELETE": denco.New(),
	}}
	recordMap := make(map[string][]denco.Record)
	for _, route := range routes {
		var f http.HandlerFunc
		switch route.method {
		case "GET":
			f = handler.Get
		case "POST":
			f = handler.Post
		case "PUT":
			f = handler.Put
		case "PATCH":
			f = handler.Patch
		case "DELETE":
			f = handler.Delete
		}
		recordMap[route.method] = append(recordMap[route.method], denco.NewRecord(route.path, f))
	}
	for method, records := range recordMap {
		if err := handler.routerMap[method].Build(records); err != nil {
			panic(err)
		}
	}
	return handler
}

func loadDencoSingle(method, path string, handler *dencoHandler, hfunc http.HandlerFunc) http.Handler {
	handler.routerMap = map[string]*denco.Router{
		method: denco.New(),
	}

	if err := handler.routerMap[method].Build([]denco.Record{
		denco.NewRecord(path, hfunc),
	}); err != nil {
		panic(err)
	}
	return handler
}

// gocraft/web
type gocraftWebContext struct{}

func gocraftWebHandler(w web.ResponseWriter, r *web.Request) {}

func gocraftWebHandlerWrite(w web.ResponseWriter, r *web.Request) {
	io.WriteString(w, r.PathParams["name"])
}

func loadGocraftWeb(routes []route) http.Handler {
	router := web.New(gocraftWebContext{})
	for _, route := range routes {
		switch route.method {
		case "GET":
			router.Get(route.path, gocraftWebHandler)
		case "POST":
			router.Post(route.path, gocraftWebHandler)
		case "PUT":
			router.Put(route.path, gocraftWebHandler)
		case "PATCH":
			router.Patch(route.path, gocraftWebHandler)
		case "DELETE":
			router.Delete(route.path, gocraftWebHandler)
		default:
			panic("Unknow HTTP method: " + route.method)
		}
	}
	return router
}

func loadGocraftWebSingle(method, path string, handler interface{}) http.Handler {
	router := web.New(gocraftWebContext{})
	switch method {
	case "GET":
		router.Get(path, handler)
	case "POST":
		router.Post(path, handler)
	case "PUT":
		router.Put(path, handler)
	case "PATCH":
		router.Patch(path, handler)
	case "DELETE":
		router.Delete(path, handler)
	default:
		panic("Unknow HTTP method: " + method)
	}
	return router
}

// goji
func gojiFuncWrite(c goji.C, w http.ResponseWriter, r *http.Request) {
	io.WriteString(w, c.URLParams["name"])
}

func loadGoji(routes []route) http.Handler {
	mux := goji.New()
	for _, route := range routes {
		switch route.method {
		case "GET":
			mux.Get(route.path, httpHandlerFunc)
		case "POST":
			mux.Post(route.path, httpHandlerFunc)
		case "PUT":
			mux.Put(route.path, httpHandlerFunc)
		case "PATCH":
			mux.Patch(route.path, httpHandlerFunc)
		case "DELETE":
			mux.Delete(route.path, httpHandlerFunc)
		default:
			panic("Unknown HTTP method: " + route.method)
		}
	}
	return mux
}

func loadGojiSingle(method, path string, handler interface{}) http.Handler {
	mux := goji.New()
	switch method {
	case "GET":
		mux.Get(path, handler)
	case "POST":
		mux.Post(path, handler)
	case "PUT":
		mux.Put(path, handler)
	case "PATCH":
		mux.Patch(path, handler)
	case "DELETE":
		mux.Delete(path, handler)
	default:
		panic("Unknow HTTP method: " + method)
	}
	return mux
}

// go-json-rest/rest
func goJsonRestHandler(w rest.ResponseWriter, req *rest.Request) {}

func goJsonRestHandlerWrite(w rest.ResponseWriter, req *rest.Request) {
	io.WriteString(w.(io.Writer), req.PathParam("name"))
}

func newGoJsonRestResourceHandler() *rest.ResourceHandler {
	handler := rest.ResourceHandler{
		EnableRelaxedContentType: true,
		Logger:            nullLogger,
		ErrorLogger:       nullLogger,
		DisableXPoweredBy: true,
	}
	return &handler
}

func loadGoJsonRest(routes []route) http.Handler {
	handler := newGoJsonRestResourceHandler()
	restRoutes := make([]*rest.Route, 0, len(routes))
	for _, route := range routes {
		restRoutes = append(restRoutes,
			&rest.Route{route.method, route.path, goJsonRestHandler},
		)
	}
	handler.SetRoutes(restRoutes...)
	return handler
}

func loadGoJsonRestSingle(method, path string, hfunc rest.HandlerFunc) http.Handler {
	handler := newGoJsonRestResourceHandler()
	handler.SetRoutes(
		&rest.Route{method, path, hfunc},
	)
	return handler
}

// gorilla/mux
func gorillaHandlerWrite(w http.ResponseWriter, r *http.Request) {
	params := mux.Vars(r)
	io.WriteString(w, params["name"])
}

func loadGorillaMux(routes []route) http.Handler {
	re := regexp.MustCompile(":([^/]*)")
	m := mux.NewRouter()
	for _, route := range routes {
		m.HandleFunc(
			re.ReplaceAllString(route.path, "{$1}"),
			httpHandlerFunc,
		).Methods(route.method)
	}
	return m
}

func loadGorillaMuxSingle(method, path string, handler http.HandlerFunc) http.Handler {
	m := mux.NewRouter()
	m.HandleFunc(path, handler).Methods(method)
	return m
}

// HttpRouter
func httpRouterHandle(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {}

func httpRouterHandleWrite(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	io.WriteString(w, ps.ByName("name"))
}

func loadHttpRouter(routes []route) http.Handler {
	router := httprouter.New()
	for _, route := range routes {
		router.Handle(route.method, route.path, httpRouterHandle)
	}
	return router
}

func loadHttpRouterSingle(method, path string, handle httprouter.Handle) http.Handler {
	router := httprouter.New()
	router.Handle(method, path, handle)
	return router
}

// httpTreeMux
func httpTreeMuxHandler(w http.ResponseWriter, r *http.Request, vars map[string]string) {}

func httpTreeMuxHandlerWrite(w http.ResponseWriter, r *http.Request, vars map[string]string) {
	io.WriteString(w, vars["name"])
}

func loadHttpTreeMux(routes []route) http.Handler {
	router := httptreemux.New()
	for _, route := range routes {
		router.Handle(route.method, route.path, httpTreeMuxHandler)
	}
	return router
}

func loadHttpTreeMuxSingle(method, path string, handler httptreemux.HandlerFunc) http.Handler {
	router := httptreemux.New()
	router.Handle(method, path, handler)
	return router
}

// Kocha-urlrouter
type kochaHandler struct {
	routerMap map[string]urlrouter.URLRouter
	params    []urlrouter.Param
}

func (h *kochaHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	meth, params := h.routerMap[r.Method].Lookup(r.URL.Path)
	h.params = params
	meth.(http.HandlerFunc).ServeHTTP(w, r)
}

func (h *kochaHandler) Get(w http.ResponseWriter, r *http.Request)    {}
func (h *kochaHandler) Post(w http.ResponseWriter, r *http.Request)   {}
func (h *kochaHandler) Put(w http.ResponseWriter, r *http.Request)    {}
func (h *kochaHandler) Patch(w http.ResponseWriter, r *http.Request)  {}
func (h *kochaHandler) Delete(w http.ResponseWriter, r *http.Request) {}
func (h *kochaHandler) kochaHandlerWrite(w http.ResponseWriter, r *http.Request) {
	var name string
	for _, param := range h.params {
		if param.Name == "name" {
			name = param.Value
			break
		}
	}
	io.WriteString(w, name)
}

func loadKocha(routes []route) http.Handler {
	handler := &kochaHandler{routerMap: map[string]urlrouter.URLRouter{
		"GET":    urlrouter.NewURLRouter("doublearray"),
		"POST":   urlrouter.NewURLRouter("doublearray"),
		"PUT":    urlrouter.NewURLRouter("doublearray"),
		"PATCH":  urlrouter.NewURLRouter("doublearray"),
		"DELETE": urlrouter.NewURLRouter("doublearray"),
	}}
	recordMap := make(map[string][]urlrouter.Record)
	for _, route := range routes {
		var f http.HandlerFunc
		switch route.method {
		case "GET":
			f = handler.Get
		case "POST":
			f = handler.Post
		case "PUT":
			f = handler.Put
		case "PATCH":
			f = handler.Patch
		case "DELETE":
			f = handler.Delete
		}
		recordMap[route.method] = append(
			recordMap[route.method],
			urlrouter.NewRecord(route.path, f),
		)
	}
	for method, records := range recordMap {
		if err := handler.routerMap[method].Build(records); err != nil {
			panic(err)
		}
	}
	return handler
}

func loadKochaSingle(method, path string, handler *kochaHandler, hfunc http.HandlerFunc) http.Handler {
	handler.routerMap = map[string]urlrouter.URLRouter{
		method: urlrouter.NewURLRouter("doublearray"),
	}

	if err := handler.routerMap[method].Build([]urlrouter.Record{
		urlrouter.NewRecord(path, hfunc),
	}); err != nil {
		panic(err)
	}
	return handler
}

// Martini
func martiniHandler() {}

func martiniHandlerWrite(params martini.Params) string {
	return params["name"]
}

func loadMartini(routes []route) http.Handler {
	router := martini.NewRouter()
	for _, route := range routes {
		switch route.method {
		case "GET":
			router.Get(route.path, martiniHandler)
		case "POST":
			router.Post(route.path, martiniHandler)
		case "PUT":
			router.Put(route.path, martiniHandler)
		case "PATCH":
			router.Patch(route.path, martiniHandler)
		case "DELETE":
			router.Delete(route.path, martiniHandler)
		default:
			panic("Unknow HTTP method: " + route.method)
		}
	}
	martini := martini.New()
	martini.Action(router.Handle)
	return martini
}

func loadMartiniSingle(method, path string, handler interface{}) http.Handler {
	router := martini.NewRouter()
	switch method {
	case "GET":
		router.Get(path, handler)
	case "POST":
		router.Post(path, handler)
	case "PUT":
		router.Put(path, handler)
	case "PATCH":
		router.Patch(path, handler)
	case "DELETE":
		router.Delete(path, handler)
	default:
		panic("Unknow HTTP method: " + method)
	}

	martini := martini.New()
	martini.Action(router.Handle)
	return martini
}

// pat
func patHandlerWrite(w http.ResponseWriter, r *http.Request) {
	io.WriteString(w, r.URL.Query().Get(":name"))
}

func loadPat(routes []route) http.Handler {
	m := pat.New()
	for _, route := range routes {
		switch route.method {
		case "GET":
			m.Get(route.path, http.HandlerFunc(httpHandlerFunc))
		case "POST":
			m.Post(route.path, http.HandlerFunc(httpHandlerFunc))
		case "PUT":
			m.Put(route.path, http.HandlerFunc(httpHandlerFunc))
		case "DELETE":
			m.Del(route.path, http.HandlerFunc(httpHandlerFunc))
		default:
			panic("Unknow HTTP method: " + route.method)
		}
	}
	return m
}

func loadPatSingle(method, path string, handler http.Handler) http.Handler {
	m := pat.New()
	switch method {
	case "GET":
		m.Get(path, handler)
	case "POST":
		m.Post(path, handler)
	case "PUT":
		m.Put(path, handler)
	case "DELETE":
		m.Del(path, handler)
	default:
		panic("Unknow HTTP method: " + method)
	}
	return m
}

// Tiger Tonic
func tigerTonicHandlerWrite(w http.ResponseWriter, r *http.Request) {
	io.WriteString(w, r.URL.Query().Get("name"))
}

func loadTigerTonic(routes []route) http.Handler {
	re := regexp.MustCompile(":([^/]*)")
	mux := tigertonic.NewTrieServeMux()
	for _, route := range routes {
		mux.HandleFunc(route.method, re.ReplaceAllString(route.path, "{$1}"), httpHandlerFunc)
	}
	return mux
}

func loadTigerTonicSingle(method, path string, handler http.HandlerFunc) http.Handler {
	mux := tigertonic.NewTrieServeMux()
	mux.HandleFunc(method, path, handler)
	return mux
}

// Traffic
func trafficHandlerWrite(w traffic.ResponseWriter, r *traffic.Request) {
	io.WriteString(w, r.URL.Query().Get("name"))
}
func trafficHandler(w traffic.ResponseWriter, r *traffic.Request) {}

func loadTraffic(routes []route) http.Handler {
	router := traffic.New()
	for _, route := range routes {
		switch route.method {
		case "GET":
			router.Get(route.path, trafficHandler)
		case "POST":
			router.Post(route.path, trafficHandler)
		case "PUT":
			router.Put(route.path, trafficHandler)
		case "PATCH":
			router.Patch(route.path, trafficHandler)
		case "DELETE":
			router.Delete(route.path, trafficHandler)
		default:
			panic("Unknow HTTP method: " + route.method)
		}
	}
	return router
}

func loadTrafficSingle(method, path string, handler traffic.HttpHandleFunc) http.Handler {
	router := traffic.New()
	switch method {
	case "GET":
		router.Get(path, handler)
	case "POST":
		router.Post(path, handler)
	case "PUT":
		router.Put(path, handler)
	case "PATCH":
		router.Patch(path, handler)
	case "DELETE":
		router.Delete(path, handler)
	default:
		panic("Unknow HTTP method: " + method)
	}
	return router
}

// Usage notice
func main() {
	fmt.Println("Usage: go test -bench=.")
	os.Exit(1)
}
