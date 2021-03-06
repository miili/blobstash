package rangedb // import "a4.io/blobstash/pkg/rangedb"

import (
	"bytes"
	"io"
	"os"
	"sync"

	"github.com/cznic/kv"
)

type RangeDB struct {
	db   *kv.DB
	path string
	mu   *sync.Mutex
}

// New creates a new database.
func New(path string) (*RangeDB, error) {
	createOpen := kv.Open
	if _, err := os.Stat(path); os.IsNotExist(err) {
		createOpen = kv.Create
	}
	kvdb, err := createOpen(path, &kv.Options{})
	if err != nil {
		return nil, err
	}
	return &RangeDB{
		db:   kvdb,
		path: path,
		mu:   new(sync.Mutex),
	}, nil
}

func (db *RangeDB) Close() error {
	return db.db.Close()
}

func (db *RangeDB) Destroy() error {
	if db.path != "" {
		db.Close()
		return os.RemoveAll(db.path)
	}
	return nil
}

func (db *RangeDB) Set(k, v []byte) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	if err := db.db.Set(k, v); err != nil {
		return err
	}
	return nil
}

func (db *RangeDB) Get(k []byte) ([]byte, error) {
	return db.db.Get(nil, k)
}

type Range struct {
	Reverse  bool
	Min, Max []byte
	db       *RangeDB
	enum     *kv.Enumerator
}

func (db *RangeDB) Range(min, max []byte, reverse bool) *Range {
	return &Range{
		Min:     min,
		Max:     max,
		Reverse: reverse,
		db:      db,
	}
}

func (r *Range) first() ([]byte, []byte, error) {
	var err error
	if r.Reverse {
		r.enum, _, err = r.db.db.Seek(r.Max)
		if err != nil {
			return nil, nil, err
		}
		k, v, err := r.enum.Prev()
		if err == io.EOF {
			r.enum, err = r.db.db.SeekLast()
			return r.enum.Prev()
		}
		if err != nil {
			return nil, nil, err
		}
		if bytes.Compare(k, r.Max) > 0 {
			k, v, err = r.enum.Prev()
		}
		return k, v, err
	}

	r.enum, _, err = r.db.db.Seek(r.Min)
	return r.enum.Next()
}

func (r *Range) next() ([]byte, []byte, error) {
	if r.enum == nil {
		return r.first()
	}
	if r.Reverse {
		return r.enum.Prev()
	}
	return r.enum.Next()
}

func (r *Range) Next() ([]byte, []byte, error) {
	r.db.mu.Lock()
	defer r.db.mu.Unlock()

	k, v, err := r.next()
	if r.shouldContinue(k) {
		return k, v, err
	}
	return nil, nil, io.EOF
}

func (r *Range) shouldContinue(key []byte) bool {
	if r.Reverse {
		return key != nil && bytes.Compare(key, r.Min) >= 0
	}

	return key != nil && bytes.Compare(key, r.Max) <= 0
}
