package main

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	driver115 "github.com/SheltonZhu/115driver/pkg/driver"
)

func TestDecodeShareSnapAcceptsWebFields(t *testing.T) {
	body := []byte(`{
		"state": true,
		"data": {
			"userinfo": {"user_id": "90001347"},
			"count": 2,
			"list": [
				{"cid": "dir1", "pid": "0", "n": "folder", "s": 123, "fc": 0},
				{"fid": "file1", "sc": "share-file1", "n": "movie.mkv", "s": 456, "sha": "abcdef", "fc": 1}
			]
		}
	}`)

	snap, err := decodeShareSnap(body)
	if err != nil {
		t.Fatalf("decodeShareSnap() error = %v", err)
	}
	if snap.UserID != "90001347" {
		t.Fatalf("UserID = %q", snap.UserID)
	}
	if snap.Count != 2 {
		t.Fatalf("Count = %d", snap.Count)
	}
	if got := snap.List[0].DirID(); got != "dir1" {
		t.Fatalf("DirID() = %q", got)
	}
	if got := snap.List[1].DownloadID(); got != "share-file1" {
		t.Fatalf("DownloadID() = %q", got)
	}
	if !snap.List[1].IsRegularFile() {
		t.Fatal("IsRegularFile() = false")
	}
	if got := snap.List[1].SHA1(); got != "abcdef" {
		t.Fatalf("SHA1() = %q", got)
	}
}

func TestDecodeShareSnapAcceptsNumericIDs(t *testing.T) {
	body := []byte(`{
		"state": true,
		"data": {
			"count": 2,
			"list": [
				{"cid": 1234567890123456789, "n": "folder", "fc": 0},
				{"fid": 987654321012345678, "sc": 876543210123456789, "n": "movie.mkv", "s": "456", "sha": "abcdef", "fc": 1}
			]
		}
	}`)

	snap, err := decodeShareSnap(body)
	if err != nil {
		t.Fatalf("decodeShareSnap() error = %v", err)
	}
	if got := snap.List[0].DirID(); got != "1234567890123456789" {
		t.Fatalf("DirID() = %q", got)
	}
	if got := snap.List[1].DownloadID(); got != "876543210123456789" {
		t.Fatalf("DownloadID() = %q", got)
	}
	if got := snap.List[1].ReceiveID(); got != "987654321012345678" {
		t.Fatalf("ReceiveID() = %q", got)
	}
}

func TestDecodeShareDownloadURLAcceptsNestedURL(t *testing.T) {
	body := []byte(`{"state": true, "data": {"url": {"url": "https://download.example/file"}}}`)

	got, err := decodeShareDownloadURL(body)
	if err != nil {
		t.Fatalf("decodeShareDownloadURL() error = %v", err)
	}
	if got != "https://download.example/file" {
		t.Fatalf("decodeShareDownloadURL() = %q", got)
	}
}

func TestDecodeShareDownloadURLAccepts302URL(t *testing.T) {
	body := []byte(`{"state": true, "data": {"file_url_302": "https://download.example/302"}}`)

	got, err := decodeShareDownloadURL(body)
	if err != nil {
		t.Fatalf("decodeShareDownloadURL() error = %v", err)
	}
	if got != "https://download.example/302" {
		t.Fatalf("decodeShareDownloadURL() = %q", got)
	}
}

func TestDecodeShareDownloadURLRejectsAPIError(t *testing.T) {
	body := []byte(`{"state": false, "error": "denied", "msg": "bad code"}`)

	if _, err := decodeShareDownloadURL(body); err == nil {
		t.Fatal("decodeShareDownloadURL() error = nil")
	}
}

func TestSummarizeHTTPErrorBodyTruncatesHTML(t *testing.T) {
	body := strings.Repeat("<html>blocked</html>", 100)

	got := summarizeHTTPErrorBody(body)
	if len(got) > 240 {
		t.Fatalf("summarizeHTTPErrorBody() length = %d", len(got))
	}
	if !strings.Contains(got, "blocked") {
		t.Fatalf("summarizeHTTPErrorBody() = %q", got)
	}
}

func TestPopDirJobUsesDepthFirstOrder(t *testing.T) {
	stack := []dirJob{
		{id: "a", path: "A"},
		{id: "b", path: "B"},
	}

	job, rest, ok := popDirJob(stack)
	if !ok {
		t.Fatal("popDirJob() ok = false")
	}
	if job.id != "b" || job.path != "B" {
		t.Fatalf("popDirJob() job = %#v", job)
	}
	if len(rest) != 1 || rest[0].id != "a" {
		t.Fatalf("popDirJob() rest = %#v", rest)
	}
}

func TestShouldAbortListErrorsHonorsLimit(t *testing.T) {
	if shouldAbortListErrors(2, 3) {
		t.Fatal("shouldAbortListErrors(2, 3) = true")
	}
	if !shouldAbortListErrors(3, 3) {
		t.Fatal("shouldAbortListErrors(3, 3) = false")
	}
	if shouldAbortListErrors(100, 0) {
		t.Fatal("shouldAbortListErrors(100, 0) = true")
	}
}

func TestShouldRetryHTTPStatus(t *testing.T) {
	retryStatuses := []int{405, 429, 500, 502, 503, 504}
	for _, status := range retryStatuses {
		if !shouldRetryHTTPStatus(status) {
			t.Fatalf("shouldRetryHTTPStatus(%d) = false", status)
		}
	}
	if shouldRetryHTTPStatus(404) {
		t.Fatal("shouldRetryHTTPStatus(404) = true")
	}
}

func TestBuildTransferBatchesHonorsMaxBytes(t *testing.T) {
	files := []shareFileEntry{
		{Path: "a.mkv", Size: 60, ShareFileID: "a"},
		{Path: "b.mkv", Size: 40, ShareFileID: "b"},
		{Path: "c.mkv", Size: 80, ShareFileID: "c"},
		{Path: "d.mkv", Size: 10, ShareFileID: "d"},
	}

	batches, err := buildTransferBatches(files, 100)
	if err != nil {
		t.Fatalf("buildTransferBatches() error = %v", err)
	}
	if len(batches) != 2 {
		t.Fatalf("len(batches) = %d", len(batches))
	}
	if batches[0].Size != 100 || len(batches[0].Files) != 2 {
		t.Fatalf("batch 0 = %#v", batches[0])
	}
	if batches[1].Size != 90 || len(batches[1].Files) != 2 {
		t.Fatalf("batch 1 = %#v", batches[1])
	}
	if batches[1].Files[0].Path != "c.mkv" {
		t.Fatalf("batch order changed: %#v", batches[1].Files)
	}
}

func TestBuildTransferBatchesRejectsSingleFileTooLarge(t *testing.T) {
	files := []shareFileEntry{{Path: "huge.mkv", Size: 101, ShareFileID: "huge"}}

	if _, err := buildTransferBatches(files, 100); err == nil {
		t.Fatal("buildTransferBatches() error = nil")
	}
}

func TestParseByteSizeSupportsBinaryUnits(t *testing.T) {
	got, err := parseByteSize("1.5TiB")
	if err != nil {
		t.Fatalf("parseByteSize() error = %v", err)
	}
	want := int64(1536 * 1024 * 1024 * 1024)
	if got != want {
		t.Fatalf("parseByteSize() = %d, want %d", got, want)
	}
}

func TestDecodeShareReceiveRejectsAPIError(t *testing.T) {
	body := []byte(`{"state": false, "error": "capacity limit", "errno": 123}`)

	if err := decodeShareReceive(body); err == nil {
		t.Fatal("decodeShareReceive() error = nil")
	}
}

func TestProcessTransferredFileUsesCachedPreID(t *testing.T) {
	outDir := t.TempDir()
	preID := strings.Repeat("B", 40)
	file := shareFileEntry{
		Path:        "dir/movie.mkv",
		Name:        "movie.mkv",
		Size:        123,
		SHA1:        strings.Repeat("A", 40),
		ShareFileID: "share-file",
	}
	opts := transferOptions{
		outDir:     outDir,
		preIDCache: map[string]string{transferMatchKey(file.SHA1, file.Size): preID},
	}

	record := processTransferredFile(t.Context(), opts, file, panFileEntry{PickCode: "pickcode"})
	if record.Status != "ok" {
		t.Fatalf("Status = %q, Error = %q", record.Status, record.Error)
	}
	if record.PreID != preID {
		t.Fatalf("PreID = %q", record.PreID)
	}
	content, err := os.ReadFile(record.CASPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	decoded, err := base64.StdEncoding.DecodeString(string(content))
	if err != nil {
		t.Fatalf("DecodeString() error = %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(decoded, &payload); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if payload["preID"] != preID {
		t.Fatalf("payload preID = %v", payload["preID"])
	}
}

func TestFindRecycleIDsForTempDirMatchesIDOrName(t *testing.T) {
	items := []driver115.RecycleBinItem{
		{FileId: "unrelated-id", FileName: "old-temp"},
		{FileId: "temp-cid", FileName: "renamed-in-recycle"},
		{FileId: "another-id", FileName: "openlist-share2cas-temp-20260528-120000-001"},
	}

	got := findRecycleIDsForTempDir(items, "temp-cid", "openlist-share2cas-temp-20260528-120000-001")
	if strings.Join(got, ",") != "temp-cid,another-id" {
		t.Fatalf("findRecycleIDsForTempDir() = %#v", got)
	}
}

func TestTransferCleanupRequiresRecyclePassword(t *testing.T) {
	opts := transferOptions{keepTemp: false}

	if err := validateRecycleCleanupOptions(opts); err == nil {
		t.Fatal("validateRecycleCleanupOptions() error = nil")
	}
	opts.recyclePassword = "secret"
	if err := validateRecycleCleanupOptions(opts); err != nil {
		t.Fatalf("validateRecycleCleanupOptions() error = %v", err)
	}
	opts.recyclePassword = ""
	opts.keepTemp = true
	if err := validateRecycleCleanupOptions(opts); err != nil {
		t.Fatalf("validateRecycleCleanupOptions() with keepTemp error = %v", err)
	}
}

func TestShouldLogListProgress(t *testing.T) {
	start := time.Unix(100, 0)

	if !shouldLogListProgress(1, start.Add(time.Second), start, time.Minute) {
		t.Fatal("first listed directory should log progress")
	}
	if !shouldLogListProgress(100, start.Add(time.Second), start, time.Minute) {
		t.Fatal("100th listed directory should log progress")
	}
	if !shouldLogListProgress(2, start.Add(time.Minute), start, time.Minute) {
		t.Fatal("elapsed interval should log progress")
	}
	if shouldLogListProgress(2, start.Add(time.Second), start, time.Minute) {
		t.Fatal("early non-boundary directory should not log progress")
	}
}
