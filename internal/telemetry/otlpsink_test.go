package telemetry

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"
)

type otlpSink struct {
	mu      sync.Mutex
	reqs    []*coltracepb.ExportTraceServiceRequest
	headers []http.Header
	paths   []string
	srv     *httptest.Server
}

func newOTLPSink(t *testing.T) *otlpSink {
	t.Helper()
	s := &otlpSink{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if r.Header.Get("Content-Encoding") == "gzip" {
			zr, err := gzip.NewReader(bytes.NewReader(raw))
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			raw, err = io.ReadAll(zr)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
		var req coltracepb.ExportTraceServiceRequest
		if err := proto.Unmarshal(raw, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		s.reqs = append(s.reqs, &req)
		s.headers = append(s.headers, r.Header.Clone())
		s.paths = append(s.paths, r.URL.Path)
		s.mu.Unlock()
		resp, _ := proto.Marshal(&coltracepb.ExportTraceServiceResponse{})
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.Write(resp)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *otlpSink) spanNames() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var names []string
	for _, req := range s.reqs {
		for _, rs := range req.GetResourceSpans() {
			for _, ss := range rs.GetScopeSpans() {
				for _, sp := range ss.GetSpans() {
					names = append(names, sp.GetName())
				}
			}
		}
	}
	return names
}

func (s *otlpSink) requestCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.reqs)
}
