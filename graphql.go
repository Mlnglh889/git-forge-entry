package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

var gqlSchema = map[string][]string{
	"Query":            {"viewer", "repository", "node", "__type"},
	"Mutation":         {"createIssue", "closeIssue", "createPullRequest", "mergePullRequest", "addComment"},
	"Repository":       {"id", "databaseId", "name", "nameWithOwner", "owner", "defaultBranchRef", "viewerPermission", "hasIssuesEnabled", "issue", "issues", "pullRequests", "pullRequest", "description"},
	"Issue":            {"id", "number", "title", "state", "body", "comments", "createdAt", "updatedAt"},
	"PullRequest":      {"id", "number", "title", "body", "headRefName", "baseRefName", "mergeable", "state", "merged", "files"},
	"User":             {"id", "login", "name"},
	"SearchType":       {"ISSUE", "REPOSITORY", "USER"},
	"IssueState":       {"OPEN", "CLOSED"},
	"PullRequestState": {"OPEN", "CLOSED", "MERGED"},
}

func isDigit(c byte) bool  { return c >= '0' && c <= '9' }
func isNameStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}
func isNameChar(c byte) bool { return isNameStart(c) || isDigit(c) }

func gqlTokenize(s string) []string {
	var toks []string
	i := 0
	for i < len(s) {
		c := s[i]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == ',' {
			i++
			continue
		}
		if c == '#' {
			for i < len(s) && s[i] != '\n' {
				i++
			}
			continue
		}
		if strings.HasPrefix(s[i:], "...") {
			toks = append(toks, "...")
			i += 3
			continue
		}
		if c == '"' {
			j := i + 1
			for j < len(s) {
				if s[j] == '\\' {
					j += 2
					continue
				}
				if s[j] == '"' {
					j++
					break
				}
				j++
			}
			toks = append(toks, s[i:j])
			i = j
			continue
		}
		if isDigit(c) || (c == '-' && i+1 < len(s) && isDigit(s[i+1])) {
			j := i
			if s[j] == '-' {
				j++
			}
			for j < len(s) && isDigit(s[j]) {
				j++
			}
			if j < len(s) && s[j] == '.' {
				j++
				for j < len(s) && isDigit(s[j]) {
					j++
				}
			}
			toks = append(toks, s[i:j])
			i = j
			continue
		}
		if isNameStart(c) {
			j := i + 1
			for j < len(s) && isNameChar(s[j]) {
				j++
			}
			toks = append(toks, s[i:j])
			i = j
			continue
		}
		toks = append(toks, string(c))
		i++
	}
	return toks
}

type gqlField struct {
	Name      string
	Alias     string
	Args      map[string]any
	Directives []gqlDirective
	Selection []gqlField
	Fragment  string
	Spread    string
}
type gqlDirective struct {
	d   string
	yes bool
}
type gqlOp struct {
	kind      string
	name      string
	selection []gqlField
}
type gqlFragment struct {
	typ       string
	selection []gqlField
}

type gqlParser struct {
	toks []string
	p    int
	vars map[string]any
}

func (p *gqlParser) take() string {
	if p.p >= len(p.toks) {
		return ""
	}
	t := p.toks[p.p]
	p.p++
	return t
}
func (p *gqlParser) peek() string {
	if p.p >= len(p.toks) {
		return ""
	}
	return p.toks[p.p]
}
func (p *gqlParser) expect(s string) {
	if p.take() != s {
		panic(fmt.Errorf("Expected " + s))
	}
}
func (p *gqlParser) value() any {
	t := p.take()
	if t == "$" {
		return p.vars[p.take()]
	}
	if t == "{" {
		o := map[string]any{}
		for p.peek() != "}" && p.p < len(p.toks) {
			k := p.take()
			p.expect(":")
			o[k] = p.value()
		}
		p.expect("}")
		return o
	}
	if t == "[" {
		var a []any
		for p.peek() != "]" && p.p < len(p.toks) {
			a = append(a, p.value())
		}
		p.expect("]")
		return a
	}
	if strings.HasPrefix(t, "\"") {
		var s string
		json.Unmarshal([]byte(t), &s)
		return s
	}
	if t == "null" {
		return nil
	}
	if t == "true" || t == "false" {
		return t == "true"
	}
	if len(t) > 0 && (isDigit(t[0]) || (t[0] == '-' && len(t) > 1)) {
		f, _ := strconv.ParseFloat(t, 64)
		return f
	}
	return t
}
func (p *gqlParser) selection() []gqlField {
	p.expect("{")
	var fields []gqlField
	for p.peek() != "}" && p.p < len(p.toks) {
		name := p.take()
		if name == "..." {
			if p.peek() == "on" {
				p.take()
				typ := p.take()
				fields = append(fields, gqlField{Fragment: typ, Selection: p.selection()})
			} else {
				fields = append(fields, gqlField{Spread: p.take()})
			}
			continue
		}
		alias := name
		if p.peek() == ":" {
			p.take()
			name = p.take()
		}
		args := map[string]any{}
		if p.peek() == "(" {
			p.take()
			for p.peek() != ")" && p.p < len(p.toks) {
				k := p.take()
				p.expect(":")
				args[k] = p.value()
			}
			p.expect(")")
		}
		var directives []gqlDirective
		for p.peek() == "@" {
			p.take()
			d := p.take()
			yes := true
			if p.peek() == "(" {
				p.take()
				p.take()
				p.expect(":")
				yes, _ = p.value().(bool)
				p.expect(")")
			}
			directives = append(directives, gqlDirective{d, yes})
		}
		var sel []gqlField
		if p.peek() == "{" {
			sel = p.selection()
		}
		fields = append(fields, gqlField{Name: name, Alias: alias, Args: args, Directives: directives, Selection: sel})
	}
	p.expect("}")
	return fields
}
func gqlParse(source string, vars map[string]any) ([]gqlOp, map[string]gqlFragment) {
	p := &gqlParser{toks: gqlTokenize(source), vars: vars}
	var ops []gqlOp
	frags := map[string]gqlFragment{}
	for p.p < len(p.toks) {
		if p.peek() == "fragment" {
			p.take()
			name := p.take()
			p.expect("on")
			typ := p.take()
			frags[name] = gqlFragment{typ, p.selection()}
			continue
		}
		kind := "query"
		var name string
		if p.peek() != "{" {
			kind = p.take()
			if p.peek() != "(" && p.peek() != "{" {
				name = p.take()
			}
			if p.peek() == "(" {
				nesting := 0
				for {
					t := p.take()
					if t == "(" {
						nesting++
					}
					if t == ")" {
						nesting--
					}
					if nesting == 0 || p.p >= len(p.toks) {
						break
					}
				}
			}
		}
		ops = append(ops, gqlOp{kind, name, p.selection()})
	}
	return ops, frags
}

func gqlTyped(typ string, m map[string]any) map[string]any {
	m["__typename"] = typ
	return m
}

func gqlConn(items []any, args map[string]any) map[string]any {
	offset := 0
	if after, ok := args["after"].(string); ok && after != "" {
		dec, _ := base64.StdEncoding.DecodeString(after)
		parts := strings.Split(string(dec), ":")
		offset, _ = strconv.Atoi(parts[len(parts)-1])
	}
	first := 100
	if fv, ok := args["first"]; ok {
		first = toInt(fv)
		if first < 0 {
			first = 0
		}
	}
	if offset > len(items) {
		offset = len(items)
	}
	end := offset + first
	if end > len(items) {
		end = len(items)
	}
	nodes := items[offset:end]
	edges := []any{}
	for i, node := range nodes {
		edges = append(edges, map[string]any{"node": node, "cursor": base64.StdEncoding.EncodeToString([]byte("cursor:"+strconv.Itoa(offset+i+1)))})
	}
	var endCursor any
	if len(nodes) > 0 {
		endCursor = base64.StdEncoding.EncodeToString([]byte("cursor:" + strconv.Itoa(offset+len(nodes))))
	}
	return map[string]any{"nodes": nodes, "edges": edges, "totalCount": len(items),
		"pageInfo": map[string]any{"hasNextPage": offset+first < len(items), "endCursor": endCursor}}
}

func commentMap(c *Comment) map[string]any {
	return map[string]any{"id": c.ID, "node_id": c.NodeID, "body": c.Body, "created_at": c.CreatedAt}
}

func gqlTypeName(v any, fallback string) string {
	switch x := v.(type) {
	case *Repository:
		return "Repository"
	case *Issue:
		return "Issue"
	case *Pull:
		return "PullRequest"
	case map[string]any:
		if tn, ok := x["__typename"].(string); ok {
			return tn
		}
	}
	return fallback
}

type gqlExec struct {
	f     *Forge
	frags map[string]gqlFragment
	errors []any
}

func (e *gqlExec) resolve(parent any, name string, args map[string]any, typ string) any {
	if name == "__typename" {
		return typ
	}
	f := e.f
	if typ == "Query" {
		switch name {
		case "viewer":
			login := f.Login
			if login == "" {
				login = "forge"
			}
			return gqlTyped("User", map[string]any{"login": login})
		case "repository":
			owner, _ := args["owner"].(string)
			rn, _ := args["name"].(string)
			r, err := safeGet(f, owner, rn)
			if err != nil {
				return nil
			}
			return r
		case "node":
			id, _ := args["id"].(string)
			return f.Nodes[id]
		case "__type":
			n, _ := args["name"].(string)
			fields, ok := gqlSchema[n]
			if !ok {
				return nil
			}
			var fs, ev []any
			for _, fn := range fields {
				fs = append(fs, map[string]any{"name": fn})
				ev = append(ev, map[string]any{"name": fn})
			}
			return map[string]any{"name": n, "fields": fs, "enumValues": ev}
		}
	}
	if typ == "Mutation" {
		input, _ := args["input"].(map[string]any)
		if input == nil {
			input = map[string]any{}
		}
		switch name {
		case "createIssue":
			r, ok := f.Nodes[str(input["repositoryId"])].(*Repository)
			if !ok {
				panic(fmt.Errorf("Unknown repository"))
			}
			return map[string]any{"issue": r.Issue(input)}
		case "closeIssue":
			i, ok := f.Nodes[str(input["issueId"])].(*Issue)
			if !ok {
				panic(fmt.Errorf("Unknown issue"))
			}
			i.State = "closed"
			i.cache = nil
			return map[string]any{"issue": i}
		case "createPullRequest":
			r, ok := f.Nodes[str(input["repositoryId"])].(*Repository)
			if !ok {
				panic(fmt.Errorf("Unknown repository"))
			}
			in := map[string]any{}
			for k, v := range input {
				in[k] = v
			}
			in["head"] = input["headRefName"]
			in["base"] = input["baseRefName"]
			return map[string]any{"pullRequest": r.Pull(in)}
		case "mergePullRequest":
			pr, ok := f.Nodes[str(input["pullRequestId"])].(*Pull)
			if !ok {
				panic(fmt.Errorf("Unknown pull request"))
			}
			pr.Repo.Merge(pr, input)
			return map[string]any{"pullRequest": pr}
		case "addComment":
			i, ok := f.Nodes[str(input["subjectId"])].(*Issue)
			if !ok {
				panic(fmt.Errorf("Unknown subject"))
			}
			return map[string]any{"commentEdge": map[string]any{"node": commentMap(i.Repo.Comment(i, input))}}
		}
	}
	if r, ok := parent.(*Repository); ok {
		switch name {
		case "id":
			return r.NodeID
		case "databaseId":
			return r.ID
		case "name":
			return r.Name
		case "nameWithOwner":
			return r.Owner + "/" + r.Name
		case "owner":
			return gqlTyped("User", map[string]any{"login": r.Owner})
		case "description":
			return r.Description
		case "defaultBranchRef":
			if _, ok := r.Git.Refs["refs/heads/"+r.Git.DefaultBranch]; ok {
				return map[string]any{"name": r.Git.DefaultBranch}
			}
			return nil
		case "viewerPermission":
			return "ADMIN"
		case "hasIssuesEnabled":
			return true
		case "issue":
			n := toInt(args["number"])
			for _, i := range r.Issues {
				if i.Number == n {
					return i
				}
			}
			return nil
		case "pullRequest":
			n := toInt(args["number"])
			for _, p := range r.Pulls {
				if p.Number == n {
					return p
				}
			}
			return nil
		case "issues":
			var list []*Issue
			for _, i := range r.Issues {
				if statesMatch(args["states"], strings.ToUpper(i.State)) {
					list = append(list, i)
				}
			}
			dir := "ASC"
			if ob, ok := args["orderBy"].(map[string]any); ok {
				if d, ok := ob["direction"].(string); ok {
					dir = d
				}
			}
			sort.Slice(list, func(i, j int) bool {
				if dir == "DESC" {
					return list[i].Number > list[j].Number
				}
				return list[i].Number < list[j].Number
			})
			var items []any
			for _, i := range list {
				items = append(items, i)
			}
			return gqlConn(items, args)
		case "pullRequests":
			var items []any
			for _, p := range r.Pulls {
				st := strings.ToUpper(p.State)
				if p.Merged {
					st = "MERGED"
				}
				if statesMatch(args["states"], st) {
					items = append(items, p)
				}
			}
			return gqlConn(items, args)
		}
	}
	if i, ok := parent.(*Issue); ok {
		switch name {
		case "id":
			return i.NodeID
		case "number":
			return i.Number
		case "title":
			return i.Title
		case "body":
			return i.Body
		case "state":
			return strings.ToUpper(i.State)
		case "createdAt":
			return i.CreatedAt
		case "updatedAt":
			return i.UpdatedAt
		case "comments":
			var items []any
			for _, c := range i.Comments_ {
				items = append(items, commentMap(c))
			}
			return gqlConn(items, args)
		}
	}
	if p, ok := parent.(*Pull); ok {
		switch name {
		case "id":
			return p.NodeID
		case "number":
			return p.Number
		case "title":
			return p.Title
		case "body":
			return p.Body
		case "merged":
			return p.Merged
		case "state":
			if p.Merged {
				return "MERGED"
			}
			return strings.ToUpper(p.State)
		case "headRefName":
			return p.Head["ref"]
		case "baseRefName":
			return p.Base["ref"]
		case "mergeable":
			_, conflict := p.Repo.MergeFiles(p)
			if conflict {
				return "CONFLICTING"
			}
			return "MERGEABLE"
		case "files":
			var items []any
			for _, x := range p.Repo.Files(p) {
				items = append(items, map[string]any{"path": x["filename"]})
			}
			return gqlConn(items, args)
		case "createdAt":
			return p.CreatedAt
		case "updatedAt":
			return p.CreatedAt
		case "comments":
			return gqlConn([]any{}, args)
		}
	}
	if m, ok := parent.(map[string]any); ok {
		if name == "id" && m["node_id"] != nil {
			return m["node_id"]
		}
		if v, ok := m[name]; ok {
			return v
		}
	}
	panic(fmt.Errorf("Cannot query field %q on type %q.", name, typ))
}

func statesMatch(states any, st string) bool {
	if states == nil {
		return true
	}
	arr, ok := states.([]any)
	if !ok {
		return true
	}
	for _, s := range arr {
		if str(s) == st {
			return true
		}
	}
	return false
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func safeGet(f *Forge, owner, name string) (r *Repository, err any) {
	defer func() { err = recover() }()
	return f.Get(owner, name), nil
}

func (e *gqlExec) project(parent any, fields []gqlField, typ string) any {
	if parent == nil {
		return nil
	}
	if arr, ok := parent.([]any); ok {
		out := []any{}
		for _, x := range arr {
			out = append(out, e.project(x, fields, gqlTypeName(x, typ)))
		}
		return out
	}
	out := map[string]any{}
	for _, field := range fields {
		if field.Fragment != "" {
			if typ == field.Fragment {
				mergeMap(out, e.project(parent, field.Selection, typ).(map[string]any))
			}
			continue
		}
		if field.Spread != "" {
			frag, ok := e.frags[field.Spread]
			if ok && frag.typ == typ {
				mergeMap(out, e.project(parent, frag.selection, typ).(map[string]any))
			}
			continue
		}
		skip := false
		for _, d := range field.Directives {
			if (d.d == "skip" && d.yes) || (d.d == "include" && !d.yes) {
				skip = true
			}
		}
		if skip {
			continue
		}
		val, err := e.safeResolve(parent, field.Name, field.Args, typ)
		if err != nil {
			e.errors = append(e.errors, map[string]any{"message": fmt.Sprint(err)})
			out[field.Alias] = nil
			continue
		}
		if field.Selection != nil {
			out[field.Alias] = e.project(val, field.Selection, gqlTypeName(val, field.Name))
		} else {
			out[field.Alias] = val
		}
	}
	return out
}

func (e *gqlExec) safeResolve(parent any, name string, args map[string]any, typ string) (v any, err any) {
	defer func() { err = recover() }()
	return e.resolve(parent, name, args, typ), nil
}

func mergeMap(dst, src map[string]any) {
	for k, v := range src {
		dst[k] = v
	}
}

func executeGraphQL(f *Forge, request map[string]any) any {
	query, _ := request["query"].(string)
	vars, _ := request["variables"].(map[string]any)
	if vars == nil {
		vars = map[string]any{}
	}
	opName, _ := request["operationName"].(string)
	ops, frags := gqlParse(query, vars)
	var op *gqlOp
	for i := range ops {
		if opName == "" || ops[i].name == opName {
			op = &ops[i]
			break
		}
	}
	if op == nil {
		return map[string]any{"errors": []any{map[string]any{"message": "Unknown operation"}}}
	}
	e := &gqlExec{f: f, frags: frags}
	rootType := "Query"
	if op.kind == "mutation" {
		rootType = "Mutation"
	}
	data := e.project(map[string]any{}, op.selection, rootType)
	if len(e.errors) > 0 {
		return map[string]any{"data": data, "errors": e.errors}
	}
	return map[string]any{"data": data}
}
