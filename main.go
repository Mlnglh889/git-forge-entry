package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

var requestID int64
var gitPathRe = regexp.MustCompile(`^/([^/]+)/([^/]+)\.git/(info/refs|git-upload-pack|git-receive-pack)$`)

type App struct {
	forge        *Forge
	expectedAuth string
}

func writeJSON(w http.ResponseWriter, status int, value any, extra map[string]string) {
	var b []byte
	if status == 204 || (status == 202 && value == nil) {
		b = []byte{}
	} else {
		b, _ = json.Marshal(value)
		b = append(b, '\n')
	}
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Content-Length", strconv.Itoa(len(b)))
	h.Set("X-GitHub-Request-Id", strconv.FormatInt(atomic.AddInt64(&requestID, 1), 10))
	for k, v := range extra {
		h.Set(k, v)
	}
	w.WriteHeader(status)
	w.Write(b)
}

func writeRaw(w http.ResponseWriter, status int, b []byte, extra map[string]string) {
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Content-Length", strconv.Itoa(len(b)))
	h.Set("X-GitHub-Request-Id", strconv.FormatInt(atomic.AddInt64(&requestID, 1), 10))
	for k, v := range extra {
		h.Set(k, v)
	}
	w.WriteHeader(status)
	w.Write(b)
}

func parseBody(b []byte) map[string]any {
	if len(b) == 0 {
		return map[string]any{}
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		panic(herr(400, "Invalid JSON"))
	}
	if m == nil {
		m = map[string]any{}
	}
	return m
}

func (a *App) page(w http.ResponseWriter, r *http.Request, u *url.URL, items []any) {
	per := 30
	if v := u.Query().Get("per_page"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			per = n
		}
	}
	if per < 1 {
		per = 1
	}
	if per > 100 {
		per = 100
	}
	num := 1
	if v := u.Query().Get("page"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			num = n
		}
	}
	extra := map[string]string{}
	if num*per < len(items) {
		next := *u
		q := next.Query()
		q.Set("per_page", strconv.Itoa(per))
		q.Set("page", strconv.Itoa(num+1))
		next.RawQuery = q.Encode()
		extra["Link"] = "<http://" + r.Host + next.RequestURI() + ">; rel=\"next\""
	}
	lo := (num - 1) * per
	hi := num * per
	if lo > len(items) {
		lo = len(items)
	}
	if hi > len(items) {
		hi = len(items)
	}
	writeJSON(w, 200, items[lo:hi], extra)
}

func refName(name string) string {
	if strings.HasPrefix(name, "refs/") {
		return name
	}
	return "refs/heads/" + name
}

func (a *App) handle(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if e := recover(); e != nil {
			if he, ok := e.(*httpError); ok {
				writeJSON(w, he.status, map[string]any{"message": he.msg}, nil)
			} else {
				writeJSON(w, 500, map[string]any{"message": fmt.Sprint(e)}, nil)
			}
		}
	}()
	path := r.URL.Path
	method := r.Method
	if path == "/healthz" {
		writeJSON(w, 200, map[string]any{"ok": true}, nil)
		return
	}
	auth := r.Header.Get("Authorization")
	if a.expectedAuth != "" {
		if auth != a.expectedAuth {
			writeJSON(w, 401, map[string]any{"message": "Requires authentication"}, nil)
			return
		}
	} else if !strings.HasPrefix(auth, "Bearer ") || auth[7:] == "" {
		writeJSON(w, 401, map[string]any{"message": "Requires authentication"}, nil)
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 32*1024*1024+1))
	if len(body) > 32*1024*1024 {
		panic(herr(413, "Body too large"))
	}
	if path == "/mcp" {
		a.mcp(w, body)
		return
	}
	if path == "/api/graphql" {
		var x any
		if len(body) > 0 {
			if err := json.Unmarshal(body, &x); err != nil {
				panic(herr(400, "Invalid JSON"))
			}
		}
		switch v := x.(type) {
		case []any:
			out := []any{}
			for _, item := range v {
				m, _ := item.(map[string]any)
				if m == nil {
					m = map[string]any{}
				}
				out = append(out, executeGraphQL(a.forge, m))
			}
			writeJSON(w, 200, out, nil)
		case map[string]any:
			writeJSON(w, 200, executeGraphQL(a.forge, v), nil)
		default:
			writeJSON(w, 200, executeGraphQL(a.forge, map[string]any{}), nil)
		}
		return
	}
	if m := gitPathRe.FindStringSubmatch(path); m != nil {
		owner, _ := url.PathUnescape(m[1])
		repo, _ := url.PathUnescape(m[2])
		rp := a.forge.Get(owner, repo)
		advertise := m[3] == "info/refs"
		service := m[3]
		if advertise {
			service = r.URL.Query().Get("service")
		}
		if service != "git-upload-pack" && service != "git-receive-pack" {
			panic(herr(400, "Unknown git service"))
		}
		v2 := strings.Contains(r.Header.Get("Git-Protocol"), "version=2")
		data := gitResponse(rp.Git, service, advertise, v2, body)
		kind := "result"
		if advertise {
			kind = "advertisement"
		}
		w.Header().Set("Content-Type", "application/x-"+service+"-"+kind)
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		w.WriteHeader(200)
		w.Write(data)
		return
	}
	if !strings.HasPrefix(path, "/api/v3") {
		panic(herr(404, "Not Found"))
	}
	var parts []string
	for _, p := range strings.Split(path[7:], "/") {
		if p != "" {
			d, _ := url.PathUnescape(p)
			parts = append(parts, d)
		}
	}
	b := parseBody(body)
	u := r.URL
	f := a.forge
	if len(parts) == 0 {
		writeJSON(w, 200, map[string]any{"current_user_url": "/api/v3/user", "repository_url": "/api/v3/repos/{owner}/{repo}"}, map[string]string{"X-OAuth-Scopes": "repo, read:org"})
		return
	}
	if parts[0] == "meta" {
		writeJSON(w, 200, map[string]any{"installed_version": "local-forge-2.0.0"}, nil)
		return
	}
	if parts[0] == "user" && len(parts) == 1 {
		login := f.Login
		if login == "" {
			login = "forge"
		}
		writeJSON(w, 200, map[string]any{"login": login}, nil)
		return
	}
	if parts[0] == "user" && len(parts) > 1 && parts[1] == "repos" {
		if method == "POST" {
			writeJSON(w, 201, f.Create(b).View(), nil)
			return
		}
		if method == "GET" {
			items := []any{}
			var repos []*Repository
			for _, rp := range f.Repos {
				repos = append(repos, rp)
			}
			sort.Slice(repos, func(i, j int) bool { return repos[i].ID < repos[j].ID })
			for _, rp := range repos {
				items = append(items, rp.View())
			}
			a.page(w, r, u, items)
			return
		}
	}
	if parts[0] != "repos" || len(parts) < 3 {
		panic(herr(404, "Not Found"))
	}
	rp := f.Get(parts[1], parts[2])
	tail := parts[3:]
	if len(tail) == 0 {
		if method == "GET" {
			writeJSON(w, 200, rp.View(), nil)
			return
		}
		if method == "PATCH" {
			if d, ok := b["description"].(string); ok {
				rp.Description = d
			}
			writeJSON(w, 200, rp.View(), nil)
			return
		}
		if method == "DELETE" {
			delete(f.Repos, strings.ToLower(rp.Name))
			writeJSON(w, 204, nil, nil)
			return
		}
	}
	if tail[0] == "git" && (tail[1] == "refs" || tail[1] == "ref") {
		if method == "POST" && len(tail) == 2 {
			ref, _ := b["ref"].(string)
			sha, _ := b["sha"].(string)
			requireValue(strings.HasPrefix(ref, "refs/") && rp.Git.Objects[sha] != nil, "Validation Failed")
			if _, ok := rp.Git.Refs[ref]; ok {
				panic(herr(422, "Reference already exists"))
			}
			rp.Git.SetRef(ref, sha)
			writeJSON(w, 201, map[string]any{"ref": ref, "object": map[string]any{"type": "commit", "sha": sha}}, nil)
			return
		}
		name := "refs/" + strings.Join(tail[2:], "/")
		id, ok := rp.Git.Refs[name]
		if !ok {
			panic(herr(404, "Not Found"))
		}
		writeJSON(w, 200, map[string]any{"ref": name, "object": map[string]any{"sha": id, "type": "commit"}}, nil)
		return
	}
	if tail[0] == "branches" {
		type br struct {
			name string
			sha  string
		}
		var bs []br
		for n, sha := range rp.Git.Refs {
			if strings.HasPrefix(n, "refs/heads/") {
				bs = append(bs, br{n[11:], sha})
			}
		}
		sort.Slice(bs, func(i, j int) bool { return bs[i].name < bs[j].name })
		if len(tail) > 1 {
			want := strings.Join(tail[1:], "/")
			for _, x := range bs {
				if x.name == want {
					writeJSON(w, 200, map[string]any{"name": x.name, "commit": map[string]any{"sha": x.sha}}, nil)
					return
				}
			}
			panic(herr(404, "Not Found"))
		}
		items := []any{}
		for _, x := range bs {
			items = append(items, map[string]any{"name": x.name, "commit": map[string]any{"sha": x.sha}})
		}
		a.page(w, r, u, items)
		return
	}
	if tail[0] == "commits" {
		sha := u.Query().Get("sha")
		if sha == "" {
			sha = rp.Git.DefaultBranch
		}
		tip := rp.Git.Refs[refName(sha)]
		if tip == "" {
			tip = sha
		}
		items := []any{}
		for _, id := range rp.Git.AncestorsOrder(tip) {
			c := rp.Git.CommitView(id)
			if c == nil {
				continue
			}
			var parents []any
			for _, p := range c.Parents {
				parents = append(parents, map[string]any{"sha": p})
			}
			items = append(items, map[string]any{"sha": id, "commit": map[string]any{"message": c.Message}, "parents": parents})
		}
		a.page(w, r, u, items)
		return
	}
	if tail[0] == "contents" {
		p := strings.Join(tail[1:], "/")
		if method == "GET" {
			ref := u.Query().Get("ref")
			if ref == "" {
				ref = rp.Git.DefaultBranch
			}
			writeJSON(w, 200, rp.Content(p, ref), nil)
			return
		}
		if method == "PUT" || method == "DELETE" {
			status, body := rp.PutContent(p, b, method == "DELETE")
			if method == "DELETE" {
				status = 200
			}
			writeJSON(w, status, body, nil)
			return
		}
	}
	if tail[0] == "labels" {
		if method == "POST" {
			name, ok := b["name"].(string)
			requireValue(ok, "Validation Failed")
			color, _ := b["color"].(string)
			if color == "" {
				color = "ededed"
			}
			label := map[string]any{"name": name, "color": color}
			rp.Labels[name] = label
			writeJSON(w, 201, label, nil)
			return
		}
		items := []any{}
		var names []string
		for n := range rp.Labels {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			items = append(items, rp.Labels[n])
		}
		writeJSON(w, 200, items, nil)
		return
	}
	if tail[0] == "issues" {
		if len(tail) == 1 {
			if method == "POST" {
				writeJSON(w, 201, rp.IssueView(rp.Issue(b)), nil)
				return
			}
			state := u.Query().Get("state")
			if state == "" {
				state = "open"
			}
			var list []*Issue
			for _, i := range rp.Issues {
				if state == "all" || i.State == state {
					list = append(list, i)
				}
			}
			asc := u.Query().Get("direction") == "asc"
			sort.Slice(list, func(i, j int) bool {
				if asc {
					return list[i].Number < list[j].Number
				}
				return list[i].Number > list[j].Number
			})
			items := []any{}
			for _, i := range list {
				items = append(items, rp.IssueView(i))
			}
			a.page(w, r, u, items)
			return
		}
		num, _ := strconv.Atoi(tail[1])
		i := rp.IssueMap[num]
		if i == nil {
			panic(herr(404, "Not Found"))
		}
		if len(tail) > 2 && tail[2] == "comments" {
			if method == "POST" {
				writeJSON(w, 201, rp.Comment(i, b), nil)
				return
			}
			items := []any{}
			for _, c := range i.Comments_ {
				items = append(items, c)
			}
			a.page(w, r, u, items)
			return
		}
		if method == "PATCH" {
			if s, ok := b["state"].(string); ok && s != "" {
				i.State = s
			}
			if t, ok := b["title"].(string); ok {
				i.Title = t
			}
			if bd, ok := b["body"].(string); ok {
				i.Body = bd
			}
			i.cache = nil
		}
		writeRaw(w, 200, rp.IssueBytes(i), nil)
		return
	}
	if tail[0] == "pulls" {
		if len(tail) == 1 {
			if method == "POST" {
				writeJSON(w, 201, rp.PullView(rp.Pull(b)), nil)
				return
			}
			state := u.Query().Get("state")
			if state == "" {
				state = "open"
			}
			items := []any{}
			for _, p := range rp.Pulls {
				if state == "all" || p.State == state {
					items = append(items, rp.PullView(p))
				}
			}
			a.page(w, r, u, items)
			return
		}
		num, _ := strconv.Atoi(tail[1])
		var p *Pull
		for _, x := range rp.Pulls {
			if x.Number == num {
				p = x
				break
			}
		}
		if p == nil {
			panic(herr(404, "Not Found"))
		}
		if len(tail) > 2 && tail[2] == "files" {
			items := []any{}
			for _, x := range rp.Files(p) {
				items = append(items, x)
			}
			a.page(w, r, u, items)
			return
		}
		if len(tail) > 2 && tail[2] == "merge" && method == "PUT" {
			writeJSON(w, 200, rp.Merge(p, b), nil)
			return
		}
		writeJSON(w, 200, rp.PullView(p), nil)
		return
	}
	panic(herr(404, "Not Found"))
}

func main() {
	forge := NewForge()
	token := os.Getenv("MAKEFUN_SHOP_TOKEN")
	app := &App{forge: forge}
	if token != "" {
		app.expectedAuth = "Bearer " + token
	}
	handler := http.HandlerFunc(app.handle)
	port := os.Getenv("PORT")
	addr := ":0"
	if port != "" {
		addr = ":" + port
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	actualPort := ln.Addr().(*net.TCPAddr).Port
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 30 * time.Second}
	go srv.Serve(ln)
	env := strings.TrimRight(os.Getenv("MAKEFUN_ENV_URL"), "/")
	if env == "" {
		fmt.Println("listening", actualPort)
		select {}
	}
	client := &http.Client{Timeout: 30 * time.Second}
	regBody, _ := json.Marshal(map[string]any{"port": actualPort})
	for {
		resp, err := client.Post(env+"/register", "application/json", bytes.NewReader(regBody))
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 || resp.StatusCode == 409 {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	for {
		resp, err := client.Get(env + "/done")
		if err == nil {
			var d map[string]any
			json.NewDecoder(resp.Body).Decode(&d)
			resp.Body.Close()
			if resp.StatusCode == 200 && d["done"] == true {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	srv.Close()
	os.Exit(0)
}
