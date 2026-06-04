package _115

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	streampkg "github.com/OpenListTeam/OpenList/v4/internal/stream"
	driver115 "github.com/SheltonZhu/115driver/pkg/driver"
)

func TestRapidUploadFetchesUploadInfoBeforeSigning(t *testing.T) {
	var (
		uploadInfoCalls int
		uploadInitCalls int
		userkeyAtInit   string
		driver          *Pan115
	)

	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch {
		case req.URL.Host == "proapi.115.com" && req.URL.Path == "/app/uploadinfo":
			uploadInfoCalls++
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body: io.NopCloser(strings.NewReader(
					`{"state":true,"user_id":123,"userkey":"server-key","size_limit":1048576,"upload_allowed":true}`,
				)),
				Request: req,
			}, nil
		case req.URL.Host == "uplb.115.com" && req.URL.Path == "/4.0/initupload.php":
			uploadInitCalls++
			userkeyAtInit = driver.client.Userkey
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("not-encrypted")),
				Request:    req,
			}, nil
		default:
			t.Fatalf("unexpected request %s", req.URL.String())
			return nil, nil
		}
	})

	driver = &Pan115{
		client: driver115.New(driver115.WithClient(&http.Client{Transport: transport})),
	}
	driver.client.UserID = 123

	stream := &streampkg.FileStream{
		Ctx: context.Background(),
		Obj: &model.Object{Name: "movie.mkv", Size: 4},
		Reader: strings.NewReader("data"),
	}

	_, err := driver.rapidUpload(
		4,
		"movie.mkv",
		"99",
		strings.Repeat("A", 40),
		strings.Repeat("B", 40),
		stream,
	)
	if err == nil {
		t.Fatal("rapidUpload() error = nil, want decrypt failure after upload init")
	}
	if uploadInfoCalls != 1 {
		t.Fatalf("upload info calls = %d, want 1", uploadInfoCalls)
	}
	if uploadInitCalls != 1 {
		t.Fatalf("upload init calls = %d, want 1", uploadInitCalls)
	}
	if userkeyAtInit != "server-key" {
		t.Fatalf("userkey at upload init = %q, want server-key", userkeyAtInit)
	}
}
