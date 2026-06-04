package _115

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/casmeta"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	driver115 "github.com/SheltonZhu/115driver/pkg/driver"
	"github.com/google/uuid"
)

const (
	casProvider115              = "115"
	casTempDirName              = "TEMP"
	casRestoreShareTempPrefix   = "openlist-cas-restore"
	casRestoreTransferWait      = 2 * time.Minute
	casRestoreCleanupWait       = 2 * time.Minute
	casRestoreRecycleListWait   = 30 * time.Second
	casRestoreSharePollInterval = 5 * time.Second
)

var shareWebAPIBase = "https://webapi.115.com"

type casUploadInfo = casmeta.Info

func isCASName(name string) bool {
	return casmeta.IsName(name)
}

func (d *Pan115) shouldUploadCAS(name string) bool {
	return d.GenerateCAS && !isCASName(name) && casmeta.ExtAllowed(name, d.CASExtAllowlist)
}

func (d *Pan115) shouldDeleteSource() bool {
	return d.GenerateCAS && d.DeleteSource
}

func (d *Pan115) CASDownloadRestoreEnabled() bool {
	return d.CASDownloadRestore
}

func (d *Pan115) shouldPlayCAS(file model.Obj, args model.LinkArgs) bool {
	if !isCASName(file.GetName()) {
		return false
	}
	return strings.EqualFold(args.Type, "cas_video")
}

func (d *Pan115) CASPreviewName(ctx context.Context, file model.Obj) (string, error) {
	if !isCASName(file.GetName()) {
		return file.GetName(), nil
	}
	info, err := d.parseCASFromObj(ctx, file)
	if err != nil {
		return "", err
	}
	previewName, err := resolveCASRestoreName(file.GetName(), info)
	if err != nil {
		return "", err
	}
	if !casmeta.ExtAllowed(previewName, d.CASExtAllowlist) {
		return file.GetName(), nil
	}
	if !d.CASDownloadRestore && !isVideoName(previewName) {
		return file.GetName(), nil
	}
	return previewName, nil
}

func (d *Pan115) prepareCASPut(ctx context.Context, dstDir model.Obj, file model.FileStreamer) (model.FileStreamer, model.Obj, bool, error) {
	if !d.RestoreSourceFromCAS || !isCASName(file.GetName()) {
		return file, nil, false, nil
	}
	casData, err := io.ReadAll(file)
	if err != nil {
		return nil, nil, true, err
	}
	info, err := d.parseCAS(casData)
	if err != nil {
		return nil, nil, true, err
	}
	restoredName, err := resolveCASRestoreName(file.GetName(), info)
	if err != nil {
		return nil, nil, true, err
	}
	if !casmeta.ExtAllowed(restoredName, d.CASExtAllowlist) {
		return &stream.FileStream{
			Ctx:               ctx,
			Obj:               file,
			Reader:            bytes.NewReader(casData),
			Mimetype:          file.GetMimetype(),
			ForceStreamUpload: file.IsForceStreamUpload(),
			Exist:             file.GetExist(),
		}, nil, false, nil
	}
	restored, err := d.restoreCAS(ctx, dstDir, info, file.GetName(), false)
	return nil, restored, true, err
}

func (d *Pan115) uploadCAS(ctx context.Context, dstDir model.Obj, info *casUploadInfo) (model.Obj, error) {
	if info == nil || !d.shouldUploadCAS(info.Name) {
		return nil, nil
	}
	content, err := casmeta.Encode(info)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	casObj := &model.Object{
		Name:     casmeta.FileName(info.Name),
		Size:     int64(len(content)),
		Modified: now,
		Ctime:    now,
		HashInfo: utils.NewHashInfo(utils.MD5, utils.HashData(utils.MD5, content)),
	}
	casStream := &stream.FileStream{
		Ctx:      ctx,
		Obj:      casObj,
		Reader:   bytes.NewReader(content),
		Mimetype: "text/plain",
	}
	uploadedCASObj, _, err := d.putRaw(ctx, dstDir, casStream, func(float64) {})
	if err != nil {
		return nil, err
	}
	if uploadedCASObj != nil {
		return uploadedCASObj, nil
	}
	return casObj, nil
}

func (d *Pan115) deleteSource(ctx context.Context, uploadedObj model.Obj) error {
	if uploadedObj == nil || uploadedObj.GetID() == "" {
		return nil
	}
	return d.Remove(ctx, uploadedObj)
}

func (d *Pan115) parseCAS(data []byte) (*casUploadInfo, error) {
	return casmeta.Decode(data)
}

func (d *Pan115) parseCASFromObj(ctx context.Context, file model.Obj) (*casUploadInfo, error) {
	link, err := d.Link(ctx, file, model.LinkArgs{Type: "raw_cas"})
	if err != nil {
		return nil, err
	}
	defer link.Close()
	if link.URL == "" {
		return nil, fmt.Errorf("cas link has no url")
	}
	req := base.RestyClient.R().SetContext(ctx)
	if link.Header != nil {
		req.SetHeaders(headerToMap(link.Header))
	}
	resp, err := req.Get(link.URL)
	if err != nil {
		return nil, err
	}
	return d.parseCAS(resp.Body())
}

func resolveCASRestoreName(casName string, info *casUploadInfo) (string, error) {
	return casmeta.ResolveRestoreName(casName, info)
}

func (d *Pan115) validateCASInfo(info *casUploadInfo) error {
	if info == nil {
		return fmt.Errorf("cas restore failed: missing cas payload")
	}
	if !strings.EqualFold(info.Provider, casProvider115) {
		return fmt.Errorf("cas restore failed: unsupported provider %q", info.Provider)
	}
	if info.SHA1 == "" {
		return fmt.Errorf("cas restore failed: missing sha1")
	}
	if info.PreID == "" {
		return fmt.Errorf("cas restore failed: missing preID")
	}
	return nil
}

func (d *Pan115) restoreCAS(ctx context.Context, dstDir model.Obj, info *casUploadInfo, casName string, temp bool) (model.Obj, error) {
	targetName, err := resolveCASRestoreName(casName, info)
	if err != nil {
		return nil, err
	}
	if !casmeta.ExtAllowed(targetName, d.CASExtAllowlist) {
		return nil, fmt.Errorf("cas restore skipped: extension of %q is not allowed", targetName)
	}
	if err = d.validateCASInfo(info); err != nil {
		return nil, err
	}
	if temp {
		targetName = fmt.Sprintf("TEMP_%d_%s_%s", time.Now().UnixNano()/1e6, uuid.NewString()[:5], targetName)
	}
	if existing, err := d.findFileByName(ctx, targetName, dstDir.GetID()); err == nil && !temp {
		return existing, nil
	}
	stream := &casRestoreStream{
		pan:        d,
		ctx:        ctx,
		info:       info,
		name:       targetName,
		cookie:     d.Cookie,
		ua:         d.getUA(),
		httpClient: &http.Client{Timeout: 60 * time.Second},
	}
	resp, err := d.rapidUpload(info.Size, targetName, dstDir.GetID(), strings.ToUpper(info.PreID), strings.ToUpper(info.SHA1), stream)
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), casRestoreCleanupWait)
	defer cleanupCancel()
	cleanupErr := stream.cleanup(cleanupCtx)
	if err != nil {
		return nil, combineRestoreAndCleanupError(err, cleanupErr)
	}
	matched, err := resp.Ok()
	if err != nil {
		return nil, combineRestoreAndCleanupError(err, cleanupErr)
	}
	if !matched {
		return nil, combineRestoreAndCleanupError(fmt.Errorf("cas restore failed: source file data does not exist in cloud"), cleanupErr)
	}
	file, err := d.getNewFileByPickCode(resp.PickCode)
	if err != nil {
		return nil, combineRestoreAndCleanupError(err, cleanupErr)
	}
	if cleanupErr != nil {
		utils.Log.Errorf("cas115RestoreCleanupError:%s", cleanupErr)
	}
	return file, nil
}

func (d *Pan115) linkCASVideo(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	info, err := d.parseCASFromObj(ctx, file)
	if err != nil {
		return nil, err
	}
	previewName, err := resolveCASRestoreName(file.GetName(), info)
	if err != nil {
		return nil, err
	}
	if !casmeta.ExtAllowed(previewName, d.CASExtAllowlist) || (!d.CASDownloadRestore && !isVideoName(previewName)) {
		return d.Link(ctx, file, model.LinkArgs{IP: args.IP, Header: args.Header, Type: "raw_cas", Redirect: args.Redirect})
	}
	tempRoot, err := d.ensureTempDir(ctx)
	if err != nil {
		return nil, err
	}
	tempObj, err := d.restoreCAS(ctx, tempRoot, info, casmeta.FileName(previewName), true)
	if err != nil {
		return nil, err
	}
	link, err := d.Link(ctx, tempObj, model.LinkArgs{IP: args.IP, Header: args.Header, Type: "raw_file", Redirect: args.Redirect})
	if err != nil {
		_ = d.Remove(context.TODO(), tempObj)
		return nil, err
	}
	go func() {
		if err := d.Remove(context.TODO(), tempObj); err != nil {
			utils.Log.Errorf("cas115TempDeleteError:%s", err)
		}
	}()
	link.ContentLength = info.Size
	link.RequireReference = true
	return link, nil
}

func (d *Pan115) ensureTempDir(ctx context.Context) (model.Obj, error) {
	if obj, err := d.findFolderByName(ctx, casTempDirName, d.RootFolderID); err == nil {
		return obj, nil
	}
	root := d.RootFolderID
	if root == "" {
		root = "0"
	}
	return d.MakeDir(ctx, &model.Object{ID: root, Name: root, IsFolder: true}, casTempDirName)
}

func (d *Pan115) findFileByName(ctx context.Context, name, dirID string) (model.Obj, error) {
	files, err := d.getFiles(dirID)
	if err != nil {
		return nil, err
	}
	for i := range files {
		if !files[i].IsDir() && files[i].GetName() == name {
			return &files[i], nil
		}
	}
	return nil, driver115.ErrNotExist
}

func (d *Pan115) findFolderByName(ctx context.Context, name, dirID string) (model.Obj, error) {
	files, err := d.getFiles(dirID)
	if err != nil {
		return nil, err
	}
	for i := range files {
		if files[i].IsDir() && files[i].GetName() == name {
			return &files[i], nil
		}
	}
	return nil, driver115.ErrNotExist
}

type casRestoreStream struct {
	pan    *Pan115
	ctx    context.Context
	info   *casUploadInfo
	name   string
	cookie string
	ua     string

	httpClient *http.Client

	mu            sync.Mutex
	tempCID       string
	tempName      string
	shareReceived bool
	transferred   casTransferredFile

	createTempDirFn      func(context.Context) (string, string, error)
	receiveShareFn       func(context.Context, *casmeta.Source, string) error
	waitForTransferredFn func(context.Context, string) (casTransferredFile, error)
	downloadInfoFn       func(string, string) (*driver115.DownloadInfo, error)
	listDirFn            func(context.Context, string) ([]driver115.File, error)
	getFileByIDFn        func(string) (*FileObj, error)
	deleteFn             func(context.Context, string) error
	waitForRecycleIDsFn  func(context.Context, *Pan115, string, string, time.Duration) ([]string, error)
	cleanRecycleFn       func(context.Context, *Pan115, string, []string, time.Duration) error
	cleanupFn            func(context.Context) error

	transferWait    time.Duration
	sharePollWait   time.Duration
	cleanupWait     time.Duration
	recycleListWait time.Duration

	utils.Closers
}

type casTransferredFile struct {
	Name     string
	Size     int64
	SHA1     string
	PickCode string
	FileID   string
}

func (s *casRestoreStream) Read([]byte) (int, error) {
	return 0, fmt.Errorf("cas restore stream has no source bytes")
}

func (s *casRestoreStream) GetSize() int64 {
	return s.info.Size
}

func (s *casRestoreStream) GetName() string {
	return s.name
}

func (s *casRestoreStream) ModTime() time.Time {
	return time.Now()
}

func (s *casRestoreStream) CreateTime() time.Time {
	return s.ModTime()
}

func (s *casRestoreStream) IsDir() bool {
	return false
}

func (s *casRestoreStream) GetHash() utils.HashInfo {
	return utils.NewHashInfo(utils.SHA1, s.info.SHA1)
}

func (s *casRestoreStream) GetID() string {
	return ""
}

func (s *casRestoreStream) GetPath() string {
	return ""
}

func (s *casRestoreStream) GetMimetype() string {
	return utils.GetMimeType(s.name)
}

func (s *casRestoreStream) NeedStore() bool {
	return false
}

func (s *casRestoreStream) IsForceStreamUpload() bool {
	return false
}

func (s *casRestoreStream) GetExist() model.Obj {
	return nil
}

func (s *casRestoreStream) SetExist(model.Obj) {}

func (s *casRestoreStream) RangeRead(httpRange http_range.Range) (io.Reader, error) {
	if s.info == nil || s.info.Source == nil {
		return nil, fmt.Errorf("cas restore requires source bytes for range %d-%d", httpRange.Start, httpRange.Start+httpRange.Length-1)
	}
	switch strings.ToLower(strings.TrimSpace(s.info.Source.Type)) {
	case "115_share":
		return s.rangeRead115Share(httpRange)
	default:
		return nil, fmt.Errorf("cas restore unsupported source type %q", s.info.Source.Type)
	}
}

func (s *casRestoreStream) CacheFullAndWriter(*model.UpdateProgress, io.Writer) (model.File, error) {
	return nil, fmt.Errorf("cas restore stream has no source bytes")
}

func (s *casRestoreStream) GetFile() model.File {
	return nil
}

func isVideoName(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".mp4", ".mkv", ".avi", ".mov", ".webm", ".flv", ".ts", ".m2ts", ".wmv", ".rmvb", ".m4v", ".mpg", ".mpeg", ".3gp":
		return true
	default:
		return false
	}
}

func (s *casRestoreStream) rangeRead115Share(httpRange http_range.Range) (io.Reader, error) {
	source := s.info.Source
	if err := validate115ShareSource(source); err != nil {
		return nil, err
	}
	if httpRange.Length < 0 {
		httpRange.Length = s.info.Size - httpRange.Start
	}
	if httpRange.Start < 0 || httpRange.Length <= 0 || httpRange.Start+httpRange.Length > s.info.Size {
		return nil, fmt.Errorf("cas restore invalid source range %d-%d", httpRange.Start, httpRange.Start+httpRange.Length-1)
	}

	file, err := s.ensureTransferred115ShareFile(s.streamContext(), source)
	if err != nil {
		return nil, err
	}
	downloadInfo, err := s.downloadInfo(file.PickCode)
	if err != nil {
		return nil, err
	}
	if downloadInfo == nil || strings.TrimSpace(downloadInfo.Url.Url) == "" {
		return nil, fmt.Errorf("cas restore account download url is empty")
	}
	req, err := http.NewRequestWithContext(s.streamContext(), http.MethodGet, downloadInfo.Url.Url, nil)
	if err != nil {
		return nil, fmt.Errorf("cas restore account download request build failed")
	}
	for key, values := range downloadInfo.Header {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	req.Header.Set("User-Agent", s.userAgent())
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", httpRange.Start, httpRange.Start+httpRange.Length-1))
	resp, err := s.httpClientForRequests().Do(req)
	if err != nil {
		return nil, sanitizeDownloadRequestError(err)
	}
	defer resp.Body.Close()
	fullRange := httpRange.Start == 0 && httpRange.Length == s.info.Size
	if resp.StatusCode != http.StatusPartialContent && !(fullRange && resp.StatusCode == http.StatusOK) {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("cas restore source range HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, httpRange.Length+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) != httpRange.Length {
		return nil, fmt.Errorf("cas restore source range returned %d bytes, want %d", len(body), httpRange.Length)
	}
	return bytes.NewReader(body), nil
}

func (s *casRestoreStream) ensureTransferred115ShareFile(ctx context.Context, source *casmeta.Source) (casTransferredFile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if strings.TrimSpace(s.transferred.PickCode) != "" {
		return s.transferred, nil
	}
	if strings.TrimSpace(s.tempCID) == "" {
		tempCID, tempName, err := s.createTempDir(ctx)
		if err != nil {
			return casTransferredFile{}, err
		}
		s.tempCID = tempCID
		s.tempName = tempName
	}
	if !s.shareReceived {
		if err := s.receiveShare(ctx, source, s.tempCID); err != nil {
			return casTransferredFile{}, err
		}
		s.shareReceived = true
	}
	file, err := s.waitForTransferred(ctx, s.tempCID)
	if err != nil {
		return casTransferredFile{}, err
	}
	s.transferred = file
	return s.transferred, nil
}

func (s *casRestoreStream) createTempDir(ctx context.Context) (string, string, error) {
	if s.createTempDirFn != nil {
		return s.createTempDirFn(ctx)
	}
	if s.pan == nil || s.pan.client == nil {
		return "", "", fmt.Errorf("cas restore missing 115 client")
	}
	if err := s.waitLimit(ctx); err != nil {
		return "", "", err
	}
	parentCID := strings.TrimSpace(s.pan.RootFolderID)
	if parentCID == "" {
		parentCID = "0"
	}
	tempName := fmt.Sprintf("%s-%d-%s", casRestoreShareTempPrefix, time.Now().UnixNano()/1e6, uuid.NewString()[:8])
	tempCID, err := s.pan.client.Mkdir(parentCID, tempName)
	if err != nil {
		return "", "", fmt.Errorf("create temp dir %q: %w", tempName, err)
	}
	return tempCID, tempName, nil
}

func (s *casRestoreStream) receiveShare(ctx context.Context, source *casmeta.Source, tempCID string) error {
	if s.receiveShareFn != nil {
		return s.receiveShareFn(ctx, source, tempCID)
	}
	if strings.TrimSpace(s.cookie) == "" {
		return fmt.Errorf("cas restore 115_share requires web cookie")
	}
	if err := s.waitLimit(ctx); err != nil {
		return err
	}
	form := url.Values{}
	form.Set("share_code", source.ShareCode)
	form.Set("receive_code", source.ReceiveCode)
	form.Set("file_id", source.FileID)
	form.Set("cid", tempCID)
	form.Set("is_check", "0")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, shareWebAPIBase+"/share/receive", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", s.userAgent())
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://115.com")
	req.Header.Set("Referer", shareReferer(source.ShareCode, source.ReceiveCode))
	req.Header.Set("Cookie", s.cookie)

	resp, err := s.httpClientForRequests().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("cas restore share receive HTTP %d: %s", resp.StatusCode, summarizeHTTPErrorBody(string(body)))
	}
	if err := decodeShareReceive(body); err != nil {
		return fmt.Errorf("cas restore share receive failed: %w", err)
	}
	return nil
}

func (s *casRestoreStream) waitForTransferred(ctx context.Context, tempCID string) (casTransferredFile, error) {
	if s.waitForTransferredFn != nil {
		return s.waitForTransferredFn(ctx, tempCID)
	}
	deadline := time.Now().Add(casRestoreTransferWait)
	if s.transferWait > 0 {
		deadline = time.Now().Add(s.transferWait)
	}
	for {
		file, found, err := s.findTransferredFile(ctx, tempCID)
		if err != nil {
			return casTransferredFile{}, err
		}
		if found {
			return file, nil
		}
		if time.Now().After(deadline) {
			break
		}
		interval := casRestoreSharePollInterval
		if s.sharePollWait > 0 {
			interval = s.sharePollWait
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return casTransferredFile{}, ctx.Err()
		case <-timer.C:
		}
	}
	return casTransferredFile{}, fmt.Errorf("cas restore transferred file sha1=%s size=%d did not become visible in temp dir %s", strings.ToUpper(strings.TrimSpace(s.info.SHA1)), s.info.Size, tempCID)
}

func (s *casRestoreStream) findTransferredFile(ctx context.Context, rootCID string) (casTransferredFile, bool, error) {
	if s.listDirFn == nil && (s.pan == nil || s.pan.client == nil) {
		return casTransferredFile{}, false, fmt.Errorf("cas restore missing 115 client")
	}
	wantSHA1 := strings.ToUpper(strings.TrimSpace(s.info.SHA1))
	queue := []string{rootCID}
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return casTransferredFile{}, false, err
		}
		last := len(queue) - 1
		dirID := queue[last]
		queue = queue[:last]
		if err := s.waitLimit(ctx); err != nil {
			return casTransferredFile{}, false, err
		}
		files, err := s.listDir(ctx, dirID)
		if err != nil {
			return casTransferredFile{}, false, err
		}
		for _, file := range files {
			if file.IsDirectory {
				queue = append(queue, file.FileID)
				continue
			}
			if strings.ToUpper(strings.TrimSpace(file.Sha1)) != wantSHA1 || file.Size != s.info.Size {
				continue
			}
			match := casTransferredFile{
				Name:     file.Name,
				Size:     file.Size,
				SHA1:     strings.ToUpper(strings.TrimSpace(file.Sha1)),
				PickCode: strings.TrimSpace(file.PickCode),
				FileID:   strings.TrimSpace(file.FileID),
			}
			if match.PickCode == "" && match.FileID != "" {
				full, err := s.getFileByID(match.FileID)
				if err != nil {
					continue
				}
				match.PickCode = strings.TrimSpace(full.PickCode)
				if match.Name == "" {
					match.Name = full.Name
				}
			}
			if match.PickCode == "" {
				continue
			}
			return match, true, nil
		}
	}
	return casTransferredFile{}, false, nil
}

func (s *casRestoreStream) downloadInfo(pickCode string) (*driver115.DownloadInfo, error) {
	if s.downloadInfoFn != nil {
		return s.downloadInfoFn(pickCode, s.userAgent())
	}
	if s.pan == nil || s.pan.client == nil {
		return nil, fmt.Errorf("cas restore missing 115 client")
	}
	if err := s.waitLimit(s.streamContext()); err != nil {
		return nil, err
	}
	return s.pan.client.DownloadWithUA(pickCode, s.userAgent())
}

func (s *casRestoreStream) cleanup(ctx context.Context) error {
	if s.cleanupFn != nil {
		return s.cleanupFn(ctx)
	}
	s.mu.Lock()
	tempCID := strings.TrimSpace(s.tempCID)
	tempName := strings.TrimSpace(s.tempName)
	s.mu.Unlock()
	if tempCID == "" {
		return nil
	}
	if s.deleteFn == nil && (s.pan == nil || s.pan.client == nil) {
		return fmt.Errorf("cas restore missing 115 client for cleanup")
	}
	if err := s.waitLimit(ctx); err != nil {
		return err
	}
	if err := s.deleteTempDir(ctx, tempCID); err != nil {
		return fmt.Errorf("cas restore temp dir delete %s (%s): %w", tempName, tempCID, err)
	}
	if s.pan == nil || strings.TrimSpace(s.pan.CASRecyclePassword) == "" {
		return missingRecyclePasswordCleanupError()
	}
	recycleWait := casRestoreRecycleListWait
	if s.recycleListWait > 0 {
		recycleWait = s.recycleListWait
	}
	recycleIDs, err := s.waitRecycleIDs(ctx, s.pan, tempCID, tempName, recycleWait)
	if err != nil {
		return err
	}
	if len(recycleIDs) == 0 {
		return fmt.Errorf("cas restore temp dir %q (%s) not found in recycle bin", tempName, tempCID)
	}
	cleanupWait := casRestoreCleanupWait
	if s.cleanupWait > 0 {
		cleanupWait = s.cleanupWait
	}
	return s.cleanRecycle(ctx, s.pan, s.pan.CASRecyclePassword, recycleIDs, cleanupWait)
}

func (s *casRestoreStream) streamContext() context.Context {
	if s.ctx != nil {
		return s.ctx
	}
	return context.Background()
}

func (s *casRestoreStream) httpClientForRequests() *http.Client {
	if s.httpClient != nil {
		return s.httpClient
	}
	return &http.Client{Timeout: 60 * time.Second}
}

func (s *casRestoreStream) waitLimit(ctx context.Context) error {
	if s.pan == nil {
		return nil
	}
	return s.pan.WaitLimit(ctx)
}

func sanitizeDownloadRequestError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		op := strings.TrimSpace(urlErr.Op)
		if op == "" {
			op = "request"
		}
		return fmt.Errorf("cas restore account download %s failed", op)
	}
	return fmt.Errorf("cas restore account download request failed")
}

func missingRecyclePasswordCleanupError() error {
	return fmt.Errorf("cas restore temp dir moved to recycle bin but purge was skipped: missing cas_recycle_password")
}

func (s *casRestoreStream) listDir(ctx context.Context, dirID string) ([]driver115.File, error) {
	if s.listDirFn != nil {
		return s.listDirFn(ctx, dirID)
	}
	if s.pan == nil || s.pan.client == nil {
		return nil, fmt.Errorf("cas restore missing 115 client")
	}
	files, err := s.pan.client.ListWithLimit(dirID, 1000, driver115.WithMultiUrls())
	if err != nil {
		return nil, err
	}
	return *files, nil
}

func (s *casRestoreStream) getFileByID(fileID string) (*FileObj, error) {
	if s.getFileByIDFn != nil {
		return s.getFileByIDFn(fileID)
	}
	if s.pan == nil {
		return nil, fmt.Errorf("cas restore missing 115 client")
	}
	return s.pan.getNewFile(fileID)
}

func (s *casRestoreStream) deleteTempDir(ctx context.Context, tempCID string) error {
	if s.deleteFn != nil {
		return s.deleteFn(ctx, tempCID)
	}
	if s.pan == nil || s.pan.client == nil {
		return fmt.Errorf("cas restore missing 115 client for cleanup")
	}
	return s.pan.client.Delete(tempCID)
}

func (s *casRestoreStream) waitRecycleIDs(ctx context.Context, pan *Pan115, tempCID, tempName string, maxWait time.Duration) ([]string, error) {
	if s.waitForRecycleIDsFn != nil {
		return s.waitForRecycleIDsFn(ctx, pan, tempCID, tempName, maxWait)
	}
	return waitForRecycleIDs(ctx, pan, tempCID, tempName, maxWait)
}

func (s *casRestoreStream) cleanRecycle(ctx context.Context, pan *Pan115, password string, recycleIDs []string, maxWait time.Duration) error {
	if s.cleanRecycleFn != nil {
		return s.cleanRecycleFn(ctx, pan, password, recycleIDs, maxWait)
	}
	return cleanRecycleBinWithRetry(ctx, pan, password, recycleIDs, maxWait)
}

func (s *casRestoreStream) userAgent() string {
	if strings.TrimSpace(s.ua) != "" {
		return s.ua
	}
	return "Mozilla/5.0 115Browser/" + appVer
}

func shareReferer(shareCode, receiveCode string) string {
	if strings.TrimSpace(receiveCode) == "" {
		return "https://115.com/s/" + url.PathEscape(shareCode)
	}
	return "https://115.com/s/" + url.PathEscape(shareCode) + "?password=" + url.QueryEscape(receiveCode)
}

func validate115ShareSource(source *casmeta.Source) error {
	if source == nil {
		return fmt.Errorf("cas restore 115_share source is missing")
	}
	if strings.TrimSpace(source.ShareCode) == "" || strings.TrimSpace(source.ReceiveCode) == "" || strings.TrimSpace(source.FileID) == "" {
		return fmt.Errorf("cas restore 115_share source is incomplete")
	}
	return nil
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

func waitForRecycleIDs(ctx context.Context, pan *Pan115, tempCID, tempName string, maxWait time.Duration) ([]string, error) {
	deadline := time.Now().Add(maxWait)
	for {
		ids, err := listRecycleIDsForTempDir(ctx, pan, tempCID, tempName)
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
	return nil, fmt.Errorf("cas restore temp dir %q (%s) did not appear in recycle bin before timeout", tempName, tempCID)
}

func listRecycleIDsForTempDir(ctx context.Context, pan *Pan115, tempCID, tempName string) ([]string, error) {
	if pan == nil || pan.client == nil {
		return nil, fmt.Errorf("cas restore missing 115 client for recycle lookup")
	}
	const pageSize = 200
	for offset := 0; ; offset += pageSize {
		if err := pan.WaitLimit(ctx); err != nil {
			return nil, err
		}
		items, err := pan.client.ListRecycleBin(offset, pageSize)
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

func cleanRecycleBinWithRetry(ctx context.Context, pan *Pan115, password string, recycleIDs []string, maxWait time.Duration) error {
	if len(recycleIDs) == 0 {
		return fmt.Errorf("missing recycle ids")
	}
	if pan == nil || pan.client == nil {
		return fmt.Errorf("cas restore missing 115 client for recycle cleanup")
	}
	deadline := time.Now().Add(maxWait)
	var lastErr error
	for {
		if err := pan.WaitLimit(ctx); err != nil {
			return err
		}
		err := pan.client.CleanRecycleBin(password, recycleIDs...)
		if err == nil {
			return nil
		}
		lastErr = err
		if maxWait <= 0 || time.Now().After(deadline) {
			break
		}
		timer := time.NewTimer(10 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return fmt.Errorf("clean recycle bin ids %s: %w", strings.Join(recycleIDs, ","), lastErr)
}

func combineRestoreAndCleanupError(restoreErr, cleanupErr error) error {
	if restoreErr == nil {
		return nil
	}
	if cleanupErr == nil {
		return restoreErr
	}
	return fmt.Errorf("%w; cleanup also failed: %v", restoreErr, cleanupErr)
}

func headerToMap(header http.Header) map[string]string {
	headers := make(map[string]string, len(header))
	for key, values := range header {
		if len(values) > 0 {
			headers[key] = values[0]
		}
	}
	return headers
}
