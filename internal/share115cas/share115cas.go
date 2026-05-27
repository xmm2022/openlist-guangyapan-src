package share115cas

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/casmeta"
)

const (
	Provider115 = "115"
	PreIDSize   = 128 * 1024
)

var sharePathRe = regexp.MustCompile(`/s/([^/?#]+)`)

func ParseShareURL(raw string) (shareCode string, receiveCode string, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", fmt.Errorf("empty share url")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", "", err
	}
	match := sharePathRe.FindStringSubmatch(parsed.Path)
	if len(match) != 2 {
		return "", "", fmt.Errorf("share code not found in %q", raw)
	}
	receive := parsed.Query().Get("password")
	if receive == "" {
		receive = parsed.Query().Get("receive_code")
	}
	if receive == "" {
		return "", "", fmt.Errorf("receive code not found in %q", raw)
	}
	return match[1], receive, nil
}

func EncodeCAS(name string, size int64, sha1Hex, preID string) ([]byte, error) {
	return casmeta.Encode(&casmeta.Info{
		Provider: Provider115,
		Name:     name,
		Size:     size,
		SHA1:     strings.ToUpper(strings.TrimSpace(sha1Hex)),
		PreID:    strings.ToUpper(strings.TrimSpace(preID)),
	})
}

func PreID(r io.Reader, size int64) (string, error) {
	limit := int64(PreIDSize)
	if size >= 0 && size < limit {
		limit = size
	}
	h := sha1.New()
	if _, err := io.CopyN(h, r, limit); err != nil && err != io.EOF {
		return "", err
	}
	return strings.ToUpper(hex.EncodeToString(h.Sum(nil))), nil
}

func OutputCASPath(root, relPath string) (string, error) {
	rel := strings.TrimSpace(filepath.ToSlash(relPath))
	if rel == "" {
		return "", fmt.Errorf("empty relative path")
	}
	clean := path.Clean("/" + rel)
	if clean == "/" {
		return "", fmt.Errorf("empty relative path")
	}
	clean = strings.TrimPrefix(clean, "/")
	if clean != rel {
		return "", fmt.Errorf("unsafe relative path %q", relPath)
	}
	return filepath.Join(root, filepath.FromSlash(clean+".cas")), nil
}
