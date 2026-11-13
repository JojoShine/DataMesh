package workflow

import (
	"bytes"
	"testing"
)

func TestPrivateFileAuthenticatedBounds(t *testing.T) {
	s := &Server{runtime: &Runtime{key: bytes.Repeat([]byte{7}, 32)}}
	for _, plain := range [][]byte{nil, []byte("00123\nsecret"), bytes.Repeat([]byte("x"), 1024*1024)} {
		cipher, err := s.runtime.cryptScope(plain, true, "datamesh-private-file-v1:resources/one")
		if err != nil {
			t.Fatal(err)
		}
		v := FileConfig{Encryption: privateFileMode, Key: "resources/one", Size: int64(len(plain)), SHA256: fileDigest(plain), CipherSize: int64(len(cipher)), CipherSHA256: fileDigest(cipher)}
		got, err := s.openPrivateFile(v, cipher, int64(len(plain)))
		if err != nil || !bytes.Equal(got, plain) {
			t.Fatal("roundtrip", err)
		}
		wrong := v
		wrong.Key = "resources/two"
		if _, err = s.openPrivateFile(wrong, cipher, int64(len(plain))); err == nil {
			t.Fatal("object swap accepted")
		}
		wrong = v
		wrong.SHA256 = fileDigest([]byte("different"))
		if _, err = s.openPrivateFile(wrong, cipher, int64(len(plain))); err == nil {
			t.Fatal("metadata tamper accepted")
		}
		altered := append([]byte(nil), cipher...)
		altered[len(altered)-1] ^= 1
		wrong = v
		wrong.CipherSHA256 = fileDigest(altered)
		if _, err = s.openPrivateFile(wrong, altered, int64(len(plain))); err == nil {
			t.Fatal("cipher tamper accepted")
		}
		old := s.runtime.key
		s.runtime.key = bytes.Repeat([]byte{8}, 32)
		if _, err = s.openPrivateFile(v, cipher, int64(len(plain))); err == nil {
			t.Fatal("wrong key accepted")
		}
		s.runtime.key = old
		if _, err = s.openPrivateFile(v, cipher, -1); err == nil {
			t.Fatal("negative limit accepted")
		}
		if len(plain) > 0 {
			if _, err = s.openPrivateFile(v, cipher, int64(len(plain)-1)); err == nil {
				t.Fatal("limit ignored")
			}
		}
	}
}
