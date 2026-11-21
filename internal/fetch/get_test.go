package fetch

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestGetReturnsTheBody(t *testing.T) {
	srv := tlsServer(t, func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("abc  file\n")) })
	b, err := Downloader{Client: srv.Client()}.Get(context.Background(), srv.URL+"/SHA256SUMS", 1024)
	if err != nil || string(b) != "abc  file\n" {
		t.Errorf("Get = %q, %v", b, err)
	}
}

func TestGetCapsTheBody(t *testing.T) {
	srv := tlsServer(t, func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(strings.Repeat("x", 2048))) })
	if _, err := (Downloader{Client: srv.Client()}).Get(context.Background(), srv.URL+"/big", 1024); !errors.Is(err, ErrTooLarge) {
		t.Errorf("err = %v, want ErrTooLarge", err)
	}
}

func TestGetRefusesHTTP(t *testing.T) {
	if _, err := (Downloader{}).Get(context.Background(), "http://example.com/SHA256SUMS", 1024); !errors.Is(err, ErrNotHTTPS) {
		t.Errorf("err = %v, want ErrNotHTTPS", err)
	}
}

func TestGetRefusesAFailedStatus(t *testing.T) {
	srv := tlsServer(t, func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "gone", http.StatusNotFound) })
	if _, err := (Downloader{Client: srv.Client()}).Get(context.Background(), srv.URL+"/x", 1024); err == nil {
		t.Error("Get accepted a 404")
	}
}
