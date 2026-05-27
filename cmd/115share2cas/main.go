package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/share115cas"
)

const shareWebAPIBase = "https://webapi.115.com"

type manifestRecord struct {
	Path    string `json:"path"`
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	SHA1    string `json:"sha1"`
	PreID   string `json:"preID"`
	FileID  string `json:"file_id"`
	CASPath string `json:"cas_path"`
	Status  string `json:"status"`
	Error   string `json:"error,omitempty"`
}

type dirJob struct {
	id   string
	path string
}

type shareWebClient struct {
	httpClient *http.Client
	ua         string
	cookie     string
	userID     string
	delay      time.Duration
	lastAPI    time.Time
}

type shareSnap struct {
	UserID string
	Count  int
	List   []shareItem
}

type shareItem struct {
	FileID      flexString `json:"fid"`
	ShareFileID flexString `json:"sc"`
	CategoryID  flexString `json:"cid"`
	FileName    string     `json:"n"`
	FileSHA     string     `json:"sha"`
	FileSHA1    string     `json:"sha1"`
	FileSize    flexInt64  `json:"s"`
	IsFile      int        `json:"fc"`
}

type flexInt64 int64
type flexString string

func main() {
	var (
		shareURL     = flag.String("share-url", "", "115 share URL, for example https://115.com/s/code?password=xxxx")
		shareCode    = flag.String("share-code", "", "115 share code")
		receiveCode  = flag.String("receive-code", "", "115 receive/password code")
		cookieFile   = flag.String("cookie-file", "", "115 cookie file, required for transfer-batch mode")
		outDir       = flag.String("out", "115-cas-output", "output directory for .cas tree")
		manifest     = flag.String("manifest", "", "manifest jsonl path, default: [out]/manifest.jsonl")
		mode         = flag.String("mode", "transfer-batch", "mode: transfer-batch or direct")
		batchSize    = flag.String("batch-size", "1.5TiB", "max share files to transfer per batch, for example 1500GiB")
		pageSize     = flag.Int("page-size", 1000, "share list page size")
		delay        = flag.Duration("delay", 2*time.Second, "delay between 115 API calls")
		limit        = flag.Int("limit", 0, "max files to process, 0 means unlimited")
		maxListErrs  = flag.Int("max-list-errors", 5, "abort after this many consecutive list errors, 0 disables")
		tempParent   = flag.String("temp-parent-cid", "0", "115 target parent cid for temporary transferred batches")
		tempPrefix   = flag.String("temp-root-name", "openlist-share2cas-temp", "temporary 115 folder name prefix")
		keepTemp     = flag.Bool("keep-temp", false, "keep temporary transferred files instead of deleting each batch")
		recyclePass  = flag.String("recycle-password-file", "", "115 recycle-bin/safe password file, required when --keep-temp=false")
		recycleWait  = flag.Duration("recycle-wait", 2*time.Minute, "max time to wait for deleted temp dir to appear in recycle bin")
		chunkSize    = flag.Int("receive-chunk-size", 100, "max file IDs per /share/receive request")
		transferWait = flag.Duration("transfer-wait", 10*time.Minute, "max time to wait for transferred files to appear")
		overwrite    = flag.Bool("overwrite", false, "overwrite existing .cas files")
		userAgent    = flag.String("ua", "Mozilla/5.0", "User-Agent for 115 share requests")
	)
	flag.Parse()

	if *shareURL != "" {
		code, receive, err := share115cas.ParseShareURL(*shareURL)
		if err != nil {
			log.Fatalf("parse share url: %v", err)
		}
		if *shareCode == "" {
			*shareCode = code
		}
		if *receiveCode == "" {
			*receiveCode = receive
		}
	}
	if *shareCode == "" || *receiveCode == "" {
		log.Fatal("missing share info: pass --share-url or both --share-code and --receive-code")
	}
	if *manifest == "" {
		*manifest = filepath.Join(*outDir, "manifest.jsonl")
	}

	ctx := context.Background()
	cookieHeader := ""
	if *cookieFile != "" {
		var err error
		cookieHeader, err = readCookieHeader(*cookieFile)
		if err != nil {
			log.Fatalf("read cookie file: %v", err)
		}
	}
	recyclePassword := ""
	if *recyclePass != "" {
		secret, err := readSecretFile(*recyclePass)
		if err != nil {
			log.Fatalf("read recycle password file: %v", err)
		}
		recyclePassword = secret
	}
	client := newShareWebClient(*userAgent, cookieHeader, *delay)

	if err := os.MkdirAll(*outDir, 0755); err != nil {
		log.Fatalf("create output dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(*manifest), 0755); err != nil {
		log.Fatalf("create manifest dir: %v", err)
	}
	manifestFile, err := os.OpenFile(*manifest, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		log.Fatalf("open manifest: %v", err)
	}
	defer manifestFile.Close()
	encoder := json.NewEncoder(manifestFile)

	switch strings.ToLower(strings.TrimSpace(*mode)) {
	case "direct":
		runDirectMode(ctx, client, encoder, *shareCode, *receiveCode, *outDir, *pageSize, *limit, *maxListErrs, *overwrite, *manifest)
	case "transfer-batch":
		if strings.TrimSpace(cookieHeader) == "" {
			log.Fatal("transfer-batch mode requires --cookie-file with a logged-in 115 cookie")
		}
		maxBatchBytes, err := parseByteSize(*batchSize)
		if err != nil {
			log.Fatalf("parse --batch-size: %v", err)
		}
		panClient, err := newPan115Client(cookieHeader, *userAgent)
		if err != nil {
			log.Fatalf("init 115 client: %v", err)
		}
		err = runTransferBatchMode(ctx, transferOptions{
			shareClient:      client,
			panClient:        panClient,
			manifest:         encoder,
			shareCode:        *shareCode,
			receiveCode:      *receiveCode,
			outDir:           *outDir,
			manifestPath:     *manifest,
			pageSize:         *pageSize,
			limit:            *limit,
			maxListErrs:      *maxListErrs,
			overwrite:        *overwrite,
			maxBatchBytes:    maxBatchBytes,
			tempParentCID:    *tempParent,
			tempNamePrefix:   *tempPrefix,
			keepTemp:         *keepTemp,
			recyclePassword:  recyclePassword,
			recycleWait:      *recycleWait,
			receiveChunkSize: *chunkSize,
			transferWait:     *transferWait,
			userAgent:        *userAgent,
		})
		if err != nil {
			log.Fatal(err)
		}
	default:
		log.Fatalf("unknown --mode %q", *mode)
	}
}

func runDirectMode(ctx context.Context, client *shareWebClient, encoder *json.Encoder, shareCode, receiveCode, outDir string, pageSize, limit, maxListErrs int, overwrite bool, manifest string) {
	queue := []dirJob{{id: "", path: ""}}
	processed := 0
	consecutiveListErrors := 0
	for len(queue) > 0 {
		var job dirJob
		var ok bool
		job, queue, ok = popDirJob(queue)
		if !ok {
			break
		}

		files, err := listShareDir(ctx, client, shareCode, receiveCode, job.id, pageSize)
		if err != nil {
			consecutiveListErrors++
			record := manifestRecord{
				Path:   job.path,
				Status: "list_error",
				Error:  err.Error(),
			}
			if err := encoder.Encode(record); err != nil {
				log.Fatalf("write manifest: %v", err)
			}
			log.Printf("list_error %q: %v", job.path, err)
			if shouldAbortListErrors(consecutiveListErrors, maxListErrs) {
				log.Printf("abort: %d consecutive list errors", consecutiveListErrors)
				return
			}
			continue
		}
		consecutiveListErrors = 0
		for _, file := range files {
			name := file.FileName
			relPath := joinSharePath(job.path, name)
			if !file.IsRegularFile() {
				queue = append(queue, dirJob{id: file.DirID(), path: relPath})
				continue
			}
			if limit > 0 && processed >= limit {
				log.Printf("limit reached: %d files", processed)
				return
			}
			record := processFile(ctx, client, shareCode, receiveCode, outDir, relPath, file, overwrite)
			if err := encoder.Encode(record); err != nil {
				log.Fatalf("write manifest: %v", err)
			}
			processed++
			if record.Status == "ok" || record.Status == "exists" {
				log.Printf("[%d] %s %s", processed, record.Status, relPath)
			} else {
				log.Printf("[%d] %s %s: %s", processed, record.Status, relPath, record.Error)
			}
		}
	}
	log.Printf("done: %d files processed, manifest=%s, out=%s", processed, manifest, outDir)
}

func newShareWebClient(ua, cookie string, delay time.Duration) *shareWebClient {
	if strings.TrimSpace(ua) == "" {
		ua = "Mozilla/5.0"
	}
	return &shareWebClient{
		httpClient: &http.Client{Timeout: 60 * time.Second},
		ua:         ua,
		cookie:     cookie,
		delay:      delay,
	}
}

func listShareDir(ctx context.Context, client *shareWebClient, shareCode, receiveCode, dirID string, pageSize int) ([]shareItem, error) {
	if pageSize <= 0 {
		pageSize = 1000
	}
	var all []shareItem
	offset := 0
	for {
		snap, err := client.getShareSnap(ctx, shareCode, receiveCode, dirID, pageSize, offset)
		if err != nil {
			return nil, err
		}
		all = append(all, snap.List...)
		offset += len(snap.List)
		if len(snap.List) == 0 || (snap.Count > 0 && offset >= snap.Count) || len(snap.List) < pageSize {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
	}
	return all, nil
}

func processFile(ctx context.Context, client *shareWebClient, shareCode, receiveCode, outDir, relPath string, file shareItem, overwrite bool) manifestRecord {
	name := file.FileName
	size := file.Size()
	sha1Hex := strings.ToUpper(strings.TrimSpace(file.SHA1()))
	record := manifestRecord{
		Path:   relPath,
		Name:   name,
		Size:   size,
		SHA1:   sha1Hex,
		FileID: file.DownloadID(),
	}
	casPath, err := share115cas.OutputCASPath(outDir, relPath)
	if err != nil {
		record.Status = "error"
		record.Error = err.Error()
		return record
	}
	record.CASPath = casPath
	if !overwrite {
		if _, err := os.Stat(casPath); err == nil {
			record.Status = "exists"
			return record
		}
	}
	if sha1Hex == "" {
		record.Status = "error"
		record.Error = "missing sha1"
		return record
	}
	if file.DownloadID() == "" {
		record.Status = "error"
		record.Error = "missing share file id"
		return record
	}

	preID, err := fetchPreID(ctx, client, shareCode, receiveCode, file.DownloadID(), size)
	if err != nil {
		record.Status = "error"
		record.Error = err.Error()
		return record
	}
	record.PreID = preID
	content, err := share115cas.EncodeCAS(name, size, sha1Hex, preID)
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

func fetchPreID(ctx context.Context, client *shareWebClient, shareCode, receiveCode, fileID string, size int64) (string, error) {
	downloadURL, err := client.getShareDownloadURL(ctx, shareCode, receiveCode, fileID)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(downloadURL) == "" {
		return "", fmt.Errorf("empty share download url")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", client.ua)
	req.Header.Set("Referer", shareReferer(shareCode, receiveCode))
	end := int64(share115cas.PreIDSize - 1)
	if size > 0 && size-1 < end {
		end = size - 1
	}
	if end >= 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", end))
	}
	resp, err := client.httpClient.Do(req)
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

func (c *shareWebClient) getShareSnap(ctx context.Context, shareCode, receiveCode, dirID string, limit, offset int) (*shareSnap, error) {
	if strings.TrimSpace(dirID) == "" {
		dirID = "0"
	}
	apiURL, err := buildShareAPIURL("/share/snap", map[string]string{
		"share_code":   shareCode,
		"offset":       strconv.Itoa(offset),
		"limit":        strconv.Itoa(limit),
		"asc":          "0",
		"cid":          dirID,
		"receive_code": receiveCode,
		"format":       "json",
	})
	if err != nil {
		return nil, err
	}
	body, err := c.get(ctx, apiURL, shareCode, receiveCode)
	if err != nil {
		return nil, err
	}
	snap, err := decodeShareSnap(body)
	if err != nil {
		return nil, err
	}
	if snap.UserID != "" {
		c.userID = snap.UserID
	}
	return snap, nil
}

func (c *shareWebClient) getShareDownloadURL(ctx context.Context, shareCode, receiveCode, fileID string) (string, error) {
	if c.userID == "" {
		return "", fmt.Errorf("missing share user_id")
	}
	apiURL, err := buildShareAPIURL("/share/downurl", map[string]string{
		"dl":           "1",
		"user_id":      c.userID,
		"share_code":   shareCode,
		"file_id":      fileID,
		"receive_code": receiveCode,
	})
	if err != nil {
		return "", err
	}
	body, err := c.get(ctx, apiURL, shareCode, receiveCode)
	if err != nil {
		return "", err
	}
	downloadURL, err := decodeShareDownloadURL(body)
	if err != nil {
		return "", err
	}
	return downloadURL, nil
}

func (c *shareWebClient) get(ctx context.Context, rawURL, shareCode, receiveCode string) ([]byte, error) {
	if err := c.wait(ctx); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.ua)
	req.Header.Set("Accept", "application/json, text/plain, */*")
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

func (c *shareWebClient) wait(ctx context.Context) error {
	if c.delay <= 0 || c.lastAPI.IsZero() {
		c.lastAPI = time.Now()
		return nil
	}
	next := c.lastAPI.Add(c.delay)
	wait := time.Until(next)
	if wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
	c.lastAPI = time.Now()
	return nil
}

func decodeShareSnap(body []byte) (*shareSnap, error) {
	var resp struct {
		State bool   `json:"state"`
		Error string `json:"error"`
		Msg   string `json:"msg"`
		Errno int    `json:"errno"`
		ErrNo int    `json:"errNo"`
		Data  struct {
			Userinfo struct {
				UserID string `json:"user_id"`
			} `json:"userinfo"`
			Count int         `json:"count"`
			List  []shareItem `json:"list"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	if !resp.State {
		return nil, shareAPIError(resp.Error, resp.Msg, resp.Errno, resp.ErrNo)
	}
	return &shareSnap{
		UserID: resp.Data.Userinfo.UserID,
		Count:  resp.Data.Count,
		List:   resp.Data.List,
	}, nil
}

func decodeShareDownloadURL(body []byte) (string, error) {
	var resp struct {
		State bool   `json:"state"`
		Error string `json:"error"`
		Msg   string `json:"msg"`
		Errno int    `json:"errno"`
		ErrNo int    `json:"errNo"`
		Data  struct {
			FileURL302 string          `json:"file_url_302"`
			URL        json.RawMessage `json:"url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", err
	}
	if !resp.State {
		err := shareAPIError(resp.Error, resp.Msg, resp.Errno, resp.ErrNo)
		if resp.Errno == 990001 || resp.ErrNo == 990001 {
			return "", fmt.Errorf("%w: pass --cookie-file with a logged-in 115 cookie", err)
		}
		return "", err
	}
	if strings.TrimSpace(resp.Data.FileURL302) != "" {
		return strings.TrimSpace(resp.Data.FileURL302), nil
	}
	if len(resp.Data.URL) > 0 && string(resp.Data.URL) != "false" && string(resp.Data.URL) != "null" {
		var nested struct {
			URL string `json:"url"`
		}
		if err := json.Unmarshal(resp.Data.URL, &nested); err == nil && strings.TrimSpace(nested.URL) != "" {
			return strings.TrimSpace(nested.URL), nil
		}
		var direct string
		if err := json.Unmarshal(resp.Data.URL, &direct); err == nil && strings.TrimSpace(direct) != "" {
			return strings.TrimSpace(direct), nil
		}
	}
	return "", fmt.Errorf("empty share download url")
}

func (f *flexInt64) UnmarshalJSON(data []byte) error {
	raw := strings.TrimSpace(string(data))
	if raw == "" || raw == "null" || raw == "false" {
		*f = 0
		return nil
	}
	raw = strings.Trim(raw, `"`)
	if raw == "" {
		*f = 0
		return nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return err
	}
	*f = flexInt64(n)
	return nil
}

func (s *flexString) UnmarshalJSON(data []byte) error {
	raw := strings.TrimSpace(string(data))
	if raw == "" || raw == "null" || raw == "false" {
		*s = ""
		return nil
	}
	if strings.HasPrefix(raw, `"`) {
		var value string
		if err := json.Unmarshal(data, &value); err != nil {
			return err
		}
		*s = flexString(value)
		return nil
	}
	*s = flexString(raw)
	return nil
}

func (s flexString) String() string {
	return strings.TrimSpace(string(s))
}

func (f shareItem) IsRegularFile() bool {
	return f.IsFile != 0
}

func (f shareItem) DirID() string {
	return f.CategoryID.String()
}

func (f shareItem) DownloadID() string {
	if f.ShareFileID.String() != "" {
		return f.ShareFileID.String()
	}
	return f.FileID.String()
}

func (f shareItem) SHA1() string {
	if strings.TrimSpace(f.FileSHA) != "" {
		return strings.TrimSpace(f.FileSHA)
	}
	return strings.TrimSpace(f.FileSHA1)
}

func (f shareItem) Size() int64 {
	return int64(f.FileSize)
}

func buildShareAPIURL(apiPath string, params map[string]string) (string, error) {
	u, err := url.Parse(shareWebAPIBase + apiPath)
	if err != nil {
		return "", err
	}
	q := u.Query()
	for key, value := range params {
		if value != "" {
			q.Set(key, value)
		}
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func shareReferer(shareCode, receiveCode string) string {
	return fmt.Sprintf("https://115.com/s/%s?password=%s", url.PathEscape(shareCode), url.QueryEscape(receiveCode))
}

func shareAPIError(errorText, msg string, errno, errNo int) error {
	message := strings.TrimSpace(errorText)
	if message == "" {
		message = strings.TrimSpace(msg)
	}
	code := errno
	if code == 0 {
		code = errNo
	}
	if message == "" {
		message = "share API returned state=false"
	}
	if code != 0 {
		return fmt.Errorf("%s (errno=%d)", message, code)
	}
	return fmt.Errorf("%s", message)
}

func summarizeHTTPErrorBody(body string) string {
	summary := strings.Join(strings.Fields(body), " ")
	if len(summary) > 200 {
		summary = summary[:200] + "..."
	}
	return summary
}

func readCookieHeader(path string) (string, error) {
	cookieBytes, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var parts []string
	for _, line := range strings.Split(string(cookieBytes), "\n") {
		line = strings.TrimSpace(strings.TrimRight(line, ";"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 7 && strings.Contains(fields[5], "=") == false {
			parts = append(parts, fields[5]+"="+fields[6])
			continue
		}
		if strings.Contains(line, "=") {
			parts = append(parts, line)
		}
	}
	return strings.Join(parts, "; "), nil
}

func readSecretFile(path string) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	secret := strings.TrimSpace(string(content))
	if secret == "" {
		return "", fmt.Errorf("empty secret file")
	}
	return secret, nil
}

func joinSharePath(parent, name string) string {
	name = strings.Trim(name, "/")
	if parent == "" {
		return name
	}
	return parent + "/" + name
}

func popDirJob(queue []dirJob) (dirJob, []dirJob, bool) {
	if len(queue) == 0 {
		return dirJob{}, queue, false
	}
	last := len(queue) - 1
	return queue[last], queue[:last], true
}

func shouldAbortListErrors(consecutive, limit int) bool {
	return limit > 0 && consecutive >= limit
}
