package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/share115cas"
	driver115 "github.com/SheltonZhu/115driver/pkg/driver"
)

type shareFileEntry struct {
	Path        string
	Name        string
	Size        int64
	SHA1        string
	ShareFileID string
}

type transferBatch struct {
	Files []shareFileEntry
	Size  int64
}

type panFileEntry struct {
	Path     string
	Name     string
	Size     int64
	SHA1     string
	PickCode string
	FileID   string
}

const listProgressInterval = 30 * time.Second

type transferOptions struct {
	shareClient      *shareWebClient
	panClient        *driver115.Pan115Client
	manifest         *json.Encoder
	shareCode        string
	receiveCode      string
	outDir           string
	manifestPath     string
	pageSize         int
	limit            int
	maxListErrs      int
	overwrite        bool
	maxBatchBytes    int64
	tempParentCID    string
	tempNamePrefix   string
	keepTemp         bool
	recyclePassword  string
	recycleWait      time.Duration
	receiveChunkSize int
	transferWait     time.Duration
	userAgent        string
	preIDCache       map[string]string
}

func runTransferBatchMode(ctx context.Context, opts transferOptions) error {
	if opts.shareClient == nil {
		return fmt.Errorf("missing share client")
	}
	if opts.panClient == nil {
		return fmt.Errorf("missing 115 client")
	}
	if opts.manifest == nil {
		return fmt.Errorf("missing manifest encoder")
	}
	if opts.maxBatchBytes <= 0 {
		return fmt.Errorf("batch size must be greater than 0")
	}
	if opts.receiveChunkSize <= 0 {
		opts.receiveChunkSize = 100
	}
	if strings.TrimSpace(opts.tempParentCID) == "" {
		opts.tempParentCID = "0"
	}
	if strings.TrimSpace(opts.tempNamePrefix) == "" {
		opts.tempNamePrefix = "openlist-share2cas-temp"
	}
	if opts.preIDCache == nil {
		opts.preIDCache = map[string]string{}
	}
	if err := validateRecycleCleanupOptions(opts); err != nil {
		return err
	}

	files, err := collectShareFiles(ctx, opts)
	if err != nil {
		return err
	}
	candidates, skipped, err := prepareTransferCandidates(opts, files)
	if err != nil {
		return err
	}
	for _, record := range skipped {
		if err := opts.manifest.Encode(record); err != nil {
			return fmt.Errorf("write manifest: %w", err)
		}
		logManifestRecord(record)
	}
	batches, err := buildTransferBatches(candidates, opts.maxBatchBytes)
	if err != nil {
		return err
	}

	processed := len(skipped)
	log.Printf("collected=%d transfer_candidates=%d batches=%d batch_size=%d", len(files), len(candidates), len(batches), opts.maxBatchBytes)
	for i, batch := range batches {
		tempName := makeTempBatchName(opts.tempNamePrefix, i+1)
		tempCID, err := opts.panClient.Mkdir(opts.tempParentCID, tempName)
		if err != nil {
			return fmt.Errorf("create temp dir %q: %w", tempName, err)
		}
		log.Printf("batch %d/%d: files=%d size=%d temp_cid=%s", i+1, len(batches), len(batch.Files), batch.Size, tempCID)

		if err := receiveShareBatch(ctx, opts, batch, tempCID); err != nil {
			return fmt.Errorf("receive batch %d into temp dir %s: %w; temp dir kept for inspection", i+1, tempCID, err)
		}
		matches, err := waitForTransferredMatches(ctx, opts.panClient, batch.Files, tempCID, opts.transferWait)
		if err != nil {
			return fmt.Errorf("wait transferred files for batch %d temp dir %s: %w; temp dir kept for inspection", i+1, tempCID, err)
		}

		for _, file := range batch.Files {
			record := processTransferredFile(ctx, opts, file, matches[transferMatchKey(file.SHA1, file.Size)])
			if err := opts.manifest.Encode(record); err != nil {
				return fmt.Errorf("write manifest: %w", err)
			}
			processed++
			logManifestRecord(record)
		}

		if opts.keepTemp {
			log.Printf("batch %d/%d: temp dir kept cid=%s", i+1, len(batches), tempCID)
			continue
		}
		if err := cleanupTempDir(ctx, opts, tempCID, tempName); err != nil {
			return fmt.Errorf("cleanup temp dir %s (%s): %w", tempName, tempCID, err)
		}
		log.Printf("batch %d/%d: temp dir permanently cleaned", i+1, len(batches))
	}
	log.Printf("done: %d files processed, manifest=%s, out=%s", processed, opts.manifestPath, opts.outDir)
	return nil
}

func validateRecycleCleanupOptions(opts transferOptions) error {
	if opts.keepTemp {
		return nil
	}
	if strings.TrimSpace(opts.recyclePassword) == "" {
		return fmt.Errorf("transfer-batch with --keep-temp=false requires --recycle-password-file so deleted temp files are permanently removed from recycle bin")
	}
	return nil
}

func collectShareFiles(ctx context.Context, opts transferOptions) ([]shareFileEntry, error) {
	queue := []dirJob{{id: "", path: ""}}
	var files []shareFileEntry
	consecutiveListErrors := 0
	listedDirs := 0
	lastProgress := time.Time{}
	for len(queue) > 0 {
		job, rest, ok := popDirJob(queue)
		if !ok {
			break
		}
		queue = rest

		items, err := listShareDir(ctx, opts.shareClient, opts.shareCode, opts.receiveCode, job.id, opts.pageSize)
		if err != nil {
			consecutiveListErrors++
			record := manifestRecord{
				Path:   job.path,
				Status: "list_error",
				Error:  err.Error(),
			}
			if opts.manifest != nil {
				if err := opts.manifest.Encode(record); err != nil {
					return nil, fmt.Errorf("write manifest: %w", err)
				}
			}
			log.Printf("list_error %q: %v", job.path, err)
			if shouldAbortListErrors(consecutiveListErrors, opts.maxListErrs) {
				return nil, fmt.Errorf("abort after %d consecutive list errors", consecutiveListErrors)
			}
			continue
		}
		consecutiveListErrors = 0
		listedDirs++
		now := time.Now()
		if shouldLogListProgress(listedDirs, now, lastProgress, listProgressInterval) {
			log.Printf("list_progress dirs=%d files=%d queue=%d current=%q", listedDirs, len(files), len(queue), job.path)
			lastProgress = now
		}
		for _, item := range items {
			relPath := joinSharePath(job.path, item.FileName)
			if !item.IsRegularFile() {
				queue = append(queue, dirJob{id: item.DirID(), path: relPath})
				continue
			}
			if opts.limit > 0 && len(files) >= opts.limit {
				log.Printf("limit reached while listing: %d files", opts.limit)
				return files, nil
			}
			files = append(files, shareFileEntry{
				Path:        relPath,
				Name:        item.FileName,
				Size:        item.Size(),
				SHA1:        strings.ToUpper(strings.TrimSpace(item.SHA1())),
				ShareFileID: item.ReceiveID(),
			})
		}
	}
	return files, nil
}

func shouldLogListProgress(listedDirs int, now, last time.Time, interval time.Duration) bool {
	if listedDirs <= 1 || listedDirs%100 == 0 {
		return true
	}
	return interval > 0 && (last.IsZero() || now.Sub(last) >= interval)
}

func prepareTransferCandidates(opts transferOptions, files []shareFileEntry) ([]shareFileEntry, []manifestRecord, error) {
	candidates := make([]shareFileEntry, 0, len(files))
	skipped := make([]manifestRecord, 0)
	for _, file := range files {
		record := manifestRecord{
			Path:   file.Path,
			Name:   file.Name,
			Size:   file.Size,
			SHA1:   file.SHA1,
			FileID: file.ShareFileID,
		}
		casPath, err := share115cas.OutputCASPath(opts.outDir, file.Path)
		if err != nil {
			record.Status = "error"
			record.Error = err.Error()
			skipped = append(skipped, record)
			continue
		}
		record.CASPath = casPath
		if !opts.overwrite {
			if _, err := os.Stat(casPath); err == nil {
				record.Status = "exists"
				skipped = append(skipped, record)
				continue
			}
		}
		if strings.TrimSpace(file.SHA1) == "" {
			record.Status = "error"
			record.Error = "missing sha1"
			skipped = append(skipped, record)
			continue
		}
		if strings.TrimSpace(file.ShareFileID) == "" {
			record.Status = "error"
			record.Error = "missing share file id"
			skipped = append(skipped, record)
			continue
		}
		if file.Size < 0 {
			record.Status = "error"
			record.Error = "negative file size"
			skipped = append(skipped, record)
			continue
		}
		candidates = append(candidates, file)
	}
	return candidates, skipped, nil
}

func buildTransferBatches(files []shareFileEntry, maxBytes int64) ([]transferBatch, error) {
	if maxBytes <= 0 {
		return nil, fmt.Errorf("batch size must be greater than 0")
	}
	var batches []transferBatch
	current := transferBatch{}
	flush := func() {
		if len(current.Files) == 0 {
			return
		}
		batches = append(batches, current)
		current = transferBatch{}
	}
	for _, file := range files {
		if file.Size > maxBytes {
			return nil, fmt.Errorf("file %q size %d exceeds batch size %d", file.Path, file.Size, maxBytes)
		}
		if len(current.Files) > 0 && current.Size+file.Size > maxBytes {
			flush()
		}
		current.Files = append(current.Files, file)
		current.Size += file.Size
	}
	flush()
	return batches, nil
}

func receiveShareBatch(ctx context.Context, opts transferOptions, batch transferBatch, tempCID string) error {
	var ids []string
	for _, file := range batch.Files {
		ids = append(ids, file.ShareFileID)
	}
	for start := 0; start < len(ids); start += opts.receiveChunkSize {
		end := start + opts.receiveChunkSize
		if end > len(ids) {
			end = len(ids)
		}
		if err := opts.shareClient.receiveShareFiles(ctx, opts.shareCode, opts.receiveCode, ids[start:end], tempCID); err != nil {
			if len(ids[start:end]) == 1 {
				return err
			}
			log.Printf("receive chunk failed, retrying one by one: %v", err)
			for _, id := range ids[start:end] {
				if err := opts.shareClient.receiveShareFiles(ctx, opts.shareCode, opts.receiveCode, []string{id}, tempCID); err != nil {
					return fmt.Errorf("receive file_id %s: %w", id, err)
				}
			}
		}
	}
	return nil
}

func waitForTransferredMatches(ctx context.Context, client *driver115.Pan115Client, files []shareFileEntry, tempCID string, maxWait time.Duration) (map[string]panFileEntry, error) {
	deadline := time.Now().Add(maxWait)
	required := map[string]struct{}{}
	for _, file := range files {
		required[transferMatchKey(file.SHA1, file.Size)] = struct{}{}
	}
	var last map[string]panFileEntry
	for {
		panFiles, err := listPanFilesRecursive(ctx, client, tempCID)
		if err != nil {
			return nil, err
		}
		last = indexPanFiles(panFiles)
		if hasAllTransferMatches(last, required) {
			return last, nil
		}
		if maxWait <= 0 || time.Now().After(deadline) {
			break
		}
		timer := time.NewTimer(5 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return last, fmt.Errorf("not all transferred files became visible: have %d unique matches, need %d", len(last), len(required))
}

func cleanupTempDir(ctx context.Context, opts transferOptions, tempCID, tempName string) error {
	if err := opts.panClient.Delete(tempCID); err != nil {
		return fmt.Errorf("move temp dir to recycle bin: %w", err)
	}
	recycleIDs, err := waitForRecycleIDs(ctx, opts.panClient, tempCID, tempName, opts.recycleWait)
	if err != nil {
		return err
	}
	if len(recycleIDs) == 0 {
		return fmt.Errorf("temp dir not found in recycle bin")
	}
	if err := opts.panClient.CleanRecycleBin(opts.recyclePassword, recycleIDs...); err != nil {
		return fmt.Errorf("clean recycle bin ids %s: %w", strings.Join(recycleIDs, ","), err)
	}
	return nil
}

func waitForRecycleIDs(ctx context.Context, client *driver115.Pan115Client, tempCID, tempName string, maxWait time.Duration) ([]string, error) {
	deadline := time.Now().Add(maxWait)
	for {
		ids, err := listRecycleIDsForTempDir(client, tempCID, tempName)
		if err != nil {
			return nil, err
		}
		if len(ids) > 0 {
			return ids, nil
		}
		if maxWait <= 0 || time.Now().After(deadline) {
			break
		}
		timer := time.NewTimer(3 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return nil, fmt.Errorf("temp dir %q (%s) did not appear in recycle bin before timeout", tempName, tempCID)
}

func listRecycleIDsForTempDir(client *driver115.Pan115Client, tempCID, tempName string) ([]string, error) {
	const pageSize = 200
	for offset := 0; ; offset += pageSize {
		items, err := client.ListRecycleBin(offset, pageSize)
		if err != nil {
			return nil, err
		}
		ids := findRecycleIDsForTempDir(items, tempCID, tempName)
		if len(ids) > 0 {
			return ids, nil
		}
		if len(items) < pageSize {
			return nil, nil
		}
	}
}

func findRecycleIDsForTempDir(items []driver115.RecycleBinItem, tempCID, tempName string) []string {
	tempCID = strings.TrimSpace(tempCID)
	tempName = strings.TrimSpace(tempName)
	var ids []string
	for _, item := range items {
		id := strings.TrimSpace(item.FileId)
		name := strings.TrimSpace(item.FileName)
		if id == "" {
			continue
		}
		if (tempCID != "" && id == tempCID) || (tempName != "" && name == tempName) {
			ids = append(ids, id)
		}
	}
	return ids
}

func listPanFilesRecursive(ctx context.Context, client *driver115.Pan115Client, rootCID string) ([]panFileEntry, error) {
	type panDirJob struct {
		id   string
		path string
	}
	queue := []panDirJob{{id: rootCID, path: ""}}
	var out []panFileEntry
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		last := len(queue) - 1
		job := queue[last]
		queue = queue[:last]
		files, err := client.ListWithLimit(job.id, 1000, driver115.WithMultiUrls())
		if err != nil {
			return nil, err
		}
		for _, file := range *files {
			relPath := joinSharePath(job.path, file.Name)
			if file.IsDirectory {
				queue = append(queue, panDirJob{id: file.FileID, path: relPath})
				continue
			}
			out = append(out, panFileEntry{
				Path:     relPath,
				Name:     file.Name,
				Size:     file.Size,
				SHA1:     strings.ToUpper(strings.TrimSpace(file.Sha1)),
				PickCode: file.PickCode,
				FileID:   file.FileID,
			})
		}
	}
	return out, nil
}

func processTransferredFile(ctx context.Context, opts transferOptions, file shareFileEntry, panFile panFileEntry) manifestRecord {
	record := manifestRecord{
		Path:   file.Path,
		Name:   file.Name,
		Size:   file.Size,
		SHA1:   strings.ToUpper(strings.TrimSpace(file.SHA1)),
		FileID: file.ShareFileID,
	}
	casPath, err := share115cas.OutputCASPath(opts.outDir, file.Path)
	if err != nil {
		record.Status = "error"
		record.Error = err.Error()
		return record
	}
	record.CASPath = casPath
	if !opts.overwrite {
		if _, err := os.Stat(casPath); err == nil {
			record.Status = "exists"
			return record
		}
	}
	if strings.TrimSpace(panFile.PickCode) == "" {
		record.Status = "error"
		record.Error = "transferred file not found"
		return record
	}
	cacheKey := transferMatchKey(file.SHA1, file.Size)
	preID := strings.TrimSpace(opts.preIDCache[cacheKey])
	if preID == "" {
		if opts.panClient == nil {
			record.Status = "error"
			record.Error = "missing 115 client"
			return record
		}
		var err error
		preID, err = fetchPreIDByPickCode(ctx, opts.panClient, panFile.PickCode, file.Size, opts.userAgent)
		if err != nil {
			record.Status = "error"
			record.Error = err.Error()
			return record
		}
		if opts.preIDCache != nil {
			opts.preIDCache[cacheKey] = preID
		}
	}
	record.PreID = preID
	content, err := share115cas.EncodeCAS(file.Name, file.Size, file.SHA1, preID)
	if err != nil {
		record.Status = "error"
		record.Error = err.Error()
		return record
	}
	if err := os.MkdirAll(filepath.Dir(casPath), 0755); err != nil {
		record.Status = "error"
		record.Error = err.Error()
		return record
	}
	if err := os.WriteFile(casPath, content, 0644); err != nil {
		record.Status = "error"
		record.Error = err.Error()
		return record
	}
	record.Status = "ok"
	return record
}

func fetchPreIDByPickCode(ctx context.Context, client *driver115.Pan115Client, pickCode string, size int64, ua string) (string, error) {
	info, err := client.DownloadWithUA(pickCode, ua)
	if err != nil {
		return "", err
	}
	if info == nil || strings.TrimSpace(info.Url.Url) == "" {
		return "", fmt.Errorf("empty 115 download url")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, info.Url.Url, nil)
	if err != nil {
		return "", err
	}
	for key, values := range info.Header {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	if strings.TrimSpace(ua) != "" {
		req.Header.Set("User-Agent", ua)
	}
	end := int64(share115cas.PreIDSize - 1)
	if size > 0 && size-1 < end {
		end = size - 1
	}
	if end >= 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", end))
	}
	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
		return "", fmt.Errorf("range download HTTP %d", resp.StatusCode)
	}
	return share115cas.PreID(resp.Body, size)
}

func (c *shareWebClient) receiveShareFiles(ctx context.Context, shareCode, receiveCode string, fileIDs []string, targetCID string) error {
	if len(fileIDs) == 0 {
		return nil
	}
	form := url.Values{}
	form.Set("share_code", shareCode)
	form.Set("receive_code", receiveCode)
	form.Set("file_id", strings.Join(fileIDs, ","))
	if strings.TrimSpace(targetCID) != "" {
		form.Set("cid", targetCID)
	}
	body, err := c.postForm(ctx, "/share/receive", form, shareCode, receiveCode)
	if err != nil {
		return err
	}
	return decodeShareReceive(body)
}

func (c *shareWebClient) postForm(ctx context.Context, apiPath string, form url.Values, shareCode, receiveCode string) ([]byte, error) {
	if err := c.wait(ctx); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, shareWebAPIBase+apiPath, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.ua)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://115.com")
	req.Header.Set("Referer", shareReferer(shareCode, receiveCode))
	if c.cookie != "" {
		req.Header.Set("Cookie", c.cookie)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 16*1024*1024))
	if readErr != nil {
		return nil, readErr
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("share API HTTP %d: %s", resp.StatusCode, summarizeHTTPErrorBody(string(body)))
	}
	return body, nil
}

func decodeShareReceive(body []byte) error {
	var resp struct {
		State bool   `json:"state"`
		Error string `json:"error"`
		Msg   string `json:"msg"`
		Errno int    `json:"errno"`
		ErrNo int    `json:"errNo"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return err
	}
	if !resp.State {
		return shareAPIError(resp.Error, resp.Msg, resp.Errno, resp.ErrNo)
	}
	return nil
}

func newPan115Client(cookieHeader, ua string) (*driver115.Pan115Client, error) {
	client := driver115.New(driver115.UA(ua))
	client.Client.SetHeader("Cookie", cookieHeader)
	cookies := parseCookieMap(cookieHeader)
	if len(cookies) > 0 {
		client.ImportCookies(cookies, driver115.CookieDomain115)
	}
	cr := &driver115.Credential{}
	if err := cr.FromCookie(cookieHeader); err == nil {
		client.ImportCredential(cr)
	}
	if err := client.CookieCheck(); err != nil {
		return nil, err
	}
	return client, nil
}

func parseCookieMap(cookieHeader string) map[string]string {
	cookies := map[string]string{}
	for _, part := range strings.Split(cookieHeader, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		key, value, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" {
			continue
		}
		cookies[key] = value
	}
	return cookies
}

func parseByteSize(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, fmt.Errorf("empty size")
	}
	cut := 0
	for cut < len(raw) {
		ch := raw[cut]
		if (ch >= '0' && ch <= '9') || ch == '.' {
			cut++
			continue
		}
		break
	}
	if cut == 0 {
		return 0, fmt.Errorf("missing number in %q", raw)
	}
	value, err := strconv.ParseFloat(raw[:cut], 64)
	if err != nil {
		return 0, err
	}
	if value <= 0 {
		return 0, fmt.Errorf("size must be greater than 0")
	}
	unit := strings.ToLower(strings.TrimSpace(raw[cut:]))
	multiplier := float64(1)
	switch unit {
	case "", "b", "byte", "bytes":
		multiplier = 1
	case "k", "kb", "kib":
		multiplier = 1024
	case "m", "mb", "mib":
		multiplier = 1024 * 1024
	case "g", "gb", "gib":
		multiplier = 1024 * 1024 * 1024
	case "t", "tb", "tib":
		multiplier = 1024 * 1024 * 1024 * 1024
	case "p", "pb", "pib":
		multiplier = 1024 * 1024 * 1024 * 1024 * 1024
	default:
		return 0, fmt.Errorf("unknown size unit %q", unit)
	}
	bytes := value * multiplier
	if bytes > math.MaxInt64 {
		return 0, fmt.Errorf("size is too large")
	}
	return int64(math.Round(bytes)), nil
}

func indexPanFiles(files []panFileEntry) map[string]panFileEntry {
	out := map[string]panFileEntry{}
	for _, file := range files {
		key := transferMatchKey(file.SHA1, file.Size)
		if _, ok := out[key]; !ok {
			out[key] = file
		}
	}
	return out
}

func hasAllTransferMatches(matches map[string]panFileEntry, required map[string]struct{}) bool {
	for key := range required {
		if _, ok := matches[key]; !ok {
			return false
		}
	}
	return true
}

func transferMatchKey(sha1Hex string, size int64) string {
	return strings.ToUpper(strings.TrimSpace(sha1Hex)) + ":" + strconv.FormatInt(size, 10)
}

func makeTempBatchName(prefix string, batchNumber int) string {
	prefix = sanitizeTempNamePart(prefix)
	if prefix == "" {
		prefix = "openlist-share2cas-temp"
	}
	return fmt.Sprintf("%s-%s-%03d", prefix, time.Now().Format("20060102-150405"), batchNumber)
}

func sanitizeTempNamePart(name string) string {
	name = strings.TrimSpace(name)
	name = strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|':
			return '-'
		default:
			return r
		}
	}, name)
	name = strings.Trim(name, " .-")
	if len(name) > 80 {
		name = name[:80]
	}
	return name
}

func logManifestRecord(record manifestRecord) {
	switch record.Status {
	case "ok", "exists":
		log.Printf("%s %s", record.Status, record.Path)
	default:
		log.Printf("%s %s: %s", record.Status, record.Path, record.Error)
	}
}

func shouldRetryHTTPStatus(status int) bool {
	switch status {
	case http.StatusMethodNotAllowed, http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func (f shareItem) ReceiveID() string {
	if f.FileID.String() != "" {
		return f.FileID.String()
	}
	return f.ShareFileID.String()
}
