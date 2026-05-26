package casmeta

import (
	"encoding/base64"
	"testing"
)

func TestEncodeDecodeSHA256Only(t *testing.T) {
	info := &Info{
		Name:   "movie.mkv",
		Size:   12345,
		SHA256: "df5e2a9e39493efcc9ab18f571d8a80516652b5295fc5a14c6fe5aba429365cc",
	}

	data, err := Encode(info)
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}

	got, err := Decode(data)
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}

	if got.Name != info.Name {
		t.Fatalf("Name = %q, want %q", got.Name, info.Name)
	}
	if got.Size != info.Size {
		t.Fatalf("Size = %d, want %d", got.Size, info.Size)
	}
	if got.SHA256 != info.SHA256 {
		t.Fatalf("SHA256 = %q, want %q", got.SHA256, info.SHA256)
	}
	if got.MD5 != "" {
		t.Fatalf("MD5 = %q, want empty", got.MD5)
	}
}

func TestDecodeKeepsLegacyMD5Payload(t *testing.T) {
	payload := []byte(`{"name":"movie.mkv","size":12345,"md5":"0123456789abcdef0123456789abcdef","sliceMd5":"abcdef0123456789abcdef0123456789","create_time":"1"}`)
	data := []byte(base64.StdEncoding.EncodeToString(payload))

	got, err := Decode(data)
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}

	if got.MD5 != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("MD5 = %q", got.MD5)
	}
	if got.SliceMD5 != "abcdef0123456789abcdef0123456789" {
		t.Fatalf("SliceMD5 = %q", got.SliceMD5)
	}
	if got.SHA256 != "" {
		t.Fatalf("SHA256 = %q, want empty", got.SHA256)
	}
}

func TestDecodeRejectsPayloadWithoutAnyHash(t *testing.T) {
	payload := []byte(`{"name":"movie.mkv","size":12345,"create_time":"1"}`)
	data := []byte(base64.StdEncoding.EncodeToString(payload))

	if _, err := Decode(data); err == nil {
		t.Fatal("Decode() error = nil, want invalid cas payload")
	}
}
