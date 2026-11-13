package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"github.com/minio/minio-go/v7"
	"io"
	"math"
)

const privateFileMode = "aes-256-gcm-v1"
const privateFileOverhead int64 = 28

func fileDigest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func (s *Server) storePrivateFile(ctx context.Context, name, typ string, data []byte) (FileConfig, error) {
	id, err := newID()
	if err != nil {
		return FileConfig{}, err
	}
	return s.storePrivateFileAt(ctx, name, typ, data, "resources/"+id)
}
func (s *Server) storePrivateFileAt(ctx context.Context, name, typ string, data []byte, key string) (FileConfig, error) {
	encrypted, err := s.runtime.cryptScope(data, true, "datamesh-private-file-v1:"+key)
	if err != nil {
		return FileConfig{}, err
	}
	object, err := s.storeFileAt(ctx, name, "application/octet-stream", encrypted, key)
	if err != nil {
		return object, err
	}
	object.Encryption = privateFileMode
	object.CipherSize = object.Size
	object.CipherSHA256 = object.SHA256
	object.Size = int64(len(data))
	object.SHA256 = fileDigest(data)
	object.MIME = typ
	return object, nil
}
func privateFileBounds(v FileConfig, max int64) error {
	if v.Encryption != privateFileMode || max < 0 || max > math.MaxInt64-privateFileOverhead-1 || v.Size < 0 || v.Size > max || v.CipherSize != v.Size+privateFileOverhead || len(v.CipherSHA256) != 64 || len(v.SHA256) != 64 {
		return invalid
	}
	return nil
}
func (s *Server) openPrivateFile(v FileConfig, data []byte, max int64) ([]byte, error) {
	if err := privateFileBounds(v, max); err != nil {
		return nil, err
	}
	if int64(len(data)) != v.CipherSize || fileDigest(data) != v.CipherSHA256 {
		return nil, invalid
	}
	plain, err := s.runtime.cryptScope(data, false, "datamesh-private-file-v1:"+v.Key)
	if err != nil {
		return nil, err
	}
	if int64(len(plain)) != v.Size || fileDigest(plain) != v.SHA256 {
		return nil, invalid
	}
	return plain, nil
}
func (s *Server) readPrivateFile(ctx context.Context, v FileConfig, max int64) ([]byte, error) {
	if err := privateFileBounds(v, max); err != nil {
		return nil, err
	}
	object, err := s.runtime.store.GetObject(ctx, resourceBucket, v.Key, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	defer object.Close()
	data, err := io.ReadAll(io.LimitReader(object, v.CipherSize+1))
	if err != nil {
		return nil, err
	}
	return s.openPrivateFile(v, data, max)
}
