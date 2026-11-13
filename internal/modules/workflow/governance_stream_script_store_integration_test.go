package workflow

import (
	"bytes"
	"context"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

func TestStreamScriptMemoActualStore(t *testing.T) {
	if os.Getenv("DATAMESH_STREAM_SCRIPT_STORE_TEST") != "1" {
		t.Skip("requires actual private object store")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	store, e := minio.New("rustfs:9000", &minio.Options{Creds: credentials.NewStaticV4(os.Getenv("RUSTFS_ACCESS_KEY"), os.Getenv("RUSTFS_SECRET_KEY"), ""), Region: "us-east-1"})
	if e != nil {
		t.Fatal("private store setup failed")
	}
	id, e := newID()
	if e != nil {
		t.Fatal(e)
	}
	key := "acceptance/" + id
	s := &Server{runtime: &Runtime{store: store, key: bytes.Repeat([]byte{73}, 32)}}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if e := store.RemoveObject(cleanup, streamScriptMemoBucket, key, minio.RemoveObjectOptions{}); e != nil {
			t.Error("owned test object cleanup failed")
		}
	}()
	var count atomic.Int64
	start := make(chan struct{})
	var ready sync.WaitGroup
	ready.Add(2)
	compute := func() (DataRow, error) {
		n := count.Add(1)
		ready.Done()
		<-start
		return DataRow{"nonce": encode(strconv.FormatInt(n, 10))}, nil
	}
	type answer struct {
		row DataRow
		err error
	}
	answers := make(chan answer, 2)
	for i := 0; i < 2; i++ {
		go func() { row, err := s.streamScriptMemo(ctx, key, compute); answers <- answer{row, err} }()
	}
	waiting := make(chan struct{})
	go func() { ready.Wait(); close(waiting) }()
	select {
	case <-waiting:
		close(start)
	case <-ctx.Done():
		close(start)
		t.Fatal("store concurrent candidates did not start")
	}
	first, second := <-answers, <-answers
	if first.err != nil || second.err != nil {
		t.Fatalf("actual immutable memo failed: %v %v", first.err, second.err)
	}
	if !bytes.Equal(encode(first.row), encode(second.row)) {
		t.Fatal("actual store overwrote the durable winner")
	}
	if count.Load() != 2 {
		t.Fatal("test did not exercise concurrent conditional writers")
	}
	restarted := &Server{runtime: &Runtime{store: store, key: bytes.Repeat([]byte{73}, 32)}}
	hit, e := restarted.streamScriptMemo(ctx, key, func() (DataRow, error) { t.Error("durable hit re-executed code"); return nil, invalid })
	if e != nil || !bytes.Equal(encode(hit), encode(first.row)) {
		t.Fatal("actual durable restart cache changed")
	}
	t.Log("actual RustFS: concurrent conditional writers kept one encrypted durable value; restart reused it")
}
