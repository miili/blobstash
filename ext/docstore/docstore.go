/*

Package docstore implements a JSON-based document store
built on top of the Versioned Key-Value store and the Blob store.

Each document will get assigned a MongoDB like ObjectId:

	<binary encoded uint32 (4 bytes) + blob ref (32 bytes)>

The resulting id will have a length of 72 characters encoded as hex.

The JSON document will be stored as is and kvk entry will reference it.

	docstore:<collection>:<id> => (empty)

The pointer contains an empty value since the hash is contained in the id.

Document will be automatically sorted by creation time thanks to the ID.

The raw JSON will be store unmodified but the API will add these fields on the fly:

 - `_id`: the hex ID
 - `_hash`: the hash of the JSON blob
 - `_created_at`: UNIX timestamp of creation date

*/
package docstore

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dchest/blake2b"
	"github.com/gorilla/mux"
	"github.com/tsileo/blobstash/client/interface"
	"github.com/tsileo/blobstash/ext/docstore/id"
)

var KeyFmt = "docstore:%s:%s"

func hashFromKey(col, key string) string {
	return strings.Replace(key, fmt.Sprintf("docstore:%s:", col), "", 1)
}

// TODO(ts) full text indexing, find a way to get the config index

// FIXME(ts) move this in utils/http
func WriteJSON(w http.ResponseWriter, data interface{}) {
	js, err := json.Marshal(data)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(js)
}

type DocStoreExt struct {
	kvStore   client.KvStorer
	blobStore client.BlobStorer
}

func New(kvStore client.KvStorer, blobStore client.BlobStorer) *DocStoreExt {
	return &DocStoreExt{
		kvStore:   kvStore,
		blobStore: blobStore,
	}
}

func (docstore *DocStoreExt) RegisterRoute(r *mux.Router) {
	r.HandleFunc("/{collection}", docstore.DocsHandler())
	r.HandleFunc("/{collection}/{_id}", docstore.DocHandler())
}

func (docstore *DocStoreExt) DocsHandler() func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		vars := mux.Vars(r)
		collection := vars["collection"]
		if collection == "" {
			panic("missing collection query arg")
		}
		switch r.Method {
		case "GET":
			q := r.URL.Query()
			start := fmt.Sprintf(KeyFmt, collection, q.Get("start"))
			// TODO(ts) check the \xff
			end := fmt.Sprintf(KeyFmt, collection, q.Get("end")+"\xff")
			limit := 0
			if q.Get("limit") != "" {
				ilimit, err := strconv.Atoi(q.Get("limit"))
				if err != nil {
					http.Error(w, "bad limit", 500)
				}
				limit = ilimit
			}
			res, err := docstore.kvStore.Keys(start, end, limit)
			if err != nil {
				panic(err)
			}
			var docs []map[string]interface{}
			for _, kv := range res {
				_id, err := id.FromHex(hashFromKey(collection, kv.Key))
				if err != nil {
					panic(err)
				}
				hash, err := _id.Hash()
				if err != nil {
					panic("failed to extract hash")
				}
				// Fetch the blob
				blob, err := docstore.blobStore.Get(hash)
				if err != nil {
					panic(err)
				}
				// Build the doc
				doc := map[string]interface{}{}
				if err := json.Unmarshal(blob, &doc); err != nil {
					panic(err)
				}
				doc["_id"] = _id
				doc["_hash"] = hash
				doc["_created_at"] = _id.Ts()
				docs = append(docs, doc)
			}
			WriteJSON(w, map[string]interface{}{"data": docs,
				"_meta": map[string]interface{}{
					"start": start,
					"end":   end,
					"limit": limit,
				},
			})
		case "POST":
			// Read the whole body
			blob, err := ioutil.ReadAll(r.Body)
			if err != nil {
				panic(err)
			}
			// Ensure it's JSON encoded
			doc := map[string]interface{}{}
			if err := json.Unmarshal(blob, &doc); err != nil {
				panic(err)
			}
			// Store the payload in a blob
			hash := fmt.Sprintf("%x", blake2b.Sum256(blob))
			docstore.blobStore.Put(hash, blob)
			// Create a pointer in the key-value store
			now := time.Now().UTC().Unix()
			_id, err := id.New(int(now), hash)
			if err != nil {
				panic(err)
			}
			if _, err := docstore.kvStore.Put(fmt.Sprintf(KeyFmt, collection, _id.String()), "", -1); err != nil {
				panic(err)
			}
			// Returns the doc along with its new ID
			doc["_id"] = _id
			doc["_hash"] = hash
			doc["_created_at"] = _id.Ts()
			WriteJSON(w, doc)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}
}

func (docstore *DocStoreExt) DocHandler() func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "GET":
			vars := mux.Vars(r)
			collection := vars["collection"]
			if collection == "" {
				panic("missing collection query arg")
			}
			sid := vars["_id"]
			if sid == "" {
				panic("missing _id query arg")
			}
			// Parse the hex ID
			_id, err := id.FromHex(sid)
			if err != nil {
				panic(fmt.Sprintf("invalid _id: %v", err))
			}
			hash, err := _id.Hash()
			if err != nil {
				panic("failed to extract hash")
			}
			// Fetch the blob
			blob, err := docstore.blobStore.Get(hash)
			if err != nil {
				panic(err)
			}
			// Build the doc
			doc := map[string]interface{}{}
			if err := json.Unmarshal(blob, &doc); err != nil {
				panic(err)
			}
			doc["_id"] = _id
			doc["_hash"] = hash
			doc["_created_at"] = _id.Ts()
			WriteJSON(w, doc)
		}
	}
}