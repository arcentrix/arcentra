// Copyright 2026 Arcentra Authors.
// SPDX-License-Identifier: Apache-2.0

package pollsnapshot

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type giteePagination struct{ total, totalPages int }

// Only locally constructed numeric pages are requested. Link is untrusted
// consistency evidence; credential-bearing links are never followed or retained.
func (m *giteePagination) next(h http.Header, current *url.URL, page, count, observed int, token, repository string) (bool, error) {
	bad := func() (bool, error) { return false, failure(ReasonPagination) }
	for _, field := range []struct {
		name string
		into *int
	}{{"Total_count", &m.total}, {"Total_page", &m.totalPages}} {
		raw, present, err := snapshotHeader(h, field.name)
		if err != nil {
			return bad()
		}
		if !present {
			continue
		}
		n, err := snapshotPageNumber(raw, 0)
		if err != nil || *field.into >= 0 && *field.into != n {
			return bad()
		}
		*field.into = n
	}
	if m.total >= 0 && observed > m.total || m.totalPages == 0 && count > 0 || m.totalPages > 0 && page > m.totalPages && count > 0 {
		return bad()
	}
	next := -1
	if values := h.Values("Link"); len(values) > 0 {
		n, last, err := giteeLink(strings.Join(values, ","), current, page, token, repository)
		if err != nil {
			return bad()
		}
		next = n
		if last >= 0 {
			if m.totalPages >= 0 && m.totalPages != last {
				return bad()
			}
			m.totalPages = last
		}
	}
	if count == 0 {
		if next > 0 || m.total >= 0 && observed != m.total || m.totalPages > 0 && page < m.totalPages {
			return bad()
		}
		return false, nil
	}
	if next == 0 {
		if m.total >= 0 && observed != m.total || m.totalPages >= 0 && page != m.totalPages {
			return bad()
		}
		return false, nil
	}
	if next > 0 {
		if m.total >= 0 && observed >= m.total || m.totalPages >= 0 && page >= m.totalPages {
			return bad()
		}
		return true, nil
	}
	if m.total >= 0 && observed == m.total {
		if m.totalPages >= 0 && page != m.totalPages {
			return bad()
		}
		return false, nil
	}
	if m.totalPages >= 0 && page == m.totalPages {
		if m.total >= 0 && observed != m.total {
			return bad()
		}
		return false, nil
	}
	return true, nil
}
func giteeLink(raw string, current *url.URL, page int, token, repository string) (int, int, error) {
	bad := func() (int, int, error) { return 0, 0, failure(ReasonPagination) }
	if strings.TrimSpace(raw) == "" {
		return bad()
	}
	next, last := 0, -1
	seen := map[string]bool{}
	parts := strings.Split(repository, "/")
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		end := strings.IndexByte(entry, '>')
		if !strings.HasPrefix(entry, "<") || end < 2 {
			return bad()
		}
		link, err := url.Parse(entry[1:end])
		if err != nil || link.User != nil || link.Fragment != "" || link.Opaque != "" || link.RawPath != "" || link.ForceQuery || strings.ContainsAny(entry[1:end], "\\\r\n\x00") {
			return bad()
		}
		link = current.ResolveReference(link)
		if link.Scheme != current.Scheme || link.Host != current.Host || link.Path != current.Path {
			return bad()
		}
		query, err := url.ParseQuery(link.RawQuery)
		if err != nil {
			return bad()
		}
		// Gitee's generated links echo the configured owner as source namespace,
		// even when our all-state request has no source filter. Accept only that
		// exact owner or an empty echo; locally constructed requests never copy it.
		if values, exists := query["source_repo_namespace"]; exists {
			if len(values) != 1 || (values[0] != "" && values[0] != parts[0]) {
				return bad()
			}
			query.Del("source_repo_namespace")
		}
		for key, expected := range map[string]string{"owner": parts[0], "repo": parts[1], "source_branch": "", "target_branch": "", "access_token": token} {
			if values, exists := query[key]; exists {
				if len(values) != 1 || values[0] != expected || key == "access_token" && token == "" {
					return bad()
				}
				query.Del(key)
			}
		}
		linkPage, err := snapshotPageNumber(query.Get("page"), 1)
		if err != nil || len(query["page"]) != 1 {
			return bad()
		}
		expected := current.Query()
		expected.Set("page", strconv.Itoa(linkPage))
		if query.Encode() != expected.Encode() {
			return bad()
		}
		relation := ""
		for _, param := range strings.Split(entry[end+1:], ";") {
			param = strings.TrimSpace(param)
			if param == "" {
				continue
			}
			key, value, ok := strings.Cut(param, "=")
			if !ok || strings.TrimSpace(key) != "rel" || relation != "" {
				return bad()
			}
			value = strings.TrimSpace(value)
			if len(value) >= 2 && (value[0] == '\'' || value[0] == '"') {
				if value[len(value)-1] != value[0] {
					return bad()
				}
				value = value[1 : len(value)-1]
			}
			relation = value
		}
		if relation == "" {
			return bad()
		}
		for _, rel := range strings.Fields(relation) {
			if seen[rel] {
				return bad()
			}
			seen[rel] = true
			switch rel {
			case "next":
				if linkPage != page+1 {
					return bad()
				}
				next = linkPage
			case "prev":
				if page == 1 || linkPage != page-1 {
					return bad()
				}
			case "first":
				if linkPage != 1 {
					return bad()
				}
			case "last":
				if linkPage < page {
					return bad()
				}
				last = linkPage
			default:
				return bad()
			}
		}
	}
	if last >= 0 && (next == 0 && last != page || next > 0 && last < next) {
		return bad()
	}
	return next, last, nil
}
