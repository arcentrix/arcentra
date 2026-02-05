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

package http

import (
	"io"

	"github.com/go-resty/resty/v2"
)

type Request struct {
	Url     string
	Method  string
	Headers map[string]string
	Body    io.Reader
}

func NewRequest(url, method string, headers map[string]string, body io.Reader) *Request {
	return &Request{
		Url:     url,
		Method:  method,
		Headers: headers,
		Body:    body,
	}
}

func (r *Request) GET() *resty.Response {
	response, err := client().R().
		SetHeaders(r.Headers).
		Get(r.Url)
	if err != nil {
		return &resty.Response{}
	}
	return response
}

func (r *Request) POST() *resty.Response {
	response, err := client().R().
		SetHeaders(r.Headers).
		SetBody(r.Body).
		Post(r.Url)
	if err != nil {
		return &resty.Response{}
	}
	return response
}

func client() *resty.Client {
	return resty.New()
}
