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

package ldap

import (
	"crypto/tls"
	"fmt"
	"strings"

	"github.com/go-ldap/ldap/v3"
)

// UserInfo LDAP 用户信息
type UserInfo struct {
	DN          string              `json:"dn"`
	Username    string              `json:"username"`
	Email       string              `json:"email"`
	DisplayName string              `json:"displayName"`
	Groups      []string            `json:"groups"`
	Attributes  map[string][]string `json:"attributes"`
}

// LDAPClient LDAP 客户端
type LDAPClient struct {
	Host         string
	Port         int
	UseTLS       bool
	SkipVerify   bool
	BaseDN       string
	BindDN       string
	BindPassword string
	UserFilter   string
	UserDN       string
	GroupFilter  string
	GroupDN      string
	Attributes   map[string]string
}

// NewLDAPClient 创建 LDAP 客户端
func NewLDAPClient(host string, port int, useTLS, skipVerify bool, baseDN, bindDN, bindPassword string) *LDAPClient {
	return &LDAPClient{
		Host:         host,
		Port:         port,
		UseTLS:       useTLS,
		SkipVerify:   skipVerify,
		BaseDN:       baseDN,
		BindDN:       bindDN,
		BindPassword: bindPassword,
	}
}

// Connect 连接到 LDAP 服务器
func (c *LDAPClient) Connect() (*ldap.Conn, error) {
	address := fmt.Sprintf("%s:%d", c.Host, c.Port)

	var conn *ldap.Conn
	var err error

	if c.UseTLS {
		tlsConfig := &tls.Config{
			InsecureSkipVerify: c.SkipVerify,
		}
		conn, err = ldap.DialURL(address, ldap.DialWithTLSConfig(tlsConfig))
	} else {
		conn, err = ldap.DialURL(address)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to connect to LDAP server: %w", err)
	}

	return conn, nil
}

// Authenticate 认证用户
func (c *LDAPClient) Authenticate(username, password string) (*UserInfo, error) {
	conn, err := c.Connect()
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	// 使用 BindDN 绑定
	if c.BindDN != "" && c.BindPassword != "" {
		err = conn.Bind(c.BindDN, c.BindPassword)
		if err != nil {
			return nil, fmt.Errorf("failed to bind with service account: %w", err)
		}
	}

	// 查找用户
	userInfo, err := c.searchUser(conn, username)
	if err != nil {
		return nil, err
	}

	// 使用用户的 DN 进行认证
	err = conn.Bind(userInfo.DN, password)
	if err != nil {
		return nil, fmt.Errorf("authentication failed: %w", err)
	}

	// 重新绑定服务账户以查询用户组
	if c.BindDN != "" && c.BindPassword != "" {
		err = conn.Bind(c.BindDN, c.BindPassword)
		if err != nil {
			return nil, fmt.Errorf("failed to rebind with service account: %w", err)
		}
	}

	// 查询用户组
	groups, err := c.searchUserGroups(conn, username, userInfo.DN)
	if err != nil {
		// 组查询失败不是致命错误
		fmt.Printf("failed to search user groups: %v\n", err)
	} else {
		userInfo.Groups = groups
	}

	return userInfo, nil
}

// searchUser 搜索用户
func (c *LDAPClient) searchUser(conn *ldap.Conn, username string) (*UserInfo, error) {
	// 构建搜索过滤器
	filter := c.UserFilter
	if filter == "" {
		filter = "(uid=%s)"
	}
	filter = fmt.Sprintf(filter, ldap.EscapeFilter(username))

	// 构建搜索基础 DN
	searchBase := c.UserDN
	if searchBase == "" {
		searchBase = c.BaseDN
	}

	// 构建搜索属性列表
	attributes := []string{"dn", "uid", "cn", "mail", "displayName", "sn", "givenName"}
	for _, attr := range c.Attributes {
		if attr != "" && !contains(attributes, attr) {
			attributes = append(attributes, attr)
		}
	}

	// 执行搜索
	searchRequest := ldap.NewSearchRequest(
		searchBase,
		ldap.ScopeWholeSubtree,
		ldap.NeverDerefAliases,
		0,
		0,
		false,
		filter,
		attributes,
		nil,
	)

	result, err := conn.Search(searchRequest)
	if err != nil {
		return nil, fmt.Errorf("failed to search user: %w", err)
	}

	if len(result.Entries) == 0 {
		return nil, fmt.Errorf("user not found: %s", username)
	}

	if len(result.Entries) > 1 {
		return nil, fmt.Errorf("multiple users found for: %s", username)
	}

	entry := result.Entries[0]

	// 提取用户信息
	userInfo := &UserInfo{
		DN:         entry.DN,
		Attributes: make(map[string][]string),
	}

	// 使用属性映射提取信息
	if c.Attributes != nil {
		if usernameAttr, ok := c.Attributes["username"]; ok && usernameAttr != "" {
			userInfo.Username = entry.GetAttributeValue(usernameAttr)
		}
		if emailAttr, ok := c.Attributes["email"]; ok && emailAttr != "" {
			userInfo.Email = entry.GetAttributeValue(emailAttr)
		}
		if displayNameAttr, ok := c.Attributes["displayName"]; ok && displayNameAttr != "" {
			userInfo.DisplayName = entry.GetAttributeValue(displayNameAttr)
		}
	}

	// 默认属性提取
	if userInfo.Username == "" {
		userInfo.Username = firstNonEmpty(
			entry.GetAttributeValue("uid"),
			entry.GetAttributeValue("sAMAccountName"),
			username,
		)
	}
	if userInfo.Email == "" {
		userInfo.Email = entry.GetAttributeValue("mail")
	}
	if userInfo.DisplayName == "" {
		userInfo.DisplayName = firstNonEmpty(
			entry.GetAttributeValue("displayName"),
			entry.GetAttributeValue("cn"),
			userInfo.Username,
		)
	}

	// 保存所有属性
	for _, attr := range entry.Attributes {
		userInfo.Attributes[attr.Name] = attr.Values
	}

	return userInfo, nil
}

// searchUserGroups 搜索用户组
func (c *LDAPClient) searchUserGroups(conn *ldap.Conn, username, userDN string) ([]string, error) {
	if c.GroupDN == "" {
		return nil, nil
	}

	// 构建组搜索过滤器
	filter := c.GroupFilter
	if filter == "" {
		// 默认使用 memberUid 或 member 属性
		filter = fmt.Sprintf("(|(memberUid=%s)(member=%s))",
			ldap.EscapeFilter(username),
			ldap.EscapeFilter(userDN))
	} else {
		filter = fmt.Sprintf(filter, ldap.EscapeFilter(username))
	}

	// 执行搜索
	searchRequest := ldap.NewSearchRequest(
		c.GroupDN,
		ldap.ScopeWholeSubtree,
		ldap.NeverDerefAliases,
		0,
		0,
		false,
		filter,
		[]string{"cn", "dn"},
		nil,
	)

	result, err := conn.Search(searchRequest)
	if err != nil {
		return nil, fmt.Errorf("failed to search groups: %w", err)
	}

	groups := make([]string, 0, len(result.Entries))
	for _, entry := range result.Entries {
		groupName := entry.GetAttributeValue("cn")
		if groupName != "" {
			groups = append(groups, groupName)
		}
	}

	return groups, nil
}

// contains 检查字符串切片是否包含指定字符串
func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

// firstNonEmpty 返回第一个非空字符串
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
