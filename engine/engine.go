package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/nicksnyder/go-i18n/v2/i18n"
	"golang.org/x/text/language"
)

const (
	templatesDir    = "engine/templates/"
	shutdownTimeout = 5 * time.Second
)

// Engine encapsulates shared non-OGC API specific logic
type Engine struct {
	Config    *Config
	OpenAPI   *OpenAPI
	Templates *Templates
	CN        *ContentNegotiation
}

// NewEngine builds a new Engine
func NewEngine(configFile string, openAPIFile string) *Engine {
	config := ReadConfigFile(configFile)

	return NewEngineWithConfig(config, openAPIFile)
}

// NewEngineWithConfig builds a new Engine
func NewEngineWithConfig(config *Config, openAPIFile string) *Engine {
	contentNegotiation := newContentNegotiation()
	localizers := initLocalizers()
	templates := newTemplates(config, localizers)
	openAPI := newOpenAPI(config, openAPIFile)

	engine := &Engine{
		Config:    config,
		OpenAPI:   openAPI,
		Templates: templates,
		CN:        contentNegotiation,
	}
	return engine
}

func initLocalizers() map[language.Tag]i18n.Localizer {
	localizers := make(map[language.Tag]i18n.Localizer)
	// dutch
	nlBundle := i18n.NewBundle(language.Dutch)
	nlBundle.RegisterUnmarshalFunc("toml", toml.Unmarshal)
	nlBundle.MustLoadMessageFile("assets/i18n/active.nl.toml")
	localizers[language.Dutch] = *i18n.NewLocalizer(nlBundle, "nl")
	// english
	enBundle := i18n.NewBundle(language.English)
	enBundle.RegisterUnmarshalFunc("toml", toml.Unmarshal)
	enBundle.MustLoadMessageFile("assets/i18n/active.en.toml")
	localizers[language.English] = *i18n.NewLocalizer(enBundle, "en")
	return localizers
}

// Start the engine by initializing all components and starting the server
func (e *Engine) Start(address string, router *chi.Mux, debugPort int, shutdownDelay int) error {
	// debug server (binds to localhost).
	if debugPort > 0 {
		go func() {
			debugAddress := fmt.Sprintf("localhost:%d", debugPort)
			debugRouter := chi.NewRouter()
			debugRouter.Use(middleware.Logger)
			debugRouter.Mount("/debug", middleware.Profiler())
			err := e.startServer("debug server", debugAddress, 0, debugRouter)
			if err != nil {
				log.Fatalf("debug server failed %v", err)
			}
		}()
	}

	// main server
	return e.startServer("main server", address, shutdownDelay, router)
}

// startServer creates and starts an HTTP server, also takes care of graceful shutdown
func (e *Engine) startServer(name string, address string, shutdownDelay int, router *chi.Mux) error {
	// Create HTTP server
	server := http.Server{
		Addr:    address,
		Handler: router,

		ReadTimeout:       15 * time.Second,
		ReadHeaderTimeout: 15 * time.Second,
		WriteTimeout:      30 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGQUIT)
	defer stop()

	go func() {
		log.Printf("%s listening on %s", name, address)
		// ListenAndServe always returns a non-nil error. After Shutdown or
		// Close, the returned error is ErrServerClosed
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("failed to shutdown %s: %v", name, err)
		}
	}()

	// Listen for interrupt signal and then perform shutdown
	<-ctx.Done()
	stop()

	if shutdownDelay > 0 {
		log.Printf("stop signal received, initiating shutdown of %s after %d seconds delay", name, shutdownDelay)
		time.Sleep(time.Duration(shutdownDelay) * time.Second)
	}
	log.Printf("shutting down %s gracefully", name)

	// Shutdown with a max timeout.
	timeoutCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	return server.Shutdown(timeoutCtx)
}

// RenderTemplates renders both HTMl and non-HTML templates depending on the format given in the TemplateKey.
// This method also performs OpenAPI validation of the rendered template, therefore we also need the URL path.
func (e *Engine) RenderTemplates(urlPath string, breadcrumbs []Breadcrumb, keys ...TemplateKey) {
	for _, key := range keys {
		if key.Format == FormatHTML {
			e.Templates.renderHTMLTemplate(key, breadcrumbs, nil)
		} else {
			e.Templates.renderNonHTMLTemplate(key, nil)
		}
		// we already perform OpenAPI validation here during startup to catch
		// issues early on, in addition to runtime OpenAPI response validation
		// TODO: deal with multiple keys created due to multiple languages
		// e.validateStaticResponse(key, urlPath)
	}
}

// RenderTemplatesWithParams renders both HTMl and non-HTML templates depending on the format given in the TemplateKey.
// This method does bot perform OpenAPI validation of the rendered template (will be done during runtime).
func (e *Engine) RenderTemplatesWithParams(params interface{}, breadcrumbs []Breadcrumb, keys ...TemplateKey) {
	for _, key := range keys {
		if key.Format == FormatHTML {
			e.Templates.renderHTMLTemplate(key, breadcrumbs, params)
		} else {
			e.Templates.renderNonHTMLTemplate(key, params)
		}
	}
}

// ServePage validates incoming HTTP request against OpenAPI spec, renders given template and serves as HTTP response
func (e *Engine) ServePage(w http.ResponseWriter, r *http.Request, templateKey TemplateKey) {
	// validate request
	if err := e.OpenAPI.validateRequest(r); err != nil {
		log.Printf("%v", err.Error())
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// render output
	template, err := e.Templates.GetRenderedTemplate(templateKey)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	contentType := e.CN.formatToMediaType(templateKey.Format)

	// validate response
	if err := e.OpenAPI.validateResponse(contentType, template, r); err != nil {
		log.Printf("%v", err.Error())
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// return response to client
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	if _, err = w.Write(template); err != nil {
		log.Printf("Write failed: %v\n", err)
	}
}

// ReverseProxy forwards given HTTP request to given target server, and optionally tweaks response
func (e *Engine) ReverseProxy(w http.ResponseWriter, r *http.Request, target *url.URL,
	prefer204 bool, contentTypeOverwrite string) {

	rewrite := func(r *httputil.ProxyRequest) {
		r.Out.URL = target
		r.Out.Host = ""   // Don't pass Host header (similar to Traefik's passHostHeader=false)
		r.SetXForwarded() // Set X-Forwarded-* headers.
		r.Out.Header.Set("X-BaseUrl", e.Config.BaseURL.String())
	}

	modifyResponse := func(proxyRes *http.Response) error {
		if prefer204 {
			// OGC spec: If the tile has no content due to lack of data in the area, but is within the data
			// resource it's tile matrix sets and tile matrix sets limits, the HTTP response will use the status
			// code either 204 (indicating an empty tile with no content) or a 200
			if proxyRes.StatusCode == http.StatusNotFound {
				proxyRes.StatusCode = http.StatusNoContent
				removeBody(proxyRes)
			}
			if contentTypeOverwrite != "" {
				proxyRes.Header.Set("Content-Type", contentTypeOverwrite)
			}
		}
		return nil
	}

	reverseProxy := &httputil.ReverseProxy{Rewrite: rewrite, ModifyResponse: modifyResponse}
	reverseProxy.ServeHTTP(w, r)
}

func removeBody(proxyRes *http.Response) {
	buf := bytes.NewBuffer(make([]byte, 0))
	proxyRes.Body = io.NopCloser(buf)
	proxyRes.Header["Content-Length"] = []string{"0"}
	proxyRes.Header["Content-Type"] = []string{}
}

func (e *Engine) validateStaticResponse(key TemplateKey, urlPath string) {
	template, _ := e.Templates.GetRenderedTemplate(key)
	serverURL := normalizeBaseURL(e.Config.BaseURL.String())
	req, err := http.NewRequest(http.MethodGet, serverURL+urlPath, nil)
	if err != nil {
		log.Fatalf("failed to construct request to validate %s "+
			"template against OpenAPI spec %v", key.Name, err)
	}
	err = e.OpenAPI.validateResponse(e.CN.formatToMediaType(key.Format), template, req)
	if err != nil {
		log.Fatalf("validation of template %s failed: %v", key.Name, err)
	}
}

// SafeWrite executes the given http.ResponseWriter.Write while logging errors
func SafeWrite(write func([]byte) (int, error), body []byte) {
	_, err := write(body)
	if err != nil {
		log.Printf("failed to write response: %v", err)
	}
}
