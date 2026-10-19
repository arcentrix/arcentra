// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package pollsnapshot

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// 元数据可以缺失；存在时必须自洽且整个单遍扫描保持不变
// 缺少终止证据时扫描到空页，不能把服务端限制后的短页当作完成
// offset 分页没有原子快照保证；第二遍库存比较仍是尽力检测

type snapshotPagination struct {
	// Set only after GitHub repository metadata binds the configured name to its ID.
	canonicalPath string
	total         int
	totalPages    int
	perPage       int
}

func (m *snapshotPagination) next(headers http.Header, current *url.URL, page, count, observed, requestedSize int) (bool, error) {
	failure := func() (bool, error) { return false, failure(ReasonPagination) }
	for _, field := range []struct {
		name string
		into *int
		min  int
	}{{"X-Total", &m.total, 0}, {"X-Total-Pages", &m.totalPages, 0}, {"X-Per-Page", &m.perPage, 1}} {
		raw, present, err := snapshotHeader(headers, field.name)
		if err != nil {
			return failure()
		}
		if !present {
			continue
		}
		n, err := snapshotPageNumber(raw, field.min)
		if err != nil || (*field.into >= 0 && *field.into != n) {
			return failure()
		}
		*field.into = n
	}
	if (m.totalPages == 0 && count > 0) || (m.perPage > requestedSize) || (m.perPage >= 0 && count > m.perPage) || (m.total >= 0 && observed > m.total) || (m.totalPages > 0 && page > m.totalPages && count != 0) {
		return failure()
	}
	if m.total >= 0 && m.totalPages >= 0 && m.perPage > 0 {
		expected := m.total / m.perPage
		if m.total%m.perPage > 0 {
			expected++
		}
		// GitLab may represent an empty collection with zero or one page
		if m.totalPages != expected && !(m.total == 0 && m.totalPages == 1) {
			return failure()
		}
	}
	if raw, present, err := snapshotHeader(headers, "X-Page"); err != nil {
		return failure()
	} else if present {
		n, err := snapshotPageNumber(raw, 1)
		if err != nil || n != page {
			return failure()
		}
	}
	if raw, present, err := snapshotHeader(headers, "X-Prev-Page"); err != nil {
		return failure()
	} else if present {
		if page == 1 {
			if raw != "" {
				return failure()
			}
		} else if n, err := snapshotPageNumber(raw, 1); err != nil || n != page-1 {
			return failure()
		}
	}

	// next=-1 表示没有明确终止或继续证据；0 表示明确末页
	next := -1
	if raw, present, err := snapshotHeader(headers, "X-Next-Page"); err != nil {
		return failure()
	} else if present {
		next = 0
		if raw != "" {
			n, err := snapshotPageNumber(raw, 1)
			if err != nil || n != page+1 {
				return failure()
			}
			next = n
		}
	}
	// Gitea uses X-Total-Count. Reject disagreement with any common total.
	if raw, present, err := snapshotHeader(headers, "X-Total-Count"); err != nil {
		return failure()
	} else if present {
		n, err := snapshotPageNumber(raw, 0)
		if err != nil || (m.total >= 0 && m.total != n) {
			return failure()
		}
		m.total = n
		if observed > n {
			return failure()
		}
	}
	linkValues := headers.Values("Link")
	if len(linkValues) > 0 {
		linkNext, err := snapshotNextLink(strings.Join(linkValues, ","), current, page, m.perPage, m.canonicalPath)
		if err != nil || (next != -1 && next != linkNext) {
			return failure()
		}
		next = linkNext
	}
	if count == 0 {
		if next > 0 || (m.total >= 0 && observed != m.total) || (m.totalPages > 0 && page < m.totalPages) {
			return failure()
		}
		return false, nil
	}
	if next == 0 {
		if (m.total >= 0 && observed != m.total) || (m.totalPages >= 0 && page != m.totalPages) {
			return failure()
		}
		return false, nil
	}
	if next > 0 {
		if (m.total >= 0 && observed >= m.total) || (m.totalPages >= 0 && page >= m.totalPages) {
			return failure()
		}
		return true, nil
	}
	if m.total >= 0 && observed == m.total {
		if m.totalPages >= 0 && page != m.totalPages {
			return failure()
		}
		return false, nil
	}
	if m.totalPages >= 0 && page == m.totalPages {
		if m.total >= 0 && observed != m.total {
			return failure()
		}
		return false, nil
	}
	return true, nil
}

func snapshotHeader(headers http.Header, name string) (string, bool, error) {
	values, present := headers[http.CanonicalHeaderKey(name)]
	if !present {
		return "", false, nil
	}
	if len(values) != 1 || strings.TrimSpace(values[0]) != values[0] {
		return "", true, failure(ReasonPagination)
	}
	return values[0], true, nil
}

func snapshotPageNumber(raw string, minimum int) (int, error) {
	value, err := strconv.Atoi(raw)
	if err != nil || value < minimum || strconv.Itoa(value) != raw {
		return 0, failure(ReasonPagination)
	}
	return value, nil
}

func snapshotNextLink(raw string, current *url.URL, page, perPage int, canonicalPath string) (int, error) {
	failure := func() (int, error) { return 0, failure(ReasonPagination) }
	if strings.TrimSpace(raw) == "" {
		return failure()
	}
	next := 0
	seenNext := false
	seenRelations := map[string]bool{}
	lastPage := -1
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		end := strings.IndexByte(entry, '>')
		if !strings.HasPrefix(entry, "<") || end < 2 {
			return failure()
		}
		link, err := url.Parse(entry[1:end])
		if err != nil || link.User != nil || link.Fragment != "" || link.Opaque != "" {
			return failure()
		}
		link = current.ResolveReference(link)
		// 参数也必须保持固定范围，尤其不能由 Link 改写查询时间或项目
		if link.Scheme != current.Scheme || !strings.EqualFold(link.Host, current.Host) || (link.EscapedPath() != current.EscapedPath() && (canonicalPath == "" || link.EscapedPath() != canonicalPath)) {
			return failure()
		}
		query, err := url.ParseQuery(link.RawQuery)
		if err != nil {
			return failure()
		}
		linkPage, err := snapshotPageNumber(query.Get("page"), 1)
		if err != nil || len(query["page"]) != 1 {
			return failure()
		}
		expected := current.Query()
		expected.Set("page", strconv.Itoa(linkPage))
		if perPage > 0 {
			if expected.Has("limit") {
				expected.Set("limit", strconv.Itoa(perPage))
			} else {
				expected.Set("per_page", strconv.Itoa(perPage))
			}
		}
		if query.Encode() != expected.Encode() {
			return failure()
		}
		relation := ""
		for _, parameter := range strings.Split(entry[end+1:], ";") {
			parameter = strings.TrimSpace(parameter)
			if parameter == "" {
				continue
			}
			key, value, ok := strings.Cut(parameter, "=")
			if !ok {
				return failure()
			}
			if strings.TrimSpace(key) == "rel" {
				if relation != "" {
					return failure()
				}
				relation = strings.Trim(strings.TrimSpace(value), `"`)
			}
		}
		if relation == "" {
			return failure()
		}
		for _, rel := range strings.Fields(relation) {
			if seenRelations[rel] {
				return failure()
			}
			seenRelations[rel] = true
			switch rel {
			case "next":
				if seenNext || linkPage != page+1 {
					return failure()
				}
				seenNext, next = true, linkPage
			case "prev":
				if page == 1 || linkPage != page-1 {
					return failure()
				}
			case "first":
				if linkPage != 1 {
					return failure()
				}
			case "last":
				if linkPage < page {
					return failure()
				}
				lastPage = linkPage
			default:
				return failure()
			}
		}
	}
	if lastPage >= 0 && ((next == 0 && lastPage != page) || (next > 0 && lastPage < next)) {
		return failure()
	}
	return next, nil
}
