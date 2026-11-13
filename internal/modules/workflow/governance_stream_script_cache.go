package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"io"

	"datamesh/internal/platform/coderunner"
	"github.com/minio/minio-go/v7"
)

const streamScriptMemoBucket = "datamesh-governance-cache"

func streamScriptMemoUnavailable() error {
	return problem(503, "STREAM_SCRIPT_CACHE_UNAVAILABLE", "流式代码结果存储暂不可用")
}

// A dedicated immutable bucket keeps replay state outside raw archive retention.
// No entry can expire while a paused stream or savepoint may still replay it.
func (s *Server) streamScriptMemo(ctx context.Context, key string, compute func() (DataRow, error)) (DataRow, error) {
	if s.runtime == nil || s.runtime.store == nil || len(s.runtime.key) != 32 {
		return nil, streamScriptMemoUnavailable()
	}
	row, missing, err := s.readStreamScriptMemo(ctx, key)
	if err != nil || missing == "" {
		return row, err
	}
	if missing == "NoSuchBucket" {
		err := s.runtime.store.MakeBucket(ctx, streamScriptMemoBucket, minio.MakeBucketOptions{Region: "us-east-1"})
		if err != nil && minio.ToErrorResponse(err).Code != "BucketAlreadyOwnedByYou" {
			return nil, streamScriptMemoUnavailable()
		}
		row, missing, err = s.readStreamScriptMemo(ctx, key)
		if err != nil || missing == "" {
			return row, err
		}
		if missing != "NoSuchKey" {
			return nil, streamScriptMemoUnavailable()
		}
	}
	row, err = compute()
	if err != nil {
		return nil, err
	}
	plain, err := json.Marshal(row)
	if err != nil || row == nil || len(plain) > coderunner.MaxRowBytes {
		return nil, problem(503, "STREAM_SCRIPT_CACHE_INVALID", "流式代码结果不符合存储约束")
	}
	ciphertext, err := s.runtime.cryptScope(plain, true, "datamesh-stream-script:"+key)
	if err != nil {
		return nil, streamScriptMemoUnavailable()
	}
	options := minio.PutObjectOptions{ContentType: "application/octet-stream", DisableMultipart: true}
	options.SetMatchETagExcept("*")
	// The SDK may retry these exact bytes with the same create-only condition.
	// Unlike execution retries, that cannot change an already committed winner.
	_, err = s.runtime.store.PutObject(ctx, streamScriptMemoBucket, key, bytes.NewReader(ciphertext), int64(len(ciphertext)), options)
	if err != nil {
		response := minio.ToErrorResponse(err)
		if response.StatusCode != 409 && response.StatusCode != 412 {
			// Unknown writes are never overwritten or recomputed in this request.
			// A retry starts by reading the potentially committed durable winner.
			return nil, streamScriptMemoUnavailable()
		}
	}
	// Even an acknowledged writer must return the persisted winner, never its
	// private candidate: a competing evaluation may have won the conditional PUT.
	row, missing, err = s.readStreamScriptMemo(ctx, key)
	if err != nil {
		return nil, err
	}
	if missing != "" {
		return nil, streamScriptMemoUnavailable()
	}
	return row, nil
}

func (s *Server) readStreamScriptMemo(ctx context.Context, key string) (DataRow, string, error) {
	object, err := s.runtime.store.GetObject(ctx, streamScriptMemoBucket, key, minio.GetObjectOptions{})
	if err == nil {
		defer object.Close()
		const limit = coderunner.MaxRowBytes + 1024
		var ciphertext []byte
		ciphertext, err = io.ReadAll(io.LimitReader(object, limit+1))
		if err == nil {
			if len(ciphertext) > limit {
				return nil, "", problem(503, "STREAM_SCRIPT_CACHE_INVALID", "流式代码结果校验失败")
			}
			plain, decryptErr := s.runtime.cryptScope(ciphertext, false, "datamesh-stream-script:"+key)
			var row DataRow
			if decryptErr != nil || len(plain) > coderunner.MaxRowBytes || json.Unmarshal(plain, &row) != nil || row == nil {
				return nil, "", problem(503, "STREAM_SCRIPT_CACHE_INVALID", "流式代码结果校验失败")
			}
			return row, "", nil
		}
	}
	code := minio.ToErrorResponse(err).Code
	if code == "NoSuchKey" || code == "NoSuchBucket" {
		return nil, code, nil
	}
	return nil, "", streamScriptMemoUnavailable()
}
