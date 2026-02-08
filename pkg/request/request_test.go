// Copyright 2025 Arcentra Team
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package request

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/valyala/fasthttp"
)

type testResp struct {
	Message string `json:"message"`
	Echo    string `json:"echo,omitempty"`
}

func TestGETWithResult(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"message":"ok"}`))
	}))
	defer srv.Close()

	var out testResp
	resp, err := NewRequest(srv.URL, fasthttp.MethodGet, nil, nil).
		WithResult(&out).
		Do()
	if err != nil {
		t.Fatalf("GET error: %v", err)
	}
	if resp.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("unexpected status: %d", resp.StatusCode())
	}
	if out.Message != "ok" {
		t.Fatalf("unexpected message: %q", out.Message)
	}
}

func TestPOSTDoWithBodyAndResult(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		respBody, _ := sonic.Marshal(testResp{
			Message: "ok",
			Echo:    string(body),
		})
		_, _ = w.Write(respBody)
	}))
	defer srv.Close()

	var out testResp
	body := bytes.NewBufferString(`{"foo":"bar"}`)
	req := NewRequest(srv.URL, fasthttp.MethodPost, nil, body).
		WithResult(&out)

	resp, err := req.Do()
	if err != nil {
		t.Fatalf("Do error: %v", err)
	}
	if resp.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("unexpected status: %d", resp.StatusCode())
	}
	if out.Message != "ok" || out.Echo == "" {
		t.Fatalf("unexpected response: %+v", out)
	}
}

func TestProxyMissingScheme(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	_, err := NewRequest(srv.URL, fasthttp.MethodGet, nil, nil).
		WithProxy("127.0.0.1:8080").
		Do()
	if err == nil {
		t.Fatalf("expected proxy scheme error")
	}
}

func TestMethodValidation(t *testing.T) {
	_, err := NewRequest("http://example.com", "", nil, nil).Do()
	if err == nil {
		t.Fatalf("expected method required error")
	}

	_, err = NewRequest("http://example.com", "BAD", nil, nil).Do()
	if err == nil {
		t.Fatalf("expected invalid method error")
	}
}

func TestQueryParams(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("a") != "1" || q.Get("b") != "2" || q.Get("x") != "y" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	resp, err := NewRequest(srv.URL+"?x=y", fasthttp.MethodGet, nil, nil).
		WithQueryParams(map[string]string{"a": "1", "b": "2"}).
		Do()
	if err != nil {
		t.Fatalf("Do error: %v", err)
	}
	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("unexpected status: %d", resp.StatusCode())
	}
}

func TestFormURLEncoded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if err := r.ParseForm(); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.FormValue("k") != "v" || r.FormValue("a") != "1" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	resp, err := NewRequest(srv.URL, fasthttp.MethodPost, nil, nil).
		WithForm(map[string]string{"k": "v"}).
		WithFormField("a", "1").
		Do()
	if err != nil {
		t.Fatalf("Do error: %v", err)
	}
	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("unexpected status: %d", resp.StatusCode())
	}
}

func TestMultipartForm(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.FormValue("k") != "v" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		files := r.MultipartForm.File["file"]
		if len(files) != 1 || files[0].Filename != "a.txt" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f, err := files[0].Open()
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		defer f.Close()
		data, _ := io.ReadAll(f)
		if string(data) != "hello" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	resp, err := NewRequest(srv.URL, fasthttp.MethodPost, nil, nil).
		WithFormField("k", "v").
		WithFormFileBytes("file", "a.txt", "text/plain", []byte("hello")).
		Do()
	if err != nil {
		t.Fatalf("Do error: %v", err)
	}
	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("unexpected status: %d", resp.StatusCode())
	}
}

func TestBodyJSONAndBytes(t *testing.T) {
	type payload struct {
		Name string `json:"name"`
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"name":"test"}` {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.Header.Get("Content-Type") != "application/json" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	resp, err := NewRequest(srv.URL, fasthttp.MethodPost, nil, nil).
		WithBodyJSON(payload{Name: "test"}).
		Do()
	if err != nil {
		t.Fatalf("Do error: %v", err)
	}
	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("unexpected status: %d", resp.StatusCode())
	}

	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if string(body) != "raw" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv2.Close()

	resp, err = NewRequest(srv2.URL, fasthttp.MethodPost, nil, nil).
		WithBodyBytes([]byte("raw")).
		Do()
	if err != nil {
		t.Fatalf("Do error: %v", err)
	}
	if resp.StatusCode() != http.StatusOK {
		t.Fatalf("unexpected status: %d", resp.StatusCode())
	}
}
