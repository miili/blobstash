package server // import "a4.io/blobstash/pkg/server"

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"golang.org/x/crypto/acme/autocert"
	_ "io"
	_ "log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"

	"a4.io/blobstash/pkg/apps"
	"a4.io/blobstash/pkg/blobstore"
	"a4.io/blobstash/pkg/config"
	"a4.io/blobstash/pkg/docstore"
	"a4.io/blobstash/pkg/filetree"
	"a4.io/blobstash/pkg/httputil"
	"a4.io/blobstash/pkg/hub"
	"a4.io/blobstash/pkg/kvstore"
	"a4.io/blobstash/pkg/meta"
	"a4.io/blobstash/pkg/middleware"
	"a4.io/blobstash/pkg/oplog"
	"a4.io/blobstash/pkg/replication"
	synctable "a4.io/blobstash/pkg/sync"

	"github.com/gorilla/mux"
	log "github.com/inconshreveable/log15"
)

type App interface {
	Register(*mux.Router, func(http.Handler) http.Handler)
}

type Server struct {
	router    *mux.Router
	conf      *config.Config
	log       log.Logger
	closeFunc func() error

	blobstore *blobstore.BlobStore

	hostWhitelist map[string]bool
	shutdown      chan struct{}
	wg            sync.WaitGroup
}

func New(conf *config.Config) (*Server, error) {
	conf.Init()
	logger := log.New("logger", "blobstash")
	logger.SetHandler(log.LvlFilterHandler(conf.LogLvl(), log.StreamHandler(os.Stdout, log.TerminalFormat())))
	var wg sync.WaitGroup
	s := &Server{
		router:        mux.NewRouter().StrictSlash(true),
		conf:          conf,
		hostWhitelist: map[string]bool{},
		log:           logger,
		wg:            wg,
		shutdown:      make(chan struct{}),
	}
	authFunc, basicAuth := middleware.NewBasicAuth(conf)
	hub := hub.New(logger.New("app", "hub"))
	// Load the blobstore
	blobstore, err := blobstore.New(logger.New("app", "blobstore"), conf, hub)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize blobstore app: %v", err)
	}
	s.blobstore = blobstore
	// FIXME(tsileo): handle middleware in the `Register` interface
	blobstore.Register(s.router.PathPrefix("/api/blobstore").Subrouter(), basicAuth)

	// Load the meta
	metaHandler, err := meta.New(logger.New("app", "meta"), hub)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize blobstore meta: %v", err)
	}

	if conf.Replication != nil && conf.Replication.EnableOplog {
		oplg, err := oplog.New(logger.New("app", "oplog"), conf, hub)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize oplog: %v", err)
		}
		oplg.Register(s.router.PathPrefix("/_oplog").Subrouter(), basicAuth)
	}
	// Load the kvstore
	kvstore, err := kvstore.New(logger.New("app", "kvstore"), conf, blobstore, metaHandler)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize kvstore app: %v", err)
	}
	kvstore.Register(s.router.PathPrefix("/api/kvstore").Subrouter(), basicAuth)
	// nsDB, err := nsdb.New(logger.New("app", "nsdb"), conf, blobstore, metaHandler, hub)
	// if err != nil {
	// 	return nil, fmt.Errorf("failed to initialize nsdb: %v", err)
	// }
	// Load the synctable
	synctable := synctable.New(logger.New("app", "sync"), conf, blobstore)
	synctable.Register(s.router.PathPrefix("/api/sync").Subrouter(), basicAuth)

	// Enable replication if set in the config
	if conf.ReplicateFrom != nil {
		if _, err := replication.New(logger.New("app", "replication"), conf, blobstore, synctable, wg); err != nil {
			return nil, fmt.Errorf("failed to initialize replication app: %v", err)
		}
	}

	filetree, err := filetree.New(logger.New("app", "filetree"), conf, authFunc, kvstore, blobstore, hub)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize filetree app: %v", err)
	}
	filetree.Register(s.router.PathPrefix("/api/filetree").Subrouter(), s.router, basicAuth)

	apps, err := apps.New(logger.New("app", "apps"), conf, filetree, hub, s.whitelistHosts)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize filetree app: %v", err)
	}
	apps.Register(s.router.PathPrefix("/api/apps").Subrouter(), s.router, basicAuth)

	docstore, err := docstore.New(logger.New("app", "docstore"), conf, kvstore, blobstore, filetree)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize docstore app: %v", err)
	}
	docstore.Register(s.router.PathPrefix("/api/docstore").Subrouter(), basicAuth)

	// Setup the closeFunc
	s.closeFunc = func() error {
		logger.Debug("waiting for the waitgroup...")
		wg.Wait()
		logger.Debug("waitgroup done")

		if err := blobstore.Close(); err != nil {
			return err
		}
		if err := kvstore.Close(); err != nil {
			return err
		}
		// if err := nsDB.Close(); err != nil {
		// 	return err
		// }
		if err := filetree.Close(); err != nil {
			return err
		}
		if err := docstore.Close(); err != nil {
			return err
		}
		if err := apps.Close(); err != nil {
			return err
		}
		return nil
	}
	return s, nil
}

func (s *Server) Shutdown() {
	s.shutdown <- struct{}{}
	// TODO(tsileo) shotdown sync repl too
}

func (s *Server) Bootstrap() error {
	s.log.Debug("Bootstrap the server")

	// Check if a full scan is requested
	if s.conf.ScanMode {
		s.log.Info("Starting full scan")
		if err := s.blobstore.Scan(context.Background()); err != nil {
			return err
		}
		s.log.Info("Scan done")
	}
	return nil
}

func (s *Server) hostPolicy(hosts ...string) autocert.HostPolicy {
	s.whitelistHosts(hosts...)
	return func(_ context.Context, host string) error {
		if !s.hostWhitelist[host] {
			return errors.New("blobstash: tls host not configured")
		}
		return nil
	}
}

func (s *Server) whitelistHosts(hosts ...string) {
	for _, h := range hosts {
		s.hostWhitelist[h] = true
	}
}

func (s *Server) Serve() error {
	go func() {
		listen := config.DefaultListen
		if s.conf.Listen != "" {
			listen = s.conf.Listen
		}
		s.log.Info(fmt.Sprintf("listening on %v", listen))
		reqLogger := httputil.LoggerMiddleware(s.log)
		h := httputil.RecoverHandler(middleware.CorsMiddleware(reqLogger(middleware.Secure(s.router))))
		if s.conf.AutoTLS {
			cacheDir := autocert.DirCache(filepath.Join(s.conf.ConfigDir(), config.LetsEncryptDir))

			m := autocert.Manager{
				Prompt:     autocert.AcceptTOS,
				HostPolicy: s.hostPolicy(s.conf.Domains...),
				Cache:      cacheDir,
			}
			s := &http.Server{
				Addr:      listen,
				Handler:   h,
				TLSConfig: &tls.Config{GetCertificate: m.GetCertificate},
			}
			s.ListenAndServeTLS("", "")
		} else {
			http.ListenAndServe(listen, h)
		}
	}()
	s.tillShutdown()
	return s.closeFunc()
	// return http.ListenAndServe(":8051", s.router)
}

func (s *Server) tillShutdown() {
	// Listen for shutdown signal
	cs := make(chan os.Signal, 1)
	signal.Notify(cs, os.Interrupt,
		syscall.SIGHUP,
		syscall.SIGINT,
		syscall.SIGTERM,
		syscall.SIGQUIT)
	for {
		select {
		case sig := <-cs:
			s.log.Debug("captured signal", "signal", sig)
			s.log.Info("shutting down...")
			return
		case <-s.shutdown:
			s.log.Info("shutting down...")
			return
		}
	}
}
