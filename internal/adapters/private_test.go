package adapters

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestBlockPrivateNetworks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	c := NewHTTPClient(5 * time.Second)
	BlockPrivateNetworks = true
	defer func() { BlockPrivateNetworks = false }()
	if _, err := c.Get(srv.URL); err == nil {
		t.Error("request to 127.0.0.1 allowed")
	}
	BlockPrivateNetworks = false
	if resp, err := c.Get(srv.URL); err != nil {
		t.Errorf("request with blocking off: %v", err)
	} else {
		resp.Body.Close()
	}
}
