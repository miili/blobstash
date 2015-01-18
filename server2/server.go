package server2

import (
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/tsileo/blobstash/api"
	"github.com/tsileo/blobstash/backend"
	"github.com/tsileo/blobstash/config2"
	"github.com/tsileo/blobstash/meta"
	"github.com/tsileo/blobstash/router"
	"github.com/tsileo/blobstash/vkv"
)

var defaultConf = map[string]interface{}{
	"backends": map[string]interface{}{
		"blobs": map[string]interface{}{
			"backend-type": "blobsfile",
			"backend-args": map[string]interface{}{
				"path": "blobs",
			},
		},
	},
	"router": []interface{}{[]interface{}{"default", "blobs"}},
}

type Server struct {
	Router      *router.Router
	Backends    map[string]backend.BlobHandler
	DB          *vkv.DB
	metaHandler *meta.MetaHandler

	KvUpdate chan *vkv.KeyValue

	stop chan struct{}
	wg   sync.WaitGroup
}

func New(conf map[string]interface{}) *Server {
	if conf == nil {
		conf = defaultConf
	}
	db, err := vkv.New("devdb")
	if err != nil {
		panic(err)
	}
	server := &Server{
		Router:   router.New(conf["router"].([]interface{})),
		Backends: map[string]backend.BlobHandler{},
		DB:       db,
		KvUpdate: make(chan *vkv.KeyValue),
		stop:     make(chan struct{}, 1),
	}
	// TODO hook vkv and pathutil
	backends := conf["backends"].(map[string]interface{})
	for _, b := range server.Router.ResolveBackends() {
		server.Backends[b] = config2.NewFromConfig(backends[b].(map[string]interface{}))
		server.Router.Backends[b] = server.Backends[b]
	}
	server.metaHandler = meta.New(server.Router)
	return server
}

func (s *Server) Run() error {
	go s.metaHandler.WatchKvUpdate(s.wg, s.KvUpdate)
	go func() {
		s.wg.Add(1)
		defer s.wg.Done()
		if err := s.metaHandler.Scan(); err != nil {
			panic(err)
		}
	}()
	r := api.New(s.wg, s.DB, s.KvUpdate, s.Router)
	http.Handle("/", r)
	go func() {
		if err := http.ListenAndServe(":8050", nil); err != nil {
			panic(err)
		}
	}()
	cs := make(chan os.Signal, 1)
	signal.Notify(cs, os.Interrupt,
		syscall.SIGHUP,
		syscall.SIGINT,
		syscall.SIGTERM,
		syscall.SIGQUIT)
	for {
		select {
		case _ = <-s.stop:
			break
		case sig := <-cs:
			log.Printf("server: Captured %v\n", sig)
			break
		}
		s.Close()
		os.Exit(0)
	}
}

func (s *Server) Close() {
	s.wg.Wait()
	close(s.KvUpdate)
	s.DB.Close()
	for _, b := range s.Backends {
		b.Close()
	}
}
