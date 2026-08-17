package blob

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

type R2 struct {
	bucket     string
	prefix     string
	client     *s3.Client
	uploader   *manager.Uploader
	cacheDir   string
	mu         sync.Mutex
	readyAt    time.Time
	readyErr   error
	cacheMu    sync.Mutex
	cacheLocks map[string]*cacheLock
}

func NewR2(opts R2Options) (*R2, error) {
	account := strings.TrimSpace(opts.AccountID)
	access := strings.TrimSpace(opts.AccessKey)
	secret := strings.TrimSpace(opts.Secret)
	bucket := strings.TrimSpace(opts.Bucket)
	if account == "" || access == "" || secret == "" || bucket == "" {
		return nil, fmt.Errorf("R2_ACCOUNT_ID, R2_ACCESS_KEY_ID, R2_SECRET_ACCESS_KEY, and R2_BUCKET are required")
	}
	endpoint := strings.TrimRight(strings.TrimSpace(opts.Endpoint), "/")
	if endpoint == "" {
		endpoint = "https://" + account + ".r2.cloudflarestorage.com"
	}
	cacheDir := filepath.Join(os.TempDir(), "ocr-r2")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return nil, fmt.Errorf("r2 cache: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(access, secret, "")),
		awsconfig.WithRegion("auto"),
		awsconfig.WithRequestChecksumCalculation(aws.RequestChecksumCalculationWhenRequired),
		awsconfig.WithResponseChecksumValidation(aws.ResponseChecksumValidationWhenRequired),
		awsconfig.WithHTTPClient(&http.Client{
			Timeout: 0,
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				MaxIdleConns:          8,
				MaxIdleConnsPerHost:   4,
				IdleConnTimeout:       90 * time.Second,
				TLSHandshakeTimeout:   5 * time.Second,
				ExpectContinueTimeout: 1 * time.Second,
				ForceAttemptHTTP2:     true,
			},
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("r2 config: %w", err)
	}

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true
		o.RetryMaxAttempts = 5
		o.RetryMode = aws.RetryModeStandard
	})
	return &R2{
		bucket: bucket,
		prefix: strings.Trim(strings.ReplaceAll(strings.TrimSpace(opts.Prefix), "\\", "/"), "/"),
		client: client,
		uploader: manager.NewUploader(client, func(u *manager.Uploader) {
			u.PartSize = 8 << 20
			u.Concurrency = 1
			u.LeavePartsOnError = false
		}),
		cacheDir:   cacheDir,
		cacheLocks: make(map[string]*cacheLock),
	}, nil
}

// cacheLock serialises access to one cached object. Entries are reference
// counted so the table cannot grow with every object the node has ever touched.
type cacheLock struct {
	mu   sync.Mutex
	refs int
}

func (r *R2) lockCache(path string) func() {
	r.cacheMu.Lock()
	m := r.cacheLocks[path]
	if m == nil {
		m = &cacheLock{}
		r.cacheLocks[path] = m
	}
	m.refs++
	r.cacheMu.Unlock()

	m.mu.Lock()
	var once sync.Once
	return func() {
		once.Do(func() {
			m.mu.Unlock()
			r.cacheMu.Lock()
			m.refs--
			if m.refs == 0 {
				delete(r.cacheLocks, path)
			}
			r.cacheMu.Unlock()
		})
	}
}

func (r *R2) Driver() string { return "r2" }

func (r *R2) Ready(ctx context.Context) error {
	r.mu.Lock()
	if time.Since(r.readyAt) < 5*time.Second {
		err := r.readyErr
		r.mu.Unlock()
		return err
	}
	r.mu.Unlock()

	if _, has := ctx.Deadline(); !has {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 4*time.Second)
		defer cancel()
	}
	_, err := r.client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(r.bucket)})
	r.mu.Lock()
	r.readyAt = time.Now()
	r.readyErr = err
	r.mu.Unlock()
	if err != nil {
		return fmt.Errorf("r2 bucket: %w", err)
	}
	return nil
}

func (r *R2) Put(ctx context.Context, key string, reader io.Reader, _ int64, contentType string) error {
	obj, err := r.objectKey(key)
	if err != nil {
		return err
	}
	if contentType == "" {
		contentType = contentTypeFor(key)
	}
	cache := r.cachePath(obj)
	unlock := r.lockCache(cache)
	defer unlock()
	if err := os.MkdirAll(filepath.Dir(cache), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(cache), ".r2-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		_ = tmp.Close()
		if !ok {
			_ = os.Remove(tmpName)
		}
	}()

	if _, has := ctx.Deadline(); !has {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
	}
	body := io.TeeReader(reader, tmp)
	filename := filepath.Base(key)
	_, err = r.uploader.Upload(ctx, &s3.PutObjectInput{
		Bucket:             aws.String(r.bucket),
		Key:                aws.String(obj),
		Body:               body,
		ContentType:        aws.String(contentType),
		ContentDisposition: aws.String(fmt.Sprintf(`inline; filename=%q`, filename)),
		CacheControl:       aws.String("private, max-age=31536000, immutable"),
	})
	if err != nil {
		return fmt.Errorf("r2 put: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, cache); err != nil {
		return err
	}
	ok = true
	r.trimCache()
	return nil
}

func (r *R2) Open(ctx context.Context, key string) (io.ReadCloser, int64, string, error) {
	obj, err := r.objectKey(key)
	if err != nil {
		return nil, 0, "", err
	}
	var cancel context.CancelFunc
	if _, has := ctx.Deadline(); !has {
		ctx, cancel = context.WithTimeout(ctx, 2*time.Minute)
	}
	out, err := r.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(r.bucket),
		Key:    aws.String(obj),
	})
	if err != nil {
		if cancel != nil {
			cancel()
		}
		return nil, 0, "", mapR2GetErr(err)
	}
	size := int64(0)
	if out.ContentLength != nil {
		size = *out.ContentLength
	}
	ctype := contentTypeFor(key)
	if out.ContentType != nil && strings.TrimSpace(*out.ContentType) != "" {
		ctype = *out.ContentType
	}
	body := out.Body
	if cancel != nil {
		body = &bodyWithCancel{ReadCloser: out.Body, cancel: cancel}
	}
	return body, size, ctype, nil
}

func (r *R2) LocalPath(ctx context.Context, key string) (string, error) {
	obj, err := r.objectKey(key)
	if err != nil {
		return "", err
	}
	cache := r.cachePath(obj)
	unlock := r.lockCache(cache)
	defer unlock()
	if info, err := os.Stat(cache); err == nil && info.Size() > 0 {
		return cache, nil
	}
	_ = os.Remove(cache)
	rc, size, _, err := r.Open(ctx, key)
	if err != nil {
		return "", err
	}
	defer rc.Close()
	if err := os.MkdirAll(filepath.Dir(cache), 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(cache), ".r2-*")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		_ = tmp.Close()
		if !ok {
			_ = os.Remove(tmpName)
		}
	}()
	wrote, err := io.Copy(tmp, rc)
	if err != nil {
		return "", err
	}
	if size > 0 && wrote != size {
		return "", fmt.Errorf("r2 cache incomplete: got %d want %d", wrote, size)
	}
	if wrote <= 0 {
		return "", ErrNotFound
	}
	if err := tmp.Sync(); err != nil {
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmpName, cache); err != nil {
		return "", err
	}
	ok = true
	r.trimCache()
	return cache, nil
}

type bodyWithCancel struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *bodyWithCancel) Close() error {
	err := b.ReadCloser.Close()
	if b.cancel != nil {
		b.cancel()
	}
	return err
}

func mapR2GetErr(err error) error {
	var nsk *types.NoSuchKey
	if errors.As(err, &nsk) {
		return ErrNotFound
	}
	var nf *types.NotFound
	if errors.As(err, &nf) {
		return ErrNotFound
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "nosuchkey") || strings.Contains(msg, "not found") {
		return ErrNotFound
	}
	return fmt.Errorf("r2 get: %w", err)
}

func (r *R2) hasObject(ctx context.Context, key string) (bool, error) {
	obj, err := r.objectKey(key)
	if err != nil {
		return false, err
	}
	_, err = r.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(r.bucket),
		Key:    aws.String(obj),
	})
	if err == nil {
		return true, nil
	}
	if errors.Is(mapR2GetErr(err), ErrNotFound) {
		return false, nil
	}
	return false, err
}

func (r *R2) ImportDir(ctx context.Context, root string) (int, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return 0, nil
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return 0, nil
	}
	copied := 0
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		key := filepath.ToSlash(rel)
		exists, err := r.hasObject(ctx, key)
		if err != nil {
			return err
		}
		if exists {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		st, _ := f.Stat()
		putErr := r.Put(ctx, key, f, st.Size(), contentTypeFor(key))
		_ = f.Close()
		if putErr != nil {
			return putErr
		}
		copied++
		return nil
	})
	return copied, err
}

func (r *R2) Delete(ctx context.Context, key string) error {
	obj, err := r.objectKey(key)
	if err != nil {
		return err
	}
	if _, has := ctx.Deadline(); !has {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
	}
	cache := r.cachePath(obj)
	unlock := r.lockCache(cache)
	defer unlock()
	_, err = r.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(r.bucket),
		Key:    aws.String(obj),
	})
	_ = os.Remove(cache)
	if err != nil {
		return fmt.Errorf("r2 delete: %w", err)
	}
	return nil
}

func (r *R2) PurgeAll(ctx context.Context) (int, error) {
	if _, has := ctx.Deadline(); !has {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
	}
	removed := 0
	var token *string
	for {
		out, err := r.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            aws.String(r.bucket),
			ContinuationToken: token,
			MaxKeys:           aws.Int32(1000),
		})
		if err != nil {
			return removed, fmt.Errorf("r2 list: %w", err)
		}
		if len(out.Contents) == 0 {
			if aws.ToBool(out.IsTruncated) && out.NextContinuationToken != nil {
				token = out.NextContinuationToken
				continue
			}
			break
		}
		objs := make([]types.ObjectIdentifier, 0, len(out.Contents))
		for _, item := range out.Contents {
			if item.Key == nil || *item.Key == "" {
				continue
			}
			objs = append(objs, types.ObjectIdentifier{Key: item.Key})
			_ = os.Remove(r.cachePath(*item.Key))
		}
		if len(objs) > 0 {
			if _, err := r.client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
				Bucket: aws.String(r.bucket),
				Delete: &types.Delete{Objects: objs, Quiet: aws.Bool(true)},
			}); err != nil {
				return removed, fmt.Errorf("r2 purge: %w", err)
			}
			removed += len(objs)
		}
		if !aws.ToBool(out.IsTruncated) || out.NextContinuationToken == nil {
			break
		}
		token = out.NextContinuationToken
	}
	return removed, nil
}

func (r *R2) objectKey(key string) (string, error) {
	clean, err := cleanKey(key)
	if err != nil {
		return "", err
	}
	return joinPrefix(r.prefix, clean), nil
}

func (r *R2) cachePath(objKey string) string {
	sum := sha256.Sum256([]byte(objKey))
	ext := filepath.Ext(objKey)
	if len(ext) > 8 {
		ext = ""
	}
	return filepath.Join(r.cacheDir, hex.EncodeToString(sum[:])+ext)
}

const maxCacheBytes int64 = 64 << 20

func (r *R2) trimCache() {
	type item struct {
		path string
		size int64
		mod  time.Time
	}
	var files []item
	var total int64
	_ = filepath.WalkDir(r.cacheDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		files = append(files, item{path: path, size: info.Size(), mod: info.ModTime()})
		total += info.Size()
		return nil
	})
	if total <= maxCacheBytes {
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.Before(files[j].mod) })
	want := maxCacheBytes * 3 / 4
	for _, f := range files {
		if total <= want {
			return
		}
		if time.Since(f.mod) < 30*time.Second {
			continue
		}
		if os.Remove(f.path) == nil {
			total -= f.size
		}
	}
}
