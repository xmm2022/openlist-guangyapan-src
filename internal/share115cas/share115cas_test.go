package share115cas

import (
	"bytes"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseShareURL(t *testing.T) {
	code, receive, err := ParseShareURL("https://115.com/s/swz0yj53zhz?password=1158#")
	if err != nil {
		t.Fatalf("ParseShareURL() error = %v", err)
	}
	if code != "swz0yj53zhz" {
		t.Fatalf("code = %q", code)
	}
	if receive != "1158" {
		t.Fatalf("receive = %q", receive)
	}
}

func TestCASPayloadUses115Fields(t *testing.T) {
	content, err := EncodeCAS("movie.mkv", 12345, strings.Repeat("a", 40), strings.Repeat("b", 40))
	if err != nil {
		t.Fatalf("EncodeCAS() error = %v", err)
	}
	decoded, err := base64.StdEncoding.DecodeString(string(content))
	if err != nil {
		t.Fatalf("DecodeString() error = %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(decoded, &payload); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if payload["provider"] != "115" {
		t.Fatalf("provider = %v", payload["provider"])
	}
	if payload["sha1"] != strings.Repeat("A", 40) {
		t.Fatalf("sha1 = %v", payload["sha1"])
	}
	if payload["preID"] != strings.Repeat("B", 40) {
		t.Fatalf("preID = %v", payload["preID"])
	}
}

func TestPreIDHashesFirst128KiB(t *testing.T) {
	data := bytes.Repeat([]byte("x"), 200*1024)
	got, err := PreID(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("PreID() error = %v", err)
	}
	sum := sha1.Sum(data[:128*1024])
	want := strings.ToUpper(hex.EncodeToString(sum[:]))
	if got != want {
		t.Fatalf("PreID() = %q, want %q", got, want)
	}
}

func TestOutputCASPathRejectsTraversal(t *testing.T) {
	if _, err := OutputCASPath("/tmp/out", "../bad.mkv"); err == nil {
		t.Fatal("OutputCASPath() error = nil")
	}
}

func TestOutputCASPathAppendsSuffix(t *testing.T) {
	got, err := OutputCASPath("/tmp/out", "dir/movie.mkv")
	if err != nil {
		t.Fatalf("OutputCASPath() error = %v", err)
	}
	want := filepath.Join("/tmp/out", "dir", "movie.mkv.cas")
	if got != want {
		t.Fatalf("OutputCASPath() = %q, want %q", got, want)
	}
}
