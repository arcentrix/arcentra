// Copyright 2025 Arcentra Authors.
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
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/textproto"
	"net/url"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/valyala/fasthttp"
	"github.com/valyala/fasthttp/fasthttpproxy"
)

// Request represents a single HTTP request with optional body, params, and proxy.
type Request struct {
	Method  string
	URL     string
	Headers map[string]string
	Query   map[string]string
	Form    map[string]string
	Files   []FormFile
	Body    io.Reader
	BodyRaw []byte
	BodyObj any
	Proxy   string
	Result  any
}

// FormFile represents a single multipart file part.
type FormFile struct {
	FieldName   string
	FileName    string
	ContentType string
	Data        []byte
}

// NewRequest creates a new request with the given method, headers, and body.
func NewRequest(url, method string, headers map[string]string, body io.Reader) *Request {
	return &Request{
		Method:  method,
		URL:     url,
		Headers: headers,
		Body:    body,
	}
}

// WithProxy sets the HTTP proxy URL.
func (r *Request) WithProxy(proxy string) *Request {
	r.Proxy = proxy
	return r
}

// WithQueryParams appends query parameters to the request URL.
func (r *Request) WithQueryParams(params map[string]string) *Request {
	r.Query = params
	return r
}

// WithForm sets form fields using application/x-www-form-urlencoded.
func (r *Request) WithForm(params map[string]string) *Request {
	r.Form = params
	if r.Headers == nil {
		r.Headers = map[string]string{}
	}
	if _, ok := r.Headers["Content-Type"]; !ok {
		r.Headers["Content-Type"] = "application/x-www-form-urlencoded"
	}
	return r
}

// WithFormField adds a single form field (urlencoded or multipart).
func (r *Request) WithFormField(key, value string) *Request {
	if r.Form == nil {
		r.Form = map[string]string{}
	}
	r.Form[key] = value
	return r
}

// WithFormFileBytes adds a multipart file with in-memory content.
func (r *Request) WithFormFileBytes(fieldName, fileName, contentType string, data []byte) *Request {
	r.Files = append(r.Files, FormFile{
		FieldName:   fieldName,
		FileName:    fileName,
		ContentType: contentType,
		Data:        data,
	})
	return r
}

// WithResult decodes the response body into result when present.
func (r *Request) WithResult(result any) *Request {
	r.Result = result
	return r
}

// WithBodyBytes sets raw body bytes.
func (r *Request) WithBodyBytes(body []byte) *Request {
	r.BodyRaw = body
	return r
}

// WithBodyJSON sets a JSON body and default Content-Type.
func (r *Request) WithBodyJSON(body any) *Request {
	r.BodyObj = body
	if r.Headers == nil {
		r.Headers = map[string]string{}
	}
	if _, ok := r.Headers["Content-Type"]; !ok {
		r.Headers["Content-Type"] = "application/json"
	}
	return r
}

// Do sends the request using the configured method.
func (r *Request) Do() (*fasthttp.Response, error) {
	method := strings.ToUpper(strings.TrimSpace(r.Method))
	if method == "" {
		return nil, errors.New("request method is required")
	}
	if !isValidMethod(method) {
		return nil, fmt.Errorf("invalid request method: %s", method)
	}
	return r.do(method)
}

func (r *Request) do(method string) (*fasthttp.Response, error) {
	req := fasthttp.AcquireRequest()
	defer fasthttp.ReleaseRequest(req)

	resp := &fasthttp.Response{}
	req.Header.SetMethod(method)
	req.SetRequestURI(r.withQuery())

	for key, value := range r.Headers {
		req.Header.Set(key, value)
	}

	switch {
	case len(r.Files) > 0 || len(r.Form) > 0:
		bodyBytes, contentType, err := r.buildMultipartOrForm()
		if err != nil {
			return resp, err
		}
		req.Header.Set("Content-Type", contentType)
		req.SetBody(bodyBytes)
	case r.Body != nil:
		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			return resp, err
		}
		req.SetBody(bodyBytes)
	case len(r.BodyRaw) > 0:
		req.SetBody(r.BodyRaw)
	case r.BodyObj != nil:
		bodyBytes, err := sonic.Marshal(r.BodyObj)
		if err != nil {
			return resp, err
		}
		req.SetBody(bodyBytes)
	}

	client, err := client(r.Proxy)
	if err != nil {
		return resp, err
	}
	if err := client.Do(req, resp); err != nil {
		return resp, err
	}

	if r.Result != nil && len(resp.Body()) > 0 {
		if err := sonic.Unmarshal(resp.Body(), r.Result); err != nil {
			return resp, err
		}
	}
	return resp, nil
}

func (r *Request) withQuery() string {
	if len(r.Query) == 0 {
		return r.URL
	}
	parsed, err := url.Parse(r.URL)
	if err != nil {
		return r.URL
	}
	values := parsed.Query()
	for key, value := range r.Query {
		values.Set(key, value)
	}
	parsed.RawQuery = values.Encode()
	return parsed.String()
}

func (r *Request) buildMultipartOrForm() ([]byte, string, error) {
	if len(r.Files) == 0 {
		values := url.Values{}
		for key, value := range r.Form {
			values.Set(key, value)
		}
		return []byte(values.Encode()), "application/x-www-form-urlencoded", nil
	}

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	for key, value := range r.Form {
		if err := writer.WriteField(key, value); err != nil {
			return nil, "", err
		}
	}

	for _, file := range r.Files {
		h := make(textproto.MIMEHeader)
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`, file.FieldName, file.FileName))
		if file.ContentType != "" {
			h.Set("Content-Type", file.ContentType)
		}
		part, err := writer.CreatePart(h)
		if err != nil {
			return nil, "", err
		}
		if _, err := part.Write(file.Data); err != nil {
			return nil, "", err
		}
	}

	if err := writer.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), writer.FormDataContentType(), nil
}

// client builds a fasthttp client with optional proxy.
func client(proxy string) (*fasthttp.Client, error) {
	if proxy == "" {
		return &fasthttp.Client{}, nil
	}
	proxyURL, err := url.Parse(proxy)
	if err != nil {
		return nil, err
	}
	if proxyURL.Scheme == "" {
		return nil, fmt.Errorf("proxy url missing scheme")
	}
	return &fasthttp.Client{Dial: fasthttpproxy.FasthttpHTTPDialer(proxy)}, nil
}

// isValidMethod validates supported HTTP methods.
func isValidMethod(method string) bool {
	switch method {
	case fasthttp.MethodGet,
		fasthttp.MethodPost,
		fasthttp.MethodPut,
		fasthttp.MethodDelete,
		fasthttp.MethodPatch,
		fasthttp.MethodHead,
		fasthttp.MethodOptions:
		return true
	default:
		return false
	}
}
