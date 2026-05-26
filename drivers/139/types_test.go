package _139

import (
	"testing"

	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

func TestPersonalFileItemHashInfoUsesSHA256ContentHash(t *testing.T) {
	item := PersonalFileItem{
		ContentHash:          "df5e2a9e39493efcc9ab18f571d8a80516652b5295fc5a14c6fe5aba429365cc",
		ContentHashAlgorithm: "sha256",
	}

	got := item.HashInfo().GetHash(utils.SHA256)
	if got != item.ContentHash {
		t.Fatalf("SHA256 hash = %q, want %q", got, item.ContentHash)
	}
}

func TestPersonalFileItemHashInfoIgnoresNonSHA256(t *testing.T) {
	item := PersonalFileItem{
		ContentHash:          "0123456789abcdef0123456789abcdef",
		ContentHashAlgorithm: "md5",
	}

	if got := item.HashInfo().GetHash(utils.SHA256); got != "" {
		t.Fatalf("SHA256 hash = %q, want empty", got)
	}
}
