package webdav

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/onaonbir/Cloodsy-S3/db"
	"github.com/onaonbir/Cloodsy-S3/storage"
	"golang.org/x/net/webdav"
)

type S3FileSystem struct {
	db             *db.DB
	storage        *storage.FileSystem
	logger         *slog.Logger
	autoResizeHook AutoResizeHook // 自动缩放钩子（可选）
}

// AutoResizeHook 自动缩放钩子接口，由 handler.ImageProcessor 实现
// WebDAV 上传文件后通过此接口触发自动缩放，避免循环依赖
type AutoResizeHook interface {
	// SubmitAutoResizeTask 提交自动缩放任务（非阻塞）
	SubmitAutoResizeTask(bucketName, key, contentType string, size int64)
}

func NewS3FileSystem(database *db.DB, store *storage.FileSystem, logger *slog.Logger) *S3FileSystem {
	return &S3FileSystem{
		db:      database,
		storage: store,
		logger:  logger,
	}
}

// SetAutoResizeHook 设置自动缩放钩子
func (fs *S3FileSystem) SetAutoResizeHook(hook AutoResizeHook) {
	fs.autoResizeHook = hook
}

func parseWebDAVPath(p string) (bucket, key string) {
	p = path.Clean(p)
	p = strings.TrimPrefix(p, "/")
	if p == "" || p == "." {
		return "", ""
	}
	idx := strings.IndexByte(p, '/')
	if idx < 0 {
		return p, ""
	}
	return p[:idx], p[idx+1:]
}

func (fs *S3FileSystem) isVirtualDir(bucketID int64, key string) bool {
	prefix := key + "/"
	objects, commonPrefixes, _, _, err := fs.db.ListObjectsMeta(bucketID, prefix, "", "/", 1)
	if err != nil {
		fs.logger.Error("webdav: isVirtualDir check failed", "bucketID", bucketID, "key", key, "error", err)
		return false
	}
	return len(objects) > 0 || len(commonPrefixes) > 0
}

func (fs *S3FileSystem) isDirPath(bucketID int64, key string) bool {
	dirMarkerMeta, err := fs.db.GetObjectMeta(bucketID, key+"/")
	if err != nil {
		fs.logger.Error("webdav: isDirPath dir marker check failed", "bucketID", bucketID, "key", key, "error", err)
	} else if dirMarkerMeta != nil && !dirMarkerMeta.IsDeleteMarker {
		return true
	}
	return fs.isVirtualDir(bucketID, key)
}

func (fs *S3FileSystem) Mkdir(ctx context.Context, name string, perm os.FileMode) error {
	bucket, key := parseWebDAVPath(name)
	if bucket == "" {
		return os.ErrPermission
	}

	b, err := fs.db.GetBucket(bucket)
	if err != nil {
		return fmt.Errorf("mkdir: get bucket: %w", err)
	}
	if b == nil {
		return os.ErrNotExist
	}

	if key == "" {
		return nil
	}

	dirKey := key
	if !strings.HasSuffix(dirKey, "/") {
		dirKey += "/"
	}

	reader := strings.NewReader("")
	size, etag, err := fs.storage.PutObject(bucket, dirKey, reader)
	if err != nil {
		return fmt.Errorf("mkdir: put directory marker: %w", err)
	}

	meta := &db.ObjectMeta{
		BucketID:     b.ID,
		Key:          dirKey,
		Size:         size,
		ETag:         etag,
		ContentType:  "application/x-directory",
		LastModified: time.Now(),
		Metadata:     "{}",
		IsLatest:     true,
	}
	if err := fs.db.PutObjectMeta(meta); err != nil {
		return fmt.Errorf("mkdir: put directory marker meta: %w", err)
	}

	fs.logger.Debug("webdav: created directory marker", "bucket", bucket, "key", dirKey)
	return nil
}

func (fs *S3FileSystem) OpenFile(ctx context.Context, name string, flag int, perm os.FileMode) (webdav.File, error) {
	bucket, key := parseWebDAVPath(name)

	if bucket == "" {
		return fs.listBucketsAsDir()
	}

	b, err := fs.db.GetBucket(bucket)
	if err != nil {
		return nil, fmt.Errorf("openfile: get bucket: %w", err)
	}
	if b == nil {
		return nil, os.ErrNotExist
	}

	if key == "" {
		return fs.listBucketObjectsAsDir(bucket, b.ID, "")
	}

	if strings.HasSuffix(key, "/") {
		return fs.listBucketObjectsAsDir(bucket, b.ID, strings.TrimSuffix(key, "/"))
	}

	meta, err := fs.db.GetObjectMeta(b.ID, key)
	if err != nil {
		return nil, fmt.Errorf("openfile: get object meta: %w", err)
	}
	if meta != nil && !meta.IsDeleteMarker {
		return &s3File{
			bucket:      bucket,
			key:         key,
			bucketID:    b.ID,
			fs:          fs,
			data:        nil,
			dataLoaded:  false,
			isNew:       false,
			lastMod:     meta.LastModified,
			contentType: meta.ContentType,
			size:        meta.Size,
		}, nil
	}

	if fs.isDirPath(b.ID, key) {
		return fs.listBucketObjectsAsDir(bucket, b.ID, key)
	}

	if flag&os.O_CREATE != 0 || flag&os.O_TRUNC != 0 {
		return &s3File{
			bucket:      bucket,
			key:         key,
			bucketID:    b.ID,
			fs:          fs,
			isNew:       true,
			lastMod:     time.Now(),
			contentType: "application/octet-stream",
		}, nil
	}

	return nil, os.ErrNotExist
}

func (fs *S3FileSystem) RemoveAll(ctx context.Context, name string) error {
	bucket, key := parseWebDAVPath(name)
	if bucket == "" {
		return os.ErrPermission
	}

	b, err := fs.db.GetBucket(bucket)
	if err != nil {
		return fmt.Errorf("removeall: get bucket: %w", err)
	}
	if b == nil {
		return os.ErrNotExist
	}

	if key == "" {
		return os.ErrPermission
	}

	if strings.HasSuffix(key, "/") {
		return fs.removeAllUnderPrefix(bucket, b.ID, key)
	}

	fileMeta, err := fs.db.GetObjectMeta(b.ID, key)
	if err != nil {
		return fmt.Errorf("removeall: get object meta: %w", err)
	}
	if fileMeta != nil && !fileMeta.IsDeleteMarker {
		if err := fs.storage.DeleteObject(bucket, key); err != nil {
			return fmt.Errorf("removeall: delete object: %w", err)
		}
		if err := fs.db.DeleteObjectMeta(b.ID, key); err != nil {
			return fmt.Errorf("removeall: delete object meta: %w", err)
		}
		fs.logger.Info("webdav: removed file", "bucket", bucket, "key", key)
		return nil
	}

	if fs.isDirPath(b.ID, key) {
		return fs.removeAllUnderPrefix(bucket, b.ID, key+"/")
	}

	return nil
}

func (fs *S3FileSystem) removeAllUnderPrefix(bucket string, bucketID int64, prefix string) error {
	objects, _, _, _, err := fs.db.ListObjectsMeta(bucketID, prefix, "", "", 10000)
	if err != nil {
		return fmt.Errorf("removeall: list objects: %w", err)
	}

	dirMarkerMeta, err := fs.db.GetObjectMeta(bucketID, prefix)
	if err == nil && dirMarkerMeta != nil {
		alreadyInList := false
		for _, obj := range objects {
			if obj.Key == prefix {
				alreadyInList = true
				break
			}
		}
		if !alreadyInList {
			objects = append(objects, *dirMarkerMeta)
		}
	}

	for _, obj := range objects {
		if err := fs.storage.DeleteObject(bucket, obj.Key); err != nil {
			fs.logger.Error("webdav: failed to delete object", "bucket", bucket, "key", obj.Key, "error", err)
		}
		if err := fs.db.DeleteObjectMeta(bucketID, obj.Key); err != nil {
			fs.logger.Error("webdav: failed to delete object meta", "bucket", bucket, "key", obj.Key, "error", err)
		}
	}
	fs.logger.Info("webdav: removed directory", "bucket", bucket, "prefix", prefix, "objects", len(objects))
	return nil
}

func (fs *S3FileSystem) Rename(ctx context.Context, oldName, newName string) error {
	oldBucket, oldKey := parseWebDAVPath(oldName)
	newBucket, newKey := parseWebDAVPath(newName)

	fs.logger.Info("webdav: Rename", "oldName", oldName, "newName", newName, "oldBucket", oldBucket, "oldKey", oldKey, "newBucket", newBucket, "newKey", newKey)

	if oldBucket == "" || newBucket == "" {
		return os.ErrPermission
	}
	if oldBucket != newBucket {
		return os.ErrPermission
	}
	if oldKey == "" || newKey == "" {
		return os.ErrPermission
	}

	b, err := fs.db.GetBucket(oldBucket)
	if err != nil {
		return fmt.Errorf("rename: get bucket: %w", err)
	}
	if b == nil {
		return os.ErrNotExist
	}

	fileMeta, err := fs.db.GetObjectMeta(b.ID, oldKey)
	if err != nil {
		return fmt.Errorf("rename: get object meta: %w", err)
	}

	if fileMeta != nil && !fileMeta.IsDeleteMarker {
		return fs.renameFile(b, oldBucket, oldKey, newBucket, newKey, fileMeta)
	}

	if fs.isDirPath(b.ID, oldKey) {
		return fs.renameDirectory(b, oldBucket, oldKey, newBucket, newKey)
	}

	return os.ErrNotExist
}

func (fs *S3FileSystem) renameFile(b *db.Bucket, oldBucket, oldKey, newBucket, newKey string, meta *db.ObjectMeta) error {
	rc, err := fs.storage.GetObject(oldBucket, oldKey)
	if err != nil {
		return fmt.Errorf("rename file: get object: %w", err)
	}
	defer rc.Close()

	size, etag, err := fs.storage.PutObject(newBucket, newKey, rc)
	if err != nil {
		return fmt.Errorf("rename file: put object: %w", err)
	}

	newMeta := &db.ObjectMeta{
		BucketID:     b.ID,
		Key:          newKey,
		Size:         size,
		ETag:         etag,
		ContentType:  meta.ContentType,
		LastModified: time.Now(),
		Metadata:     meta.Metadata,
		IsLatest:     true,
	}
	if err := fs.db.PutObjectMeta(newMeta); err != nil {
		return fmt.Errorf("rename file: put object meta: %w", err)
	}

	if err := fs.storage.DeleteObject(oldBucket, oldKey); err != nil {
		fs.logger.Error("webdav: rename file: failed to delete old object", "bucket", oldBucket, "key", oldKey, "error", err)
	}
	if err := fs.db.DeleteObjectMeta(b.ID, oldKey); err != nil {
		fs.logger.Error("webdav: rename file: failed to delete old object meta", "bucket", oldBucket, "key", oldKey, "error", err)
	}

	fs.logger.Info("webdav: renamed file", "bucket", oldBucket, "oldKey", oldKey, "newKey", newKey)
	return nil
}

func (fs *S3FileSystem) renameDirectory(b *db.Bucket, oldBucket, oldKey, newBucket, newKey string) error {
	oldPrefix := oldKey + "/"

	objects, _, _, _, err := fs.db.ListObjectsMeta(b.ID, oldPrefix, "", "", 10000)
	if err != nil {
		return fmt.Errorf("rename dir: list objects: %w", err)
	}

	dirMarkerMeta, _ := fs.db.GetObjectMeta(b.ID, oldPrefix)
	if dirMarkerMeta != nil && !dirMarkerMeta.IsDeleteMarker {
		alreadyInList := false
		for _, obj := range objects {
			if obj.Key == oldPrefix {
				alreadyInList = true
				break
			}
		}
		if !alreadyInList {
			objects = append(objects, *dirMarkerMeta)
		}
	}

	if len(objects) == 0 {
		reader := strings.NewReader("")
		size, etag, err := fs.storage.PutObject(newBucket, newKey+"/", reader)
		if err != nil {
			return fmt.Errorf("rename dir: create new dir marker: %w", err)
		}
		newDirMeta := &db.ObjectMeta{
			BucketID:     b.ID,
			Key:          newKey + "/",
			Size:         size,
			ETag:         etag,
			ContentType:  "application/x-directory",
			LastModified: time.Now(),
			Metadata:     "{}",
			IsLatest:     true,
		}
		if err := fs.db.PutObjectMeta(newDirMeta); err != nil {
			return fmt.Errorf("rename dir: put new dir marker meta: %w", err)
		}
		fs.logger.Info("webdav: renamed empty directory", "bucket", oldBucket, "oldKey", oldKey, "newKey", newKey)
		return nil
	}

	for _, obj := range objects {
		relativePath := strings.TrimPrefix(obj.Key, oldPrefix)
		newObjKey := newKey + "/" + relativePath

		isDirMarker := strings.HasSuffix(obj.Key, "/")

		rc, err := fs.storage.GetObject(oldBucket, obj.Key)
		if err != nil {
			if isDirMarker {
				reader := strings.NewReader("")
				size, etag, putErr := fs.storage.PutObject(newBucket, newObjKey, reader)
				if putErr != nil {
					fs.logger.Error("webdav: rename dir: create dir marker", "newKey", newObjKey, "error", putErr)
					continue
				}
				newObjMeta := &db.ObjectMeta{
					BucketID:     b.ID,
					Key:          newObjKey,
					Size:         size,
					ETag:         etag,
					ContentType:  "application/x-directory",
					LastModified: time.Now(),
					Metadata:     "{}",
					IsLatest:     true,
				}
				if putErr := fs.db.PutObjectMeta(newObjMeta); putErr != nil {
					fs.logger.Error("webdav: rename dir: put dir marker meta", "newKey", newObjKey, "error", putErr)
				}
			} else {
				fs.logger.Error("webdav: rename dir: get object failed, skipping", "key", obj.Key, "error", err)
			}
			continue
		}

		size, etag, err := fs.storage.PutObject(newBucket, newObjKey, rc)
		rc.Close()
		if err != nil {
			fs.logger.Error("webdav: rename dir: put object", "newKey", newObjKey, "error", err)
			continue
		}

		contentType := obj.ContentType
		if isDirMarker && contentType == "" {
			contentType = "application/x-directory"
		}
		newObjMeta := &db.ObjectMeta{
			BucketID:     b.ID,
			Key:          newObjKey,
			Size:         size,
			ETag:         etag,
			ContentType:  contentType,
			LastModified: time.Now(),
			Metadata:     obj.Metadata,
			IsLatest:     true,
		}
		if err := fs.db.PutObjectMeta(newObjMeta); err != nil {
			fs.logger.Error("webdav: rename dir: put object meta", "newKey", newObjKey, "error", err)
		}
	}

	for _, obj := range objects {
		if err := fs.storage.DeleteObject(oldBucket, obj.Key); err != nil {
			fs.logger.Error("webdav: rename dir: delete old object", "key", obj.Key, "error", err)
		}
		if err := fs.db.DeleteObjectMeta(b.ID, obj.Key); err != nil {
			fs.logger.Error("webdav: rename dir: delete old object meta", "key", obj.Key, "error", err)
		}
	}

	fs.logger.Info("webdav: renamed directory", "bucket", oldBucket, "oldKey", oldKey, "newKey", newKey, "objects", len(objects))
	return nil
}

func (fs *S3FileSystem) Stat(ctx context.Context, name string) (os.FileInfo, error) {
	bucket, key := parseWebDAVPath(name)

	if bucket == "" {
		return &dirInfo{name: "/", modTime: time.Now()}, nil
	}

	b, err := fs.db.GetBucket(bucket)
	if err != nil {
		return nil, fmt.Errorf("stat: get bucket: %w", err)
	}
	if b == nil {
		fs.logger.Debug("webdav: Stat bucket not found", "bucket", bucket)
		return nil, os.ErrNotExist
	}

	if key == "" {
		return &dirInfo{name: bucket, modTime: b.CreatedAt}, nil
	}

	if strings.HasSuffix(key, "/") {
		return &dirInfo{name: filepath.Base(strings.TrimSuffix(key, "/")), modTime: time.Now()}, nil
	}

	meta, err := fs.db.GetObjectMeta(b.ID, key)
	if err != nil {
		return nil, fmt.Errorf("stat: get object meta: %w", err)
	}
	if meta != nil && !meta.IsDeleteMarker {
		return &fileInfo{
			name:    filepath.Base(key),
			size:    meta.Size,
			modTime: meta.LastModified,
			isDir:   false,
		}, nil
	}

	dirMarkerMeta, err := fs.db.GetObjectMeta(b.ID, key+"/")
	if err != nil {
		return nil, fmt.Errorf("stat: get dir marker meta: %w", err)
	}
	if dirMarkerMeta != nil && !dirMarkerMeta.IsDeleteMarker {
		return &dirInfo{name: filepath.Base(key), modTime: dirMarkerMeta.LastModified}, nil
	}

	if fs.isVirtualDir(b.ID, key) {
		return &dirInfo{name: filepath.Base(key), modTime: time.Now()}, nil
	}

	fs.logger.Debug("webdav: Stat not found", "bucket", bucket, "key", key)
	return nil, os.ErrNotExist
}

func (fs *S3FileSystem) listBucketsAsDir() (webdav.File, error) {
	buckets, err := fs.db.ListBuckets()
	if err != nil {
		return nil, fmt.Errorf("list buckets: %w", err)
	}

	entries := make([]os.FileInfo, 0, len(buckets))
	for _, b := range buckets {
		entries = append(entries, &dirInfo{name: b.Name, modTime: b.CreatedAt})
	}
	return &virtualDir{name: "/", entries: entries}, nil
}

func (fs *S3FileSystem) listBucketObjectsAsDir(bucket string, bucketID int64, prefix string) (webdav.File, error) {
	listPrefix := prefix
	if listPrefix != "" {
		listPrefix += "/"
	}
	objects, commonPrefixes, _, _, err := fs.db.ListObjectsMeta(bucketID, listPrefix, "", "/", 10000)
	if err != nil {
		return nil, fmt.Errorf("list objects: %w", err)
	}

	entries := make([]os.FileInfo, 0, len(objects)+len(commonPrefixes))

	for _, cp := range commonPrefixes {
		trimmed := strings.TrimPrefix(cp, listPrefix)
		entries = append(entries, &dirInfo{name: strings.TrimSuffix(trimmed, "/"), modTime: time.Now()})
	}

	for _, obj := range objects {
		name := strings.TrimPrefix(obj.Key, listPrefix)
		if name == "" {
			continue
		}
		if strings.HasSuffix(name, "/") {
			entries = append(entries, &dirInfo{name: strings.TrimSuffix(name, "/"), modTime: obj.LastModified})
			continue
		}
		entries = append(entries, &fileInfo{
			name:    name,
			size:    obj.Size,
			modTime: obj.LastModified,
			isDir:   false,
		})
	}

	dirName := bucket
	if prefix != "" {
		dirName = filepath.Base(prefix)
	}
	return &virtualDir{name: dirName, entries: entries}, nil
}

type s3File struct {
	bucket      string
	key         string
	bucketID    int64
	fs          *S3FileSystem
	data        []byte
	dataLoaded  bool // 标记数据是否已从存储加载
	isNew       bool
	offset      int64
	lastMod     time.Time
	contentType string
	size        int64
}

func (f *s3File) Close() error {
	if f.isNew && f.data != nil {
		reader := strings.NewReader(string(f.data))
		size, etag, err := f.fs.storage.PutObject(f.bucket, f.key, reader)
		if err != nil {
			return fmt.Errorf("close: put object: %w", err)
		}

		metadataStr := "{}"
		writeMeta := func() {
			if b, err := json.Marshal(map[string]string{}); err == nil {
				metadataStr = string(b)
			}
		}
		writeMeta()

		// 当 Content-Type 为通用二进制类型时，根据文件扩展名推断更精确的 MIME 类型
		contentType := f.contentType
		if contentType == "" || contentType == "application/octet-stream" {
			if guessed := mime.TypeByExtension(filepath.Ext(f.key)); guessed != "" {
				contentType = guessed
			}
		}

		meta := &db.ObjectMeta{
			BucketID:     f.bucketID,
			Key:          f.key,
			Size:         size,
			ETag:         etag,
			ContentType:  contentType,
			LastModified: time.Now(),
			Metadata:     metadataStr,
			IsLatest:     true,
		}
		if err := f.fs.db.PutObjectMeta(meta); err != nil {
			return fmt.Errorf("close: put object meta: %w", err)
		}
		f.isNew = false

		// 自动缩放：如果设置了自动缩放钩子且文件是大图片，触发异步缩放
		if f.fs.autoResizeHook != nil {
			f.fs.autoResizeHook.SubmitAutoResizeTask(f.bucket, f.key, contentType, size)
		}
	}
	return nil
}

func (f *s3File) Read(p []byte) (int, error) {
	// 延迟加载：首次 Read 时才从存储读取文件数据
	if !f.dataLoaded && !f.isNew {
		rc, err := f.fs.storage.GetObject(f.bucket, f.key)
		if err != nil {
			return 0, fmt.Errorf("read: get object: %w", err)
		}
		defer rc.Close()
		f.data, err = io.ReadAll(rc)
		if err != nil {
			return 0, fmt.Errorf("read: read object: %w", err)
		}
		f.dataLoaded = true
		f.size = int64(len(f.data))
	}
	if f.data == nil {
		return 0, io.EOF
	}
	if f.offset >= int64(len(f.data)) {
		return 0, io.EOF
	}
	n := copy(p, f.data[f.offset:])
	f.offset += int64(n)
	return n, nil
}

func (f *s3File) Write(p []byte) (int, error) {
	if !f.isNew {
		f.isNew = true
		f.data = nil
	}
	f.data = append(f.data, p...)
	f.size = int64(len(f.data))
	return len(p), nil
}

func (f *s3File) Seek(offset int64, whence int) (int64, error) {
	var newOffset int64
	switch whence {
	case io.SeekStart:
		newOffset = offset
	case io.SeekCurrent:
		newOffset = f.offset + offset
	case io.SeekEnd:
		if f.data != nil {
			newOffset = int64(len(f.data)) + offset
		} else {
			newOffset = f.size + offset
		}
	default:
		return 0, fmt.Errorf("invalid whence: %d", whence)
	}
	if newOffset < 0 {
		return 0, fmt.Errorf("negative position")
	}
	f.offset = newOffset
	return newOffset, nil
}

func (f *s3File) Readdir(count int) ([]os.FileInfo, error) {
	return nil, fmt.Errorf("readdir not supported on files")
}

func (f *s3File) Stat() (os.FileInfo, error) {
	size := f.size
	if f.data != nil {
		size = int64(len(f.data))
	}
	return &fileInfo{
		name:    filepath.Base(f.key),
		size:    size,
		modTime: f.lastMod,
		isDir:   false,
	}, nil
}

type virtualDir struct {
	name    string
	entries []os.FileInfo
	offset  int
}

func (d *virtualDir) Close() error                                 { return nil }
func (d *virtualDir) Read(p []byte) (int, error)                   { return 0, io.EOF }
func (d *virtualDir) Write(p []byte) (int, error)                  { return 0, fmt.Errorf("write on directory") }
func (d *virtualDir) Seek(offset int64, whence int) (int64, error) { return 0, nil }

func (d *virtualDir) Readdir(count int) ([]os.FileInfo, error) {
	if d.offset >= len(d.entries) {
		if count <= 0 {
			return []os.FileInfo{}, nil
		}
		return nil, io.EOF
	}
	remaining := d.entries[d.offset:]
	if count <= 0 {
		d.offset = len(d.entries)
		return remaining, nil
	}
	if count > len(remaining) {
		count = len(remaining)
	}
	result := remaining[:count]
	d.offset += count
	if d.offset >= len(d.entries) {
		return result, io.EOF
	}
	return result, nil
}

func (d *virtualDir) Stat() (os.FileInfo, error) {
	return &dirInfo{name: d.name, modTime: time.Now()}, nil
}

type fileInfo struct {
	name    string
	size    int64
	modTime time.Time
	isDir   bool
}

func (fi *fileInfo) Name() string       { return fi.name }
func (fi *fileInfo) Size() int64        { return fi.size }
func (fi *fileInfo) Mode() os.FileMode  { return 0644 }
func (fi *fileInfo) ModTime() time.Time { return fi.modTime }
func (fi *fileInfo) IsDir() bool        { return fi.isDir }
func (fi *fileInfo) Sys() interface{}   { return nil }

type dirInfo struct {
	name    string
	modTime time.Time
}

func (di *dirInfo) Name() string       { return di.name }
func (di *dirInfo) Size() int64        { return 0 }
func (di *dirInfo) Mode() os.FileMode  { return os.ModeDir | 0755 }
func (di *dirInfo) ModTime() time.Time { return di.modTime }
func (di *dirInfo) IsDir() bool        { return true }
func (di *dirInfo) Sys() interface{}   { return nil }

type BasicAuthHandler struct {
	handler http.Handler
	db      *db.DB
	logger  *slog.Logger
}

func NewBasicAuthHandler(handler http.Handler, database *db.DB, logger *slog.Logger) *BasicAuthHandler {
	return &BasicAuthHandler{
		handler: handler,
		db:      database,
		logger:  logger,
	}
}

func (h *BasicAuthHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	username, password, ok := r.BasicAuth()
	if !ok {
		w.Header().Set("WWW-Authenticate", `Basic realm="Cloodsy-S3 WebDAV"`)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	cred, err := h.db.GetCredentialByAccessKey(username)
	if err != nil {
		h.logger.Error("webdav auth: db error", "error", err)
		w.Header().Set("WWW-Authenticate", `Basic realm="Cloodsy-S3 WebDAV"`)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if cred == nil || cred.SecretKey != password {
		w.Header().Set("WWW-Authenticate", `Basic realm="Cloodsy-S3 WebDAV"`)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	h.handler.ServeHTTP(w, r)
}

func NewHandler(database *db.DB, store *storage.FileSystem, logger *slog.Logger, prefix string, autoResizeHook ...AutoResizeHook) http.Handler {
	s3fs := NewS3FileSystem(database, store, logger)

	// 如果传入了自动缩放钩子，设置到文件系统
	if len(autoResizeHook) > 0 && autoResizeHook[0] != nil {
		s3fs.SetAutoResizeHook(autoResizeHook[0])
	}

	davHandler := &webdav.Handler{
		Prefix:     prefix,
		FileSystem: s3fs,
		LockSystem: webdav.NewMemLS(),
		Logger: func(r *http.Request, err error) {
			if err == nil {
				return
			}
			if errors.Is(err, os.ErrNotExist) {
				logger.Info("webdav: resource not found", "method", r.Method, "path", r.URL.Path)
				return
			}
			logger.Error("webdav error", "method", r.Method, "path", r.URL.Path, "error", err)
		},
	}

	return NewBasicAuthHandler(davHandler, database, logger)
}
