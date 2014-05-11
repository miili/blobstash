package client

import (
	"os"
	"bytes"
	"crypto/sha1"
	"fmt"
	"log"
	"io"
	"bufio"
	"sync"
	"github.com/tsileo/silokv/rolling"
	"github.com/garyburd/redigo/redis"
	"path/filepath"
	"errors"
)

// FileWriter reads the file byte and byte and upload it,
// chunk by chunk, it also constructs the file index .
func (client *Client) FileWriter(txID, key, path string) (*WriteResult, error) {
	writeResult := &WriteResult{}
	window := 64
	rs := rolling.New(window)
	f, err := os.Open(path)
	defer f.Close()
	if err != nil {
		return writeResult, err
	}
	freader := bufio.NewReader(f)
	con := client.Pool.Get()
	defer con.Close()
	if _, err := con.Do("TXINIT", txID); err != nil {
		log.Printf("Error TXINIT %v, %v", txID, err)
		return writeResult, err
	}
	log.Printf("FileWriter(%v, %v, %v)", txID, key, path)
	var buf bytes.Buffer
	buf.Reset()
	fullHash := sha1.New()
	eof := false
	i := 0
	for {
		b := make([]byte, 1)
		_, err := freader.Read(b)
		if err == io.EOF {
			eof = true
		} else {
			rs.Write(b)
			buf.Write(b)
			i++
		}
		onSplit := rs.OnSplit()
		if (onSplit && (buf.Len() > 64 << 10)) || buf.Len() >= 1 << 20 || eof {
			nsha := SHA1(buf.Bytes())
			ndata := string(buf.Bytes())
			fullHash.Write(buf.Bytes())
			// Check if the blob exists
			exists, err := redis.Bool(con.Do("BEXISTS", nsha))
			if err != nil {
				panic(fmt.Sprintf("DB error: %v", err))
			}
			if !exists {
				rsha, err := redis.String(con.Do("BPUT", ndata))
				if err != nil {
					panic(fmt.Sprintf("DB error: %v", err))
				}
				writeResult.UploadedCnt++
				writeResult.UploadedSize += buf.Len()
				// Check if the hash returned correspond to the locally computed hash
				if rsha != nsha {
					panic(fmt.Sprintf("Corrupted data: %+v/%+v", rsha, nsha))
				}
			} else {
				writeResult.SkippedSize += buf.Len()
				writeResult.SkippedCnt++
			}
			writeResult.Size += buf.Len()
			buf.Reset()
			writeResult.BlobsCnt++
			// Save the location and the blob hash into a sorted list (with the offset as index)
			con.Do("LADD", key, writeResult.Size, nsha)
		}
		if eof {
			break
		}
	}
	writeResult.Hash = fmt.Sprintf("%x", fullHash.Sum(nil))
	log.Printf("PutFile WriteResult:%+v", writeResult)
	return writeResult, nil
}

func (client *Client) PutFile(txID, path string) (meta *Meta, wr *WriteResult, err error) {
	//log.Printf("PutFile %v\n", path)
	client.StartUpload()
	defer client.UploadDone()
	if _, err = os.Stat(path); os.IsNotExist(err) {
		return
	}
	_, filename := filepath.Split(path)
	sha := FullSHA1(path)
	con := client.Pool.Get()
	defer con.Close()
	// First we check if the file isn't already uploaded,
	// if so we skip it.
	cnt, err := redis.Int(con.Do("HLEN", sha))
	if err != nil {
		return
	}
	if cnt > 0 {
		wr = &WriteResult{}
		wr.Hash = sha
		wr.AlreadyExists = true
		wr.Filename = filename
	} else {
		wr, err = client.FileWriter(txID, sha, path)
		if err != nil {
			return
		}	
	}
	meta = NewMeta()
	if sha != wr.Hash {
		err = errors.New("initial hash and WriteResult aren't the same")
		return
	}
	meta.Hash = wr.Hash
	meta.Name = filename
	meta.Size = wr.Size
	meta.Type = "file"
	// TODO(tsileo) load if it already exits ?
	if cnt == 0 {
		err = meta.Save(txID, client.Pool)	
	}
	return
}

// PutFileWg is a wrapper around PutFile except it take a sync.WaitGroup and two channels.
func (client *Client) PutFileWg(txID, path string, wg *sync.WaitGroup, cwrrc chan<- *WriteResult, errch chan<- error) {
	defer wg.Done()
	_, wr, err := client.PutFile(txID, path)
	if err != nil {
		errch <- err
	} else {
		cwrrc <- wr	
	}
	return
}