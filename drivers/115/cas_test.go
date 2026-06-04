package _115

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/casmeta"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	driver115 "github.com/SheltonZhu/115driver/pkg/driver"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func TestValidate115CASInfoRequiresProviderSHA1AndPreID(t *testing.T) {
	driver := &Pan115{}

	tests := []struct {
		name    string
		info    *casUploadInfo
		wantErr string
	}{
		{
			name: "valid 115 cas",
			info: &casUploadInfo{
				Provider: casProvider115,
				Name:     "movie.mkv",
				Size:     1024,
				SHA1:     strings.Repeat("A", 40),
				PreID:    strings.Repeat("B", 40),
			},
		},
		{
			name: "reject foreign provider",
			info: &casUploadInfo{
				Provider: "139",
				Name:     "movie.mkv",
				Size:     1024,
				SHA1:     strings.Repeat("A", 40),
				PreID:    strings.Repeat("B", 40),
			},
			wantErr: "provider",
		},
		{
			name: "reject missing sha1",
			info: &casUploadInfo{
				Provider: casProvider115,
				Name:     "movie.mkv",
				Size:     1024,
				PreID:    strings.Repeat("B", 40),
			},
			wantErr: "sha1",
		},
		{
			name: "reject missing preID",
			info: &casUploadInfo{
				Provider: casProvider115,
				Name:     "movie.mkv",
				Size:     1024,
				SHA1:     strings.Repeat("A", 40),
			},
			wantErr: "preID",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := driver.validateCASInfo(tt.info)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validateCASInfo() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("validateCASInfo() error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestPan115CASPreviewNameHonorsAllowlist(t *testing.T) {
	driver := &Pan115{Addition: Addition{CASExtAllowlist: "mp4"}}
	info := &casmeta.Info{Name: "movie.mkv"}

	got, err := resolveCASRestoreName("movie.mkv.cas", info)
	if err != nil {
		t.Fatalf("resolveCASRestoreName() error = %v", err)
	}
	if casmeta.ExtAllowed(got, driver.CASExtAllowlist) {
		t.Fatalf("ExtAllowed(%q, %q) = true, want false", got, driver.CASExtAllowlist)
	}
}

func TestPan115CASFeatureSwitches(t *testing.T) {
	driver := &Pan115{Addition: Addition{
		GenerateCAS:        true,
		DeleteSource:       true,
		CASExtAllowlist:    "mkv",
		CASDownloadRestore: true,
	}}

	if !driver.shouldUploadCAS("movie.mkv") {
		t.Fatal("shouldUploadCAS(movie.mkv) = false, want true")
	}
	if driver.shouldUploadCAS("movie.mkv.cas") {
		t.Fatal("shouldUploadCAS(movie.mkv.cas) = true, want false")
	}
	if driver.shouldUploadCAS("movie.mp4") {
		t.Fatal("shouldUploadCAS(movie.mp4) = true, want false")
	}
	if !driver.shouldDeleteSource() {
		t.Fatal("shouldDeleteSource() = false, want true")
	}
	if !driver.CASDownloadRestoreEnabled() {
		t.Fatal("CASDownloadRestoreEnabled() = false, want true")
	}
}

func TestPan115ShouldPlayCASOnlyForCASVideoType(t *testing.T) {
	driver := &Pan115{}
	casObj := &model.Object{Name: "movie.mkv.cas"}
	rawObj := &model.Object{Name: "movie.mkv"}

	if !driver.shouldPlayCAS(casObj, model.LinkArgs{Type: "cas_video"}) {
		t.Fatal("shouldPlayCAS(.cas, cas_video) = false, want true")
	}
	if driver.shouldPlayCAS(casObj, model.LinkArgs{Type: "raw_cas"}) {
		t.Fatal("shouldPlayCAS(.cas, raw_cas) = true, want false")
	}
	if driver.shouldPlayCAS(casObj, model.LinkArgs{Type: "raw_file"}) {
		t.Fatal("shouldPlayCAS(.cas, raw_file) = true, want false")
	}
	if driver.shouldPlayCAS(rawObj, model.LinkArgs{Type: "cas_video"}) {
		t.Fatal("shouldPlayCAS(raw, cas_video) = true, want false")
	}
}

func TestCASRestoreStreamCannotSatisfySignCheckRanges(t *testing.T) {
	stream := &casRestoreStream{
		name: "movie.mkv",
		info: &casUploadInfo{
			Name:  "movie.mkv",
			Size:  1024,
			SHA1:  strings.Repeat("A", 40),
			PreID: strings.Repeat("B", 40),
		},
	}

	if _, err := stream.RangeRead(http_range.Range{Start: 10, Length: 20}); err == nil || !strings.Contains(err.Error(), "source bytes") {
		t.Fatalf("RangeRead() error = %v, want source bytes error", err)
	}
	if _, err := stream.CacheFullAndWriter(nil, io.Discard); err == nil || !strings.Contains(err.Error(), "no source bytes") {
		t.Fatalf("CacheFullAndWriter() error = %v, want no source bytes error", err)
	}
	if got := stream.GetHash().GetHash(utils.SHA1); got != strings.Repeat("A", 40) {
		t.Fatalf("GetHash(SHA1) = %q", got)
	}

	var _ model.FileStreamer = stream
}

func TestCASRestoreStreamRangeRead115ShareReusesReceivedPickCode(t *testing.T) {
	payload := []byte("0123456789")
	var ranges []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Test"); got != "present" {
			t.Fatalf("download request X-Test header = %q", got)
		}
		if got := r.Header.Get("User-Agent"); got != "unit-test-ua" {
			t.Fatalf("download request User-Agent = %q", got)
		}
		ranges = append(ranges, r.Header.Get("Range"))
		switch r.Header.Get("Range") {
		case "bytes=2-5":
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(payload[2:6])
		case "bytes=6-8":
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(payload[6:9])
		default:
			t.Fatalf("unexpected Range header %q", r.Header.Get("Range"))
		}
	}))
	defer server.Close()

	var (
		createTempCalls int
		receiveCalls    int
		waitCalls       int
		downloadCalls   int
	)
	stream := &casRestoreStream{
		name: "movie.mkv",
		info: &casUploadInfo{
			Name:  "movie.mkv",
			Size:  int64(len(payload)),
			SHA1:  strings.Repeat("A", 40),
			PreID: strings.Repeat("B", 40),
			Source: &casmeta.Source{
				Type:        "115_share",
				ShareCode:   "share",
				ReceiveCode: "code",
				FileID:      "file-id",
			},
		},
		ua:         "unit-test-ua",
		httpClient: server.Client(),
		createTempDirFn: func(context.Context) (string, string, error) {
			createTempCalls++
			return "temp-cid", "temp-name", nil
		},
		receiveShareFn: func(_ context.Context, source *casmeta.Source, tempCID string) error {
			receiveCalls++
			if source.FileID != "file-id" {
				t.Fatalf("receive source file id = %q", source.FileID)
			}
			if tempCID != "temp-cid" {
				t.Fatalf("receive tempCID = %q", tempCID)
			}
			return nil
		},
		waitForTransferredFn: func(context.Context, string) (casTransferredFile, error) {
			waitCalls++
			return casTransferredFile{
				PickCode: "pick-1",
				FileID:   "account-file-id",
				SHA1:     strings.Repeat("A", 40),
				Size:     int64(len(payload)),
			}, nil
		},
		downloadInfoFn: func(pickCode, ua string) (*driver115.DownloadInfo, error) {
			downloadCalls++
			if pickCode != "pick-1" {
				t.Fatalf("DownloadWithUA pickCode = %q", pickCode)
			}
			if ua != "unit-test-ua" {
				t.Fatalf("DownloadWithUA ua = %q", ua)
			}
			return &driver115.DownloadInfo{
				PickCode: pickCode,
				Url:      driver115.FileDownloadUrl{Url: server.URL},
				Header:   http.Header{"X-Test": {"present"}},
			}, nil
		},
	}

	first, err := stream.RangeRead(http_range.Range{Start: 2, Length: 4})
	if err != nil {
		t.Fatalf("first RangeRead() error = %v", err)
	}
	firstBody, err := io.ReadAll(first)
	if err != nil {
		t.Fatalf("ReadAll(first) error = %v", err)
	}
	if got := string(firstBody); got != "2345" {
		t.Fatalf("first RangeRead() body = %q", got)
	}

	second, err := stream.RangeRead(http_range.Range{Start: 6, Length: 3})
	if err != nil {
		t.Fatalf("second RangeRead() error = %v", err)
	}
	secondBody, err := io.ReadAll(second)
	if err != nil {
		t.Fatalf("ReadAll(second) error = %v", err)
	}
	if got := string(secondBody); got != "678" {
		t.Fatalf("second RangeRead() body = %q", got)
	}

	if createTempCalls != 1 {
		t.Fatalf("createTempCalls = %d, want 1", createTempCalls)
	}
	if receiveCalls != 1 {
		t.Fatalf("receiveCalls = %d, want 1", receiveCalls)
	}
	if waitCalls != 1 {
		t.Fatalf("waitCalls = %d, want 1", waitCalls)
	}
	if downloadCalls != 2 {
		t.Fatalf("downloadCalls = %d, want 2", downloadCalls)
	}
	if got := strings.Join(ranges, ","); got != "bytes=2-5,bytes=6-8" {
		t.Fatalf("download ranges = %q", got)
	}
}

func TestCASRestoreStreamRangeRead115ShareRejectsFullBody200ForPartialRange(t *testing.T) {
	payload := []byte("0123456789")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Range"); got != "bytes=2-5" {
			t.Fatalf("Range = %q", got)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	stream := &casRestoreStream{
		name: "movie.mkv",
		info: &casUploadInfo{
			Name:  "movie.mkv",
			Size:  int64(len(payload)),
			SHA1:  strings.Repeat("A", 40),
			PreID: strings.Repeat("B", 40),
			Source: &casmeta.Source{
				Type:        "115_share",
				ShareCode:   "share",
				ReceiveCode: "code",
				FileID:      "file-id",
			},
		},
		ua:         "unit-test-ua",
		httpClient: server.Client(),
		createTempDirFn: func(context.Context) (string, string, error) {
			return "temp-cid", "temp-name", nil
		},
		receiveShareFn: func(context.Context, *casmeta.Source, string) error {
			return nil
		},
		waitForTransferredFn: func(context.Context, string) (casTransferredFile, error) {
			return casTransferredFile{PickCode: "pick-1", FileID: "account-file-id"}, nil
		},
		downloadInfoFn: func(string, string) (*driver115.DownloadInfo, error) {
			return &driver115.DownloadInfo{
				PickCode: "pick-1",
				Url:      driver115.FileDownloadUrl{Url: server.URL},
			}, nil
		},
	}

	_, err := stream.RangeRead(http_range.Range{Start: 2, Length: 4})
	if err == nil || !strings.Contains(err.Error(), "HTTP 200") {
		t.Fatalf("RangeRead() error = %v, want HTTP 200 failure", err)
	}
}

func TestCASRestoreStreamRangeRead115ShareAllows200ForFullFile(t *testing.T) {
	payload := []byte("0123456789")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Range"); got != "bytes=0-9" {
			t.Fatalf("Range = %q", got)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	stream := &casRestoreStream{
		name: "movie.mkv",
		info: &casUploadInfo{
			Name:  "movie.mkv",
			Size:  int64(len(payload)),
			SHA1:  strings.Repeat("A", 40),
			PreID: strings.Repeat("B", 40),
			Source: &casmeta.Source{
				Type:        "115_share",
				ShareCode:   "share",
				ReceiveCode: "code",
				FileID:      "file-id",
			},
		},
		ua:         "unit-test-ua",
		httpClient: server.Client(),
		createTempDirFn: func(context.Context) (string, string, error) {
			return "temp-cid", "temp-name", nil
		},
		receiveShareFn: func(context.Context, *casmeta.Source, string) error {
			return nil
		},
		waitForTransferredFn: func(context.Context, string) (casTransferredFile, error) {
			return casTransferredFile{PickCode: "pick-1", FileID: "account-file-id"}, nil
		},
		downloadInfoFn: func(string, string) (*driver115.DownloadInfo, error) {
			return &driver115.DownloadInfo{
				PickCode: "pick-1",
				Url:      driver115.FileDownloadUrl{Url: server.URL},
			}, nil
		},
	}

	reader, err := stream.RangeRead(http_range.Range{Start: 0, Length: int64(len(payload))})
	if err != nil {
		t.Fatalf("RangeRead() error = %v", err)
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if got := string(body); got != string(payload) {
		t.Fatalf("body = %q", got)
	}
}

func TestCASRestoreStreamRangeRead115ShareSanitizesDownloadRequestErrors(t *testing.T) {
	signedURL := "https://download.example.invalid/file?sign=secret-token&pickcode=abc123"
	stream := &casRestoreStream{
		name: "movie.mkv",
		info: &casUploadInfo{
			Name:  "movie.mkv",
			Size:  10,
			SHA1:  strings.Repeat("A", 40),
			PreID: strings.Repeat("B", 40),
			Source: &casmeta.Source{
				Type:        "115_share",
				ShareCode:   "share",
				ReceiveCode: "code",
				FileID:      "file-id",
			},
		},
		ua: "unit-test-ua",
		httpClient: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				return nil, &url.Error{
					Op:  "Get",
					URL: req.URL.String(),
					Err: errors.New("dial failed for " + req.URL.String()),
				}
			}),
		},
		createTempDirFn: func(context.Context) (string, string, error) {
			return "temp-cid", "temp-name", nil
		},
		receiveShareFn: func(context.Context, *casmeta.Source, string) error {
			return nil
		},
		waitForTransferredFn: func(context.Context, string) (casTransferredFile, error) {
			return casTransferredFile{PickCode: "pick-1", FileID: "account-file-id"}, nil
		},
		downloadInfoFn: func(string, string) (*driver115.DownloadInfo, error) {
			return &driver115.DownloadInfo{
				PickCode: "pick-1",
				Url:      driver115.FileDownloadUrl{Url: signedURL},
			}, nil
		},
	}

	_, err := stream.RangeRead(http_range.Range{Start: 0, Length: 10})
	if err == nil {
		t.Fatal("RangeRead() error = nil")
	}
	if strings.Contains(err.Error(), signedURL) {
		t.Fatalf("RangeRead() leaked signed URL in error: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "download Get failed") {
		t.Fatalf("RangeRead() error = %q, want sanitized operation context", err.Error())
	}
}

func TestCASRestoreStreamReceiveSharePostsExpectedRequest(t *testing.T) {
	oldBase := shareWebAPIBase
	defer func() { shareWebAPIBase = oldBase }()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/share/receive" {
			t.Fatalf("path = %s, want /share/receive", r.URL.Path)
		}
		if got := r.Header.Get("Cookie"); got != "UID=u; CID=c" {
			t.Fatalf("Cookie = %q", got)
		}
		if got := r.Header.Get("User-Agent"); got != "unit-test-ua" {
			t.Fatalf("User-Agent = %q", got)
		}
		if got := r.Header.Get("Referer"); got != "https://115.com/s/share-code?password=receive-code" {
			t.Fatalf("Referer = %q", got)
		}
		if got := r.Header.Get("Content-Type"); !strings.Contains(got, "application/x-www-form-urlencoded") {
			t.Fatalf("Content-Type = %q", got)
		}
		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("ReadAll(body) error = %v", err)
		}
		values, err := url.ParseQuery(string(bodyBytes))
		if err != nil {
			t.Fatalf("ParseQuery(body) error = %v", err)
		}
		want := map[string]string{
			"share_code":   "share-code",
			"receive_code": "receive-code",
			"file_id":      "file-123",
			"cid":          "temp-cid",
			"is_check":     "0",
		}
		for key, expected := range want {
			if got := values.Get(key); got != expected {
				t.Fatalf("%s = %q, want %q", key, got, expected)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"state":true}`))
	}))
	defer server.Close()
	shareWebAPIBase = server.URL

	stream := &casRestoreStream{
		ua:         "unit-test-ua",
		cookie:     "UID=u; CID=c",
		httpClient: server.Client(),
	}

	err := stream.receiveShare(context.Background(), &casmeta.Source{
		ShareCode:   "share-code",
		ReceiveCode: "receive-code",
		FileID:      "file-123",
	}, "temp-cid")
	if err != nil {
		t.Fatalf("receiveShare() error = %v", err)
	}
}

func TestCASRestoreStreamReceiveShareReturnsDecodedError(t *testing.T) {
	oldBase := shareWebAPIBase
	defer func() { shareWebAPIBase = oldBase }()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"state":false,"error":"receive denied","errno":401}`))
	}))
	defer server.Close()
	shareWebAPIBase = server.URL

	stream := &casRestoreStream{
		ua:         "unit-test-ua",
		cookie:     "UID=u",
		httpClient: server.Client(),
	}

	err := stream.receiveShare(context.Background(), &casmeta.Source{
		ShareCode:   "share-code",
		ReceiveCode: "receive-code",
		FileID:      "file-123",
	}, "temp-cid")
	if err == nil || !strings.Contains(err.Error(), "receive denied") || !strings.Contains(err.Error(), "errno=401") {
		t.Fatalf("receiveShare() error = %v, want decoded share error", err)
	}
}

func TestCASRestoreStreamWaitForTransferredPollsAndMatchesBySHA1AndSize(t *testing.T) {
	wantSHA1 := strings.Repeat("A", 40)
	rootID := "root"
	dirID := "nested"
	callLog := make([]string, 0, 4)
	listCalls := 0

	stream := &casRestoreStream{
		info: &casUploadInfo{
			SHA1: wantSHA1,
			Size: 12,
		},
		transferWait:  50 * time.Millisecond,
		sharePollWait: 1 * time.Millisecond,
		listDirFn: func(_ context.Context, dir string) ([]driver115.File, error) {
			listCalls++
			callLog = append(callLog, dir)
			switch listCalls {
			case 1:
				if dir != rootID {
					t.Fatalf("list call 1 dir = %q", dir)
				}
				return []driver115.File{
					{FileID: dirID, IsDirectory: true, Name: "nested"},
					{FileID: "wrong-size", Name: "wrong-size.mkv", Size: 9, Sha1: wantSHA1, PickCode: "pc1"},
					{FileID: "wrong-sha", Name: "wrong-sha.mkv", Size: 12, Sha1: strings.Repeat("B", 40), PickCode: "pc2"},
				}, nil
			case 2:
				if dir != dirID {
					t.Fatalf("list call 2 dir = %q", dir)
				}
				return nil, nil
			case 3:
				if dir != rootID {
					t.Fatalf("list call 3 dir = %q", dir)
				}
				return []driver115.File{
					{FileID: dirID, IsDirectory: true, Name: "nested"},
				}, nil
			case 4:
				if dir != dirID {
					t.Fatalf("list call 4 dir = %q", dir)
				}
				return []driver115.File{
					{FileID: "match-file", Name: "movie.mkv", Size: 12, Sha1: strings.ToLower(wantSHA1)},
				}, nil
			default:
				t.Fatalf("unexpected list call %d for dir %q", listCalls, dir)
				return nil, nil
			}
		},
		getFileByIDFn: func(fileID string) (*FileObj, error) {
			if fileID != "match-file" {
				t.Fatalf("getFileByID(fileID) = %q", fileID)
			}
			return &FileObj{File: driver115.File{
				FileID:   fileID,
				Name:     "movie.mkv",
				Size:     12,
				Sha1:     wantSHA1,
				PickCode: "pick-123",
			}}, nil
		},
	}

	got, err := stream.waitForTransferred(context.Background(), rootID)
	if err != nil {
		t.Fatalf("waitForTransferred() error = %v", err)
	}
	if got.PickCode != "pick-123" {
		t.Fatalf("PickCode = %q, want pick-123", got.PickCode)
	}
	if got.FileID != "match-file" {
		t.Fatalf("FileID = %q, want match-file", got.FileID)
	}
	if !slices.Equal(callLog, []string{rootID, dirID, rootID, dirID}) {
		t.Fatalf("callLog = %#v", callLog)
	}
}

func TestCASRestoreStreamCleanupDeletesAndOptionallyPurgesRecycle(t *testing.T) {
	t.Run("delete only with nil pan does not panic", func(t *testing.T) {
		deleteCalls := 0
		waitRecycleCalls := 0
		cleanRecycleCalls := 0

		stream := &casRestoreStream{
			tempCID:  "temp-cid",
			tempName: "temp-name",
			deleteFn: func(_ context.Context, tempCID string) error {
				deleteCalls++
				if tempCID != "temp-cid" {
					t.Fatalf("delete tempCID = %q", tempCID)
				}
				return nil
			},
			waitForRecycleIDsFn: func(context.Context, *Pan115, string, string, time.Duration) ([]string, error) {
				waitRecycleCalls++
				return []string{"unexpected"}, nil
			},
			cleanRecycleFn: func(context.Context, *Pan115, string, []string, time.Duration) error {
				cleanRecycleCalls++
				return nil
			},
		}

		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("cleanup() panicked: %v", r)
			}
		}()
		err := stream.cleanup(context.Background())
		if err == nil || !strings.Contains(err.Error(), "cas_recycle_password") {
			t.Fatalf("cleanup() error = %v, want missing cas_recycle_password", err)
		}
		if deleteCalls != 1 {
			t.Fatalf("deleteCalls = %d, want 1", deleteCalls)
		}
		if waitRecycleCalls != 0 {
			t.Fatalf("waitRecycleCalls = %d, want 0", waitRecycleCalls)
		}
		if cleanRecycleCalls != 0 {
			t.Fatalf("cleanRecycleCalls = %d, want 0", cleanRecycleCalls)
		}
	})

	t.Run("delete only without recycle password", func(t *testing.T) {
		deleteCalls := 0
		waitRecycleCalls := 0
		cleanRecycleCalls := 0

		stream := &casRestoreStream{
			pan:      &Pan115{},
			tempCID:  "temp-cid",
			tempName: "temp-name",
			deleteFn: func(_ context.Context, tempCID string) error {
				deleteCalls++
				if tempCID != "temp-cid" {
					t.Fatalf("delete tempCID = %q", tempCID)
				}
				return nil
			},
			waitForRecycleIDsFn: func(context.Context, *Pan115, string, string, time.Duration) ([]string, error) {
				waitRecycleCalls++
				return []string{"recycle-id"}, nil
			},
			cleanRecycleFn: func(context.Context, *Pan115, string, []string, time.Duration) error {
				cleanRecycleCalls++
				return nil
			},
		}

		err := stream.cleanup(context.Background())
		if err == nil || !strings.Contains(err.Error(), "cas_recycle_password") {
			t.Fatalf("cleanup() error = %v, want missing cas_recycle_password", err)
		}
		if deleteCalls != 1 {
			t.Fatalf("deleteCalls = %d, want 1", deleteCalls)
		}
		if waitRecycleCalls != 0 {
			t.Fatalf("waitRecycleCalls = %d, want 0", waitRecycleCalls)
		}
		if cleanRecycleCalls != 0 {
			t.Fatalf("cleanRecycleCalls = %d, want 0", cleanRecycleCalls)
		}
	})

	t.Run("delete and purge with recycle password", func(t *testing.T) {
		deleteCalls := 0
		waitRecycleCalls := 0
		cleanRecycleCalls := 0

		stream := &casRestoreStream{
			pan:             &Pan115{Addition: Addition{CASRecyclePassword: "secret"}},
			tempCID:         "temp-cid",
			tempName:        "temp-name",
			recycleListWait: 12 * time.Second,
			cleanupWait:     34 * time.Second,
			deleteFn: func(_ context.Context, tempCID string) error {
				deleteCalls++
				if tempCID != "temp-cid" {
					t.Fatalf("delete tempCID = %q", tempCID)
				}
				return nil
			},
			waitForRecycleIDsFn: func(_ context.Context, pan *Pan115, tempCID, tempName string, maxWait time.Duration) ([]string, error) {
				waitRecycleCalls++
				if pan.CASRecyclePassword != "secret" {
					t.Fatalf("CASRecyclePassword = %q", pan.CASRecyclePassword)
				}
				if tempCID != "temp-cid" || tempName != "temp-name" {
					t.Fatalf("waitForRecycleIDs(tempCID=%q, tempName=%q)", tempCID, tempName)
				}
				if maxWait != 12*time.Second {
					t.Fatalf("waitForRecycleIDs maxWait = %s", maxWait)
				}
				return []string{"recycle-1", "recycle-2"}, nil
			},
			cleanRecycleFn: func(_ context.Context, pan *Pan115, password string, recycleIDs []string, maxWait time.Duration) error {
				cleanRecycleCalls++
				if pan.CASRecyclePassword != "secret" {
					t.Fatalf("CASRecyclePassword = %q", pan.CASRecyclePassword)
				}
				if password != "secret" {
					t.Fatalf("password = %q", password)
				}
				if !slices.Equal(recycleIDs, []string{"recycle-1", "recycle-2"}) {
					t.Fatalf("recycleIDs = %#v", recycleIDs)
				}
				if maxWait != 34*time.Second {
					t.Fatalf("cleanRecycle maxWait = %s", maxWait)
				}
				return nil
			},
		}

		if err := stream.cleanup(context.Background()); err != nil {
			t.Fatalf("cleanup() error = %v", err)
		}
		if deleteCalls != 1 {
			t.Fatalf("deleteCalls = %d, want 1", deleteCalls)
		}
		if waitRecycleCalls != 1 {
			t.Fatalf("waitRecycleCalls = %d, want 1", waitRecycleCalls)
		}
		if cleanRecycleCalls != 1 {
			t.Fatalf("cleanRecycleCalls = %d, want 1", cleanRecycleCalls)
		}
	})
}

func TestCombineRestoreAndCleanupError(t *testing.T) {
	restoreErr := errors.New("restore failed")
	cleanupErr := errors.New("cleanup failed")

	if err := combineRestoreAndCleanupError(nil, cleanupErr); err != nil {
		t.Fatalf("combineRestoreAndCleanupError(nil, cleanupErr) = %v, want nil", err)
	}

	err := combineRestoreAndCleanupError(restoreErr, cleanupErr)
	if err == nil {
		t.Fatal("combineRestoreAndCleanupError(restoreErr, cleanupErr) = nil")
	}
	if !strings.Contains(err.Error(), "restore failed") {
		t.Fatalf("combined error = %v, want restore context", err)
	}
	if !strings.Contains(err.Error(), "cleanup failed") {
		t.Fatalf("combined error = %v, want cleanup context", err)
	}
}
