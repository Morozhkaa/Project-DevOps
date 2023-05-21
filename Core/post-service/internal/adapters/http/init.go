package http

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"post-service/internal/domain/models"
	"post-service/internal/ports"
	"post-service/logger"
	"sync"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.uber.org/zap"
)

type Adapter struct {
	s           *http.Server
	l           net.Listener
	PostService ports.Post
	authURL     string
	client      *http.Client
}

type AdapterOptions struct {
	HTTP_port int
	IsProd    bool
	AuthURL   string
}

func New(postService ports.Post, opts AdapterOptions) (*Adapter, error) {

	l, err := net.Listen("tcp", fmt.Sprintf(":%d", opts.HTTP_port))
	if err != nil {
		return nil, fmt.Errorf("server start failed: %w", err)
	}

	router := gin.Default()
	config := cors.DefaultConfig()
	//config.AllowAllOrigins = true
	config.AllowOrigins = []string{"http://localhost:3000"}
	config.AllowCredentials = true
	router.Use(cors.New(config))

	server := http.Server{
		Handler:      router,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}

	a := Adapter{
		s:           &server,
		l:           l,
		PostService: postService,
		authURL:     opts.AuthURL,
		client: &http.Client{
			Transport: otelhttp.NewTransport(http.DefaultTransport),
			Jar:       jar,
		},
	}
	optsLogger := logger.LoggerOptions{IsProd: opts.IsProd}
	err = initRouter(&a, router, optsLogger)

	return &a, err
}

func (a *Adapter) Start() error {
	err := a.s.Serve(a.l)
	if err != http.ErrServerClosed {
		return err
	}
	return nil
}

type Response struct {
	Email string          `json:"email"`
	Login string          `json:"login"`
	Role  models.UserRole `json:"role"`
}

func (a *Adapter) Verify(ctx *gin.Context) error {

	log := zap.L()
	log.Info("authURL: " + a.authURL)
	authR, err := http.NewRequestWithContext(ctx.Request.Context(), "POST", a.authURL+"verify", nil)
	if err != nil {
		return err
	}

	accessToken, err := ctx.Cookie("access_token")
	log.Info("In_verify: get access_token: " + accessToken)
	if err != nil {
		return models.ErrForbidden
	}
	refreshToken, err := ctx.Cookie("refresh_token")
	log.Info("In_verify: get refresh_token: " + refreshToken)
	if err != nil {
		return models.ErrBadRequest
	}

	a.client.Jar.SetCookies(authR.URL, []*http.Cookie{
		{
			Name:  "access_token",
			Value: accessToken,
			Path:  "/",
		},
		{
			Name:  "refresh_token",
			Value: refreshToken,
			Path:  "/",
		},
	})

	resp, err := a.client.Do(authR)

	if err != nil || resp.StatusCode != http.StatusOK {
		log.Info("In_verify: error in auth-service verify")
		return models.ErrForbidden
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil
	}
	r := &Response{}
	err = json.Unmarshal(data, r)
	if err != nil {
		return nil
	}
	ctx.Set("email", r.Email)
	ctx.Set("login", r.Login)
	ctx.Set("role", r.Role)
	log.Info("In_verify: set email, login, role: " + r.Email + " " + r.Login + " " + string(r.Role))

	return nil
}

func (a *Adapter) Stop(ctx context.Context) error {
	var (
		err  error
		once sync.Once
	)
	once.Do(func() {
		err = a.s.Shutdown(ctx)
	})
	return err
}
