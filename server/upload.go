/*
http://sanatgersappa.blogspot.fr/2013/03/handling-multiple-file-uploads-in-go.html

*/
package server

import (
	"net/http"
	"bytes"
	"io"
	"strconv"

	"github.com/gorilla/mux"

	"github.com/tsileo/blobstash/backend"
)

func blobHandler(router *backend.Router) func(http.ResponseWriter, *http.Request) {
	return func (w http.ResponseWriter, r *http.Request) {
		meta, _ := strconv.ParseBool(r.Header.Get("BlobStash-Meta"))
		req := &backend.Request{
			Host: r.Header.Get("BlobStash-Hostname"),
			MetaBlob: meta,
		}
		vars := mux.Vars(r)
		switch {
		case r.Method == "HEAD":
			exists := router.Exists(req, vars["hash"])
			if exists {
				return
			}
			http.Error(w, http.StatusText(404), 404)
			return
		case r.Method == "GET":
			blob, err := router.Get(req, vars["hash"])
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
			w.Write(blob)
			return
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
	}
}

func uploadHandler(jobc chan<- *blobPutJob) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {

		//POST takes the uploaded file(s) and saves it to disk.
		case "POST":
			hostname := r.Header.Get("BlobStash-Hostname")
			//ctx := r.Header.Get("BlobStash-Ctx")
			meta, _ := strconv.ParseBool(r.Header.Get("BlobStash-Meta"))

			//parse the multipart form in the request
			mr, err := r.MultipartReader()
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}

			for {
				part, err := mr.NextPart()
				if err == io.EOF {
					break
				}
				if err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				hash := part.FormName()
				var buf bytes.Buffer
				buf.ReadFrom(part)
				breq := &backend.Request{
					Host: hostname,
					MetaBlob: meta,
					Archive: false,
				}
				jobc<- newBlobPutJob(breq, hash, buf.Bytes(), nil)
			}
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}
}