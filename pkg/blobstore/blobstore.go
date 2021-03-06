package blobstore // import "a4.io/blobstash/pkg/blobstore"

import (
	"context"
	"fmt"
	"path/filepath"

	log "github.com/inconshreveable/log15"

	"a4.io/blobsfile"

	// "a4.io/blobstash/pkg/backend/blobsfile"
	"a4.io/blobstash/pkg/backend/s3"
	"a4.io/blobstash/pkg/blob"
	"a4.io/blobstash/pkg/config"
	"a4.io/blobstash/pkg/ctxutil"
	"a4.io/blobstash/pkg/hub"
)

var ErrBlobExists = fmt.Errorf("blob exist")

type BlobStore struct {
	back   *blobsfile.BlobsFiles
	s3back *s3.S3Backend
	hub    *hub.Hub
	conf   *config.Config

	log log.Logger
}

func New(logger log.Logger, conf2 *config.Config, hub *hub.Hub) (*BlobStore, error) {
	logger.Debug("init")

	back, err := blobsfile.New(&blobsfile.Opts{Directory: filepath.Join(conf2.VarDir(), "blobs")})
	if err != nil {
		return nil, fmt.Errorf("failed to init BlobsFile: %v", err)
	}
	var s3back *s3.S3Backend
	if s3repl := conf2.S3Repl; s3repl != nil && s3repl.Bucket != "" {
		logger.Debug("init s3 replication")
		var err error
		s3back, err = s3.New(logger.New("app", "s3_replication"), back, hub, conf2)
		if err != nil {
			return nil, err
		}
	}

	return &BlobStore{
		back:   back,
		s3back: s3back,
		hub:    hub,
		conf:   conf2,
		log:    logger,
	}, nil
}

func (bs *BlobStore) Close() error {
	// TODO(tsileo): improve this
	if bs.s3back != nil {
		bs.s3back.Close()
	}

	if err := bs.back.Close(); err != nil {
		return err
	}
	return nil
}

func (bs *BlobStore) Put(ctx context.Context, blob *blob.Blob) error {
	_, fromHttp := ctxutil.Request(ctx)
	bs.log.Info("OP Put", "from_http", fromHttp, "hash", blob.Hash, "len", len(blob.Data))

	// Ensure the blob hash match the blob content
	if err := blob.Check(); err != nil {
		return err
	}

	exists, err := bs.back.Exists(blob.Hash)
	if err != nil {
		return err
	}

	if exists {
		bs.log.Debug("blob already saved", "hash", blob.Hash)
		return nil
	}

	// Save the blob
	if err := bs.back.Put(blob.Hash, blob.Data); err != nil {
		return err
	}

	// Wait for adding the blob to the S3 replication queue if enabled
	if bs.s3back != nil {
		if err := bs.s3back.Put(blob.Hash); err != nil {
			return err
		}
	}

	// Wait for subscribed event completion
	if err := bs.hub.NewBlobEvent(ctx, blob, nil); err != nil {
		return err
	}

	bs.log.Debug("blob saved", "hash", blob.Hash)
	return nil
}

func (bs *BlobStore) Get(ctx context.Context, hash string) ([]byte, error) {
	_, fromHttp := ctxutil.Request(ctx)
	bs.log.Info("OP Get", "from_http", fromHttp, "hash", hash)
	return bs.back.Get(hash)
}

func (bs *BlobStore) Stat(ctx context.Context, hash string) (bool, error) {
	_, fromHttp := ctxutil.Request(ctx)
	bs.log.Info("OP Stat", "from_http", fromHttp, "hash", hash)
	return bs.back.Exists(hash)
}

// func (backend *BlobsFileBackend) Enumerate(blobs chan<- *blob.SizedBlobRef, start, stop string, limit int) error {
func (bs *BlobStore) Enumerate(ctx context.Context, start, end string, limit int) ([]*blob.SizedBlobRef, error) {
	return bs.enumerate(ctx, start, end, limit, false)
}

func (bs *BlobStore) Scan(ctx context.Context) error {
	_, err := bs.enumerate(ctx, "", "\xff", 0, true)
	return err
}

func (bs *BlobStore) enumerate(ctx context.Context, start, end string, limit int, scan bool) ([]*blob.SizedBlobRef, error) {
	_, fromHttp := ctxutil.Request(ctx)
	bs.log.Info("OP Enumerate", "from_http", fromHttp, "start", start, "end", end, "limit", limit)
	out := make(chan *blobsfile.Blob)
	refs := []*blob.SizedBlobRef{}
	errc := make(chan error, 1)
	go func() {
		errc <- bs.back.Enumerate(out, start, end, limit)
	}()
	for cblob := range out {
		if scan {
			fullblob, err := bs.Get(ctx, cblob.Hash)
			if err != nil {
				return nil, err
			}
			if err := bs.hub.ScanBlobEvent(ctx, &blob.Blob{Hash: cblob.Hash, Data: fullblob}, nil); err != nil {
				return nil, err
			}
		}
		refs = append(refs, &blob.SizedBlobRef{Hash: cblob.Hash, Size: cblob.Size})
	}
	if err := <-errc; err != nil {
		return nil, err
	}
	return refs, nil
}
