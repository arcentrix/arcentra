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

package http

var (
	StatusMovedPermanently = failed(301, "Moved Permanently")

	Failed                        = failed(500, "Request failed")
	RequestParameterParsingFailed = failed(5001, "Request parameter parsing failed")
	TeamIdIsEmpty                 = failed(5002, "Team id is empty")
	OrgIdIsEmpty                  = failed(5003, "Org id is empty")

	// Unauthorized 401 sso
	Unauthorized           = failed(4401, "Unauthorized")
	AuthenticationFailed   = failed(4402, "Authentication failed")
	AuthorizationIncorrect = failed(4403, "The sso format in the request header is incorrect")
	AuthorizationEmpty     = failed(4404, "Authorization is empty")
	InvalidToken           = failed(4405, "Invalid token")
	TokenBeEmpty           = failed(4406, "Token cannot be empty")
	TokenExpired           = failed(4407, "Token is expired")
	TokenFormatIncorrect   = failed(4408, "Token format is incorrect")

	// BadRequest 400
	BadRequest = failed(4000, "Bad request")
	NotFound   = failed(4004, "Not found")

	// Forbidden 403
	Forbidden        = failed(4030, "Forbidden")
	PermissionDenied = failed(4031, "Permission denied")

	InternalError = failed(5000, "Internal error, please contact the administrator")

	UserNotExist                  = failed(4041, "User does not exist")
	UserAlreadyExist              = failed(4042, "User already exists")
	UserIncorrectPassword         = failed(4043, "User incorrect password")
	UsernameArePasswordIsRequired = failed(4045, "Username and password are required")

	UnsupportedProviders          = failed(4501, "Unsupported provider")
	ProviderIsRequired            = failed(4502, "Provider is required")
	ProviderTypeIsRequired        = failed(4503, "Provider type is required")
	InvalidStatusParameter        = failed(4502, "Invalid status parameter")
	TokenExchangeFailed           = failed(4503, "Token exchange failed")
	FailedToObtainUserInformation = failed(4504, "Failed to obtain user information")
)

var (
	Success = success(200, "Request Success")
)

// failed 构造函数
func failed(code int, msg string) *Response {
	return &Response{
		Code:   code,
		Msg:    msg,
		Detail: nil,
	}
}

// success 构造函数
func success(code int, msg string) *Response {
	return &Response{
		Code:   code,
		Msg:    msg,
		Detail: nil,
	}
}
