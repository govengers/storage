//go:build e2e

// Package e2e drives the public API the way a service wires it: every backend behind an
// ObservedStore with Prometheus metrics and OpenTelemetry spans, S3 over real HTTP on
// loopback against the in-process fake, and an upload and download path through net/http
// with content sniffing, a size cap and a digest.
package e2e

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/govengers/storage"
	"github.com/govengers/storage/filesystem"
	"github.com/govengers/storage/internal/s3fake"
	"github.com/govengers/storage/oteladapter"
	"github.com/govengers/storage/prometheusadapter"
	"github.com/govengers/storage/s3adapter"
	"github.com/govengers/storage/storagetest"
)

const bucket = "attachments"

type stack struct {
	store    *storage.ObservedStore
	registry *prometheus.Registry
	spans    *tracetest.SpanRecorder
}

func observe(t *testing.T, backend storage.Store) *stack {
	t.Helper()
	registry := prometheus.NewPedanticRegistry()
	metrics, err := prometheusadapter.New(&prometheusadapter.Options{Registerer: registry, Namespace: "todo"})
	require.NoError(t, err)
	spans := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))
	t.Cleanup(func() { require.NoError(t, provider.Shutdown(context.Background())) })
	tracer, err := oteladapter.New(&oteladapter.Options{TracerProvider: provider})
	require.NoError(t, err)
	store, err := storage.NewObservedStore(&storage.ObservedStoreOptions{Store: backend, Name: bucket, Observers: []storage.Observer{tracer, metrics}})
	require.NoError(t, err)
	return &stack{store: store, registry: registry, spans: spans}
}

func s3Backend(t *testing.T) storage.Store {
	t.Helper()
	server := s3fake.New(t.Cleanup, bucket)
	t.Cleanup(func() { require.Zero(t, server.PendingUploads()) })
	store, err := s3adapter.New(&s3adapter.Options{
		Endpoint: server.URL, Bucket: bucket, AccessKeyID: "access", SecretAccessKey: "secret", PathStyle: true,
		HTTPClient: &http.Client{},
	})
	require.NoError(t, err)
	return store
}

func filesystemBackend(t *testing.T) storage.Store {
	t.Helper()
	store, err := filesystem.New(&filesystem.Options{Root: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	return store
}

var backends = map[string]func(t *testing.T) storage.Store{"s3": s3Backend, "filesystem": filesystemBackend}

func TestObservedStacksConform(t *testing.T) {
	for name, backend := range backends {
		t.Run(name, func(t *testing.T) {
			storagetest.Run(t, func(t *testing.T) storage.Store { return observe(t, backend(t)).store })
		})
	}
}

// fileServer is the upload and download path of a service: POST /files/{key} accepts PNG and
// PDF up to maximumUpload bytes, GET /files/{key} streams the object with an optional
// single-range header.
func fileServer(store storage.Store) http.Handler {
	const maximumUpload = 1 << 20
	mux := http.NewServeMux()
	mux.HandleFunc("POST /files/{key...}", func(writer http.ResponseWriter, request *http.Request) {
		contentType, body, err := storage.DetectContentType(request.Body, []string{"image/png", "application/pdf"})
		if err != nil {
			writer.WriteHeader(statusOf(err))
			return
		}
		digest := storage.NewSHA256Reader(body)
		object, err := store.Put(request.Context(), request.PathValue("key"), digest, &storage.PutOptions{
			ContentType: contentType, MaximumSize: maximumUpload,
		})
		if err != nil {
			writer.WriteHeader(statusOf(err))
			return
		}
		writer.Header().Set("X-Content-SHA256", digest.Sum())
		writer.Header().Set("X-Content-Size", strconv.FormatInt(object.Size, 10))
		writer.WriteHeader(http.StatusCreated)
	})
	mux.HandleFunc("GET /files/{key...}", func(writer http.ResponseWriter, request *http.Request) {
		options, err := rangeOf(request.Header.Get("Range"))
		if err != nil {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		reader, err := store.Get(request.Context(), request.PathValue("key"), options)
		if err != nil {
			writer.WriteHeader(statusOf(err))
			return
		}
		defer reader.Close()
		writer.Header().Set("Content-Type", reader.Object.ContentType)
		writer.Header().Set("Content-Length", strconv.FormatInt(reader.Length, 10))
		_, _ = io.Copy(writer, reader) // the client hung up; the headers are out
	})
	return mux
}

// rangeOf reads "bytes=<offset>-" only, enough for the test.
func rangeOf(header string) (*storage.GetOptions, error) {
	if header == "" {
		return &storage.GetOptions{}, nil
	}
	offset, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(header, "bytes="), "-"), 10, 64)
	if err != nil {
		return nil, err
	}
	return &storage.GetOptions{Offset: offset}, nil
}

func statusOf(err error) int {
	switch {
	case errors.Is(err, storage.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, storage.ErrTooLarge):
		return http.StatusRequestEntityTooLarge
	case errors.Is(err, storage.ErrUnsupportedContentType):
		return http.StatusUnsupportedMediaType
	case errors.Is(err, storage.ErrInvalidRange):
		return http.StatusRequestedRangeNotSatisfiable
	case errors.Is(err, storage.ErrInvalid):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

// response is an answer of the file server, read completely.
type response struct {
	StatusCode int
	Header     http.Header
	Body       []byte
}

func send(t *testing.T, method, url string, body io.Reader, header http.Header) *response {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), method, url, body)
	require.NoError(t, err)
	for name, values := range header {
		request.Header[name] = values
	}
	answer, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	content, err := io.ReadAll(answer.Body)
	require.NoError(t, errors.Join(err, answer.Body.Close()))
	return &response{StatusCode: answer.StatusCode, Header: answer.Header, Body: content}
}

func TestUploadAndDownloadThroughAService(t *testing.T) {
	png := append([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"), bytes.Repeat([]byte{7}, 4000)...)
	for name, backend := range backends {
		t.Run(name, func(t *testing.T) {
			observed := observe(t, backend(t))
			server := httptest.NewServer(fileServer(observed.store))
			t.Cleanup(server.Close)

			response := send(t, http.MethodPost, server.URL+"/files/tasks/42/screenshot.png", bytes.NewReader(png), nil)
			require.Equal(t, http.StatusCreated, response.StatusCode)
			require.Equal(t, sumOf(t, png), response.Header.Get("X-Content-SHA256"))
			require.Equal(t, strconv.Itoa(len(png)), response.Header.Get("X-Content-Size"))

			response = send(t, http.MethodPost, server.URL+"/files/tasks/42/page.html", strings.NewReader("<html><script>alert(1)</script>"), nil)
			require.Equal(t, http.StatusUnsupportedMediaType, response.StatusCode)
			response = send(t, http.MethodPost, server.URL+"/files/tasks/42/huge.pdf", io.MultiReader(strings.NewReader("%PDF-1.7\n"), bytes.NewReader(make([]byte, 1<<20))), nil)
			require.Equal(t, http.StatusRequestEntityTooLarge, response.StatusCode)
			response = send(t, http.MethodPost, server.URL+"/files/tasks/back%5Cslash.png", bytes.NewReader(png), nil)
			require.Equal(t, http.StatusBadRequest, response.StatusCode, "a key with a backslash is invalid on every backend")

			response = send(t, http.MethodGet, server.URL+"/files/tasks/42/screenshot.png", nil, http.Header{"Range": {"bytes=8-"}})
			require.Equal(t, http.StatusOK, response.StatusCode)
			require.Equal(t, "image/png", response.Header.Get("Content-Type"))
			require.Equal(t, png[8:], response.Body)
			response = send(t, http.MethodGet, server.URL+"/files/tasks/42/huge.pdf", nil, nil)
			require.Equal(t, http.StatusNotFound, response.StatusCode, "the rejected upload stored nothing")

			requireObserved(t, observed, int64(len(png)))
		})
	}
}

func sumOf(t *testing.T, content []byte) string {
	t.Helper()
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}

func requireObserved(t *testing.T, observed *stack, uploaded int64) {
	t.Helper()
	require.Equal(t, 1.0, gauge(t, observed.registry, "todo_storage_operations_total", map[string]string{"operation": "put", "outcome": "success"}))
	require.Equal(t, 1.0, gauge(t, observed.registry, "todo_storage_operations_total", map[string]string{"operation": "put", "outcome": "too_large"}))
	require.Equal(t, 1.0, gauge(t, observed.registry, "todo_storage_operations_total", map[string]string{"operation": "put", "outcome": "invalid"}))
	require.Equal(t, 1.0, gauge(t, observed.registry, "todo_storage_operations_total", map[string]string{"operation": "get", "outcome": "not_found"}))
	require.Equal(t, float64(uploaded), gauge(t, observed.registry, "todo_storage_bytes_total", map[string]string{"operation": "put"}))
	require.Equal(t, float64(uploaded-8), gauge(t, observed.registry, "todo_storage_bytes_total", map[string]string{"operation": "get"}))
	names := map[string]int{}
	for _, span := range observed.spans.Ended() {
		names[span.Name()]++
	}
	require.Equal(t, map[string]int{"storage.put": 3, "storage.get": 2}, names, "the rejected content type never reached the store")
}

// gauge sums the samples of a metric family whose labels include the given ones.
func gauge(t *testing.T, registry *prometheus.Registry, name string, labels map[string]string) float64 {
	t.Helper()
	families, err := registry.Gather()
	require.NoError(t, err)
	total := 0.0
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, metric := range family.GetMetric() {
			matched := 0
			for _, pair := range metric.GetLabel() {
				if labels[pair.GetName()] == pair.GetValue() {
					matched++
				}
			}
			if matched == len(labels) {
				total += metric.GetCounter().GetValue()
			}
		}
	}
	return total
}
