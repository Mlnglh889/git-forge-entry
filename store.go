package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

type Forge struct {
	Repos   map[string]*Repository
	Nodes   map[string]any
	Login   string
	Counter int
	Now     string
}

func NewForge() *Forge {
	now := os.Getenv("MAKEFUN_TRIAL_NOW")
	if now == "" {
		now = "2025-01-01T00:00:00.000Z"
	}
	login := os.Getenv("FORGE_LOGIN")
	return &Forge{Repos: map[string]*Repository{}, Nodes: map[string]any{}, Login: login, Now: now}
}

func (f *Forge) ID(kind string) string {
	f.Counter++
	return base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%s:%d", kind, f.Counter)))
}

func (f *Forge) Bind(owner string) {
	if f.Login == "" {
		f.Login = owner
		for _, r := range f.Repos {
			r.Owner = owner
		}
	}
}

type httpError struct {
	status int
	msg    string
}

func (e *httpError) Error() string { return e.msg }

func herr(status int, msg string) *httpError { return &httpError{status, msg} }

func requireValue(v bool, msg string) {
	if !v {
		panic(herr(422, msg))
	}
}

func (f *Forge) Create(b map[string]any) *Repository {
	name, _ := b["name"].(string)
	if !validRepoName(name) {
		panic(herr(422, "Invalid repository name"))
	}
	if _, ok := f.Repos[strings.ToLower(name)]; ok {
		panic(herr(422, "Repository already exists"))
	}
	r := NewRepository(f, b)
	f.Repos[strings.ToLower(r.Name)] = r
	f.Nodes[r.NodeID] = r
	return r
}

func validRepoName(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if !(c == '_' || c == '.' || c == '-' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')) {
			return false
		}
	}
	return true
}

func (f *Forge) Get(owner, name string) *Repository {
	r, ok := f.Repos[strings.ToLower(name)]
	if !ok {
		panic(herr(404, "Not Found"))
	}
	f.Bind(owner)
	login := f.Login
	if login == "" {
		login = "forge"
	}
	if !strings.EqualFold(owner, login) {
		panic(herr(404, "Not Found"))
	}
	return r
}

type Repository struct {
	Forge       *Forge
	ID          int
	NodeID      string
	Name        string
	Owner       string
	Private     bool
	Description string
	Git         *GitStore
	Issues      []*Issue
	IssueMap    map[int]*Issue
	Pulls       []*Pull
	Labels      map[string]map[string]any
	NextNumber  int
}

func NewRepository(f *Forge, b map[string]any) *Repository {
	nodeID := f.ID("Repository")
	owner := f.Login
	if owner == "" {
		owner = "forge"
	}
	r := &Repository{Forge: f, NodeID: nodeID, ID: f.Counter, Name: b["name"].(string), Owner: owner,
		Private: truthy(b["private"]), Git: NewGitStore(), IssueMap: map[int]*Issue{}, Labels: map[string]map[string]any{}, NextNumber: 1}
	if d, ok := b["description"].(string); ok {
		r.Description = d
	}
	if db, ok := b["default_branch"].(string); ok && db != "" {
		r.Git.DefaultBranch = db
	}
	return r
}

func truthy(v any) bool {
	b, _ := v.(bool)
	return b
}

func (r *Repository) View() map[string]any {
	return map[string]any{
		"id": r.ID, "node_id": r.NodeID, "name": r.Name, "full_name": r.Owner + "/" + r.Name,
		"private": r.Private, "description": r.Description, "default_branch": r.Git.DefaultBranch,
		"owner": map[string]any{"login": r.Owner}, "has_issues": true,
		"created_at": r.Forge.Now, "updated_at": r.Forge.Now,
	}
}

type Issue struct {
	ID        int
	NodeID    string
	Number    int
	Title     string
	Body      string
	State     string
	Labels    []map[string]any
	Comments  int
	CreatedAt string
	UpdatedAt string
	Comments_ []*Comment
	Repo      *Repository
	cache     []byte
}

type Comment struct {
	ID        int    `json:"id"`
	NodeID    string `json:"node_id"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
}

func (r *Repository) Issue(b map[string]any) *Issue {
	title, _ := b["title"].(string)
	requireValue(title != "", "Validation Failed")
	var labels []map[string]any
	if ls, ok := b["labels"].([]any); ok {
		for _, l := range ls {
			var name string
			switch v := l.(type) {
			case string:
				name = v
			case map[string]any:
				name, _ = v["name"].(string)
			}
			label, ok := r.Labels[name]
			if !ok {
				panic(herr(422, "Unknown label"))
			}
			labels = append(labels, label)
		}
	}
	nodeID := r.Forge.ID("Issue")
	body, _ := b["body"].(string)
	i := &Issue{ID: r.Forge.Counter, NodeID: nodeID, Number: r.NextNumber, Title: title, Body: body,
		State: "open", Labels: labels, Comments: 0, CreatedAt: r.Forge.Now, UpdatedAt: r.Forge.Now, Repo: r}
	r.NextNumber++
	r.Issues = append(r.Issues, i)
	r.IssueMap[i.Number] = i
	r.Forge.Nodes[nodeID] = i
	return i
}

func (r *Repository) IssueView(i *Issue) map[string]any {
	labels := i.Labels
	if labels == nil {
		labels = []map[string]any{}
	}
	return map[string]any{
		"id": i.ID, "node_id": i.NodeID, "number": i.Number, "title": i.Title, "body": i.Body,
		"state": i.State, "labels": labels, "comments": i.Comments, "created_at": i.CreatedAt, "updated_at": i.UpdatedAt,
	}
}

func (r *Repository) IssueBytes(i *Issue) []byte {
	if i.cache == nil {
		b, _ := json.Marshal(r.IssueView(i))
		i.cache = append(b, '\n')
	}
	return i.cache
}

func (r *Repository) Comment(i *Issue, b map[string]any) *Comment {
	body, ok := b["body"].(string)
	requireValue(ok, "Validation Failed")
	nodeID := r.Forge.ID("IssueComment")
	c := &Comment{ID: r.Forge.Counter, NodeID: nodeID, Body: body, CreatedAt: r.Forge.Now}
	i.Comments_ = append(i.Comments_, c)
	i.Comments = len(i.Comments_)
	i.cache = nil
	r.Forge.Nodes[nodeID] = c
	return c
}

type Pull struct {
	ID              int
	NodeID          string
	Number          int
	Title           string
	Body            string
	State           string
	Merged          bool
	MergedAt        any
	Head            map[string]any
	Base            map[string]any
	CreatedAt       string
	MergeCommitSHA  string
	Repo            *Repository
}

func (r *Repository) Pull(b map[string]any) *Pull {
	head, _ := b["head"].(string)
	base, _ := b["base"].(string)
	requireValue(head != "" && base != "", "Validation Failed")
	hparts := strings.Split(head, ":")
	h := hparts[len(hparts)-1]
	hs, hok := r.Git.Refs["refs/heads/"+h]
	bs, bok := r.Git.Refs["refs/heads/"+base]
	requireValue(hok && bok, "Unknown head or base")
	nodeID := r.Forge.ID("PullRequest")
	title, _ := b["title"].(string)
	body, _ := b["body"].(string)
	p := &Pull{ID: r.Forge.Counter, NodeID: nodeID, Number: r.NextNumber, Title: title, Body: body,
		State: "open", Merged: false, MergedAt: nil, Head: map[string]any{"ref": h, "sha": hs},
		Base: map[string]any{"ref": base, "sha": bs}, CreatedAt: r.Forge.Now, Repo: r}
	r.NextNumber++
	r.Pulls = append(r.Pulls, p)
	r.Forge.Nodes[nodeID] = p
	return p
}

func (r *Repository) PullView(p *Pull) map[string]any {
	_, conflict := r.MergeFiles(p)
	v := map[string]any{
		"id": p.ID, "node_id": p.NodeID, "number": p.Number, "title": p.Title, "body": p.Body,
		"state": p.State, "merged": p.Merged, "merged_at": p.MergedAt, "head": p.Head, "base": p.Base,
		"created_at": p.CreatedAt, "mergeable": !conflict,
	}
	if p.MergeCommitSHA != "" {
		v["merge_commit_sha"] = p.MergeCommitSHA
	}
	return v
}

func (r *Repository) BaseOf(a, b string) string {
	as := r.Git.Ancestors(a)
	for _, id := range r.Git.AncestorsOrder(b) {
		if as[id] {
			return id
		}
	}
	return ""
}

func (r *Repository) MergeFiles(p *Pull) (map[string]FileEntry, bool) {
	headRef, _ := p.Head["ref"].(string)
	headSha, _ := p.Head["sha"].(string)
	baseRef, _ := p.Base["ref"].(string)
	baseSha, _ := p.Base["sha"].(string)
	hTip := r.Git.Refs["refs/heads/"+headRef]
	if hTip == "" {
		hTip = headSha
	}
	baseTip := r.Git.Refs["refs/heads/"+baseRef]
	if baseTip == "" {
		baseTip = baseSha
	}
	h := r.Git.Files(hTip)
	b := r.Git.Files(baseTip)
	ancestor := r.BaseOf(headSha, baseTip)
	old := r.Git.Files(ancestor)
	result := map[string]FileEntry{}
	for k, v := range b {
		result[k] = v
	}
	conflict := false
	paths := map[string]bool{}
	for k := range old {
		paths[k] = true
	}
	for k := range h {
		paths[k] = true
	}
	for path := range paths {
		before, bok := old[path]
		after, aok := h[path]
		current, cok := b[path]
		if bok && aok && before.OID == after.OID {
			continue
		}
		if cok && (!bok || current.OID != before.OID) && (!aok || current.OID != after.OID) {
			conflict = true
		} else if aok {
			result[path] = after
		} else {
			delete(result, path)
		}
	}
	return result, conflict
}

func (r *Repository) Files(p *Pull) []map[string]any {
	headSha, _ := p.Head["sha"].(string)
	baseSha, _ := p.Base["sha"].(string)
	baseTip := r.BaseOf(headSha, baseSha)
	if baseTip == "" {
		baseTip = baseSha
	}
	base := r.Git.Files(baseTip)
	head := r.Git.Files(headSha)
	paths := map[string]bool{}
	for k := range base {
		paths[k] = true
	}
	for k := range head {
		paths[k] = true
	}
	var sorted []string
	for k := range paths {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	out := []map[string]any{}
	lines := func(id string) int {
		if id == "" {
			return 0
		}
		o := r.Git.Objects[id]
		if o == nil {
			return 0
		}
		n := 0
		for _, l := range strings.Split(string(o.Data), "\n") {
			if l != "" {
				n++
			}
		}
		return n
	}
	for _, path := range sorted {
		a, aok := base[path]
		b, bok := head[path]
		if aok && bok && a.OID == b.OID {
			continue
		}
		var oid string
		if bok {
			oid = b.OID
		} else {
			oid = a.OID
		}
		status := "modified"
		if !aok {
			status = "added"
		} else if !bok {
			status = "removed"
		}
		add, del := lines(b.OID), lines(a.OID)
		out = append(out, map[string]any{"sha": oid, "filename": path, "status": status,
			"additions": add, "deletions": del, "changes": add + del, "patch": ""})
	}
	return out
}

func (r *Repository) Merge(p *Pull, b map[string]any) map[string]any {
	if p.Merged {
		return map[string]any{"sha": p.MergeCommitSHA, "merged": true, "message": "Already merged"}
	}
	result, conflict := r.MergeFiles(p)
	if conflict {
		panic(herr(405, "Pull Request is not mergeable"))
	}
	baseRef, _ := p.Base["ref"].(string)
	parent := r.Git.Refs["refs/heads/"+baseRef]
	msg, _ := b["commit_message"].(string)
	if msg == "" {
		msg, _ = b["commit_title"].(string)
	}
	if msg == "" {
		msg = p.Title
	}
	sha := r.Git.Commit(result, []string{parent}, msg)
	r.Git.SetRef("refs/heads/"+baseRef, sha)
	p.Merged = true
	p.State = "closed"
	p.MergedAt = r.Forge.Now
	p.MergeCommitSHA = sha
	return map[string]any{"sha": sha, "merged": true, "message": "Pull Request successfully merged"}
}

func (r *Repository) Content(path, ref string) any {
	tip := r.Git.Refs[ref]
	if tip == "" {
		tip = r.Git.Refs["refs/heads/"+ref]
	}
	if tip == "" {
		tip = ref
	}
	files := r.Git.Files(tip)
	if entry, ok := files[path]; ok {
		data := r.Git.Objects[entry.OID].Data
		name := path
		if i := strings.LastIndex(path, "/"); i >= 0 {
			name = path[i+1:]
		}
		return map[string]any{"type": "file", "name": name, "path": path, "sha": entry.OID,
			"size": len(data), "encoding": "base64", "content": base64.StdEncoding.EncodeToString(data)}
	}
	prefix := ""
	if path != "" {
		prefix = strings.TrimSuffix(path, "/") + "/"
	}
	dirs := map[string]map[string]any{}
	for p, e := range files {
		if !strings.HasPrefix(p, prefix) {
			continue
		}
		rest := p[len(prefix):]
		name := strings.SplitN(rest, "/", 2)[0]
		directory := strings.Contains(rest, "/")
		typ := "file"
		if directory {
			typ = "dir"
		}
		dirs[name] = map[string]any{"name": name, "path": prefix + name, "type": typ, "sha": e.OID}
	}
	if len(dirs) == 0 {
		panic(herr(404, "Not Found"))
	}
	out := []map[string]any{}
	for _, v := range dirs {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["name"].(string) < out[j]["name"].(string) })
	return out
}

func (r *Repository) PutContent(path string, b map[string]any, del bool) (int, map[string]any) {
	parts := strings.Split(path, "/")
	bad := path == ""
	for _, p := range parts {
		if p == "" || p == "." || p == ".." {
			bad = true
		}
	}
	requireValue(!bad, "Invalid path")
	branch, _ := b["branch"].(string)
	if branch == "" {
		branch = r.Git.DefaultBranch
	}
	ref := "refs/heads/" + branch
	tip := r.Git.Refs[ref]
	files := r.Git.Files(tip)
	old, oldOK := files[path]
	if del && !oldOK {
		panic(herr(404, "Not Found"))
	}
	sha, _ := b["sha"].(string)
	if (oldOK && sha != old.OID) || (!oldOK && sha != "") {
		panic(herr(409, "SHA does not match"))
	}
	var content any
	if del {
		delete(files, path)
	} else {
		cs, ok := b["content"].(string)
		requireValue(ok, "Missing content")
		data, err := base64.StdEncoding.DecodeString(cs)
		if err != nil {
			panic(herr(422, "Invalid content"))
		}
		id := r.Git.Put("blob", data)
		files[path] = FileEntry{OID: id, Mode: "100644"}
		name := path
		if i := strings.LastIndex(path, "/"); i >= 0 {
			name = path[i+1:]
		}
		content = map[string]any{"name": name, "path": path, "sha": id, "encoding": "base64", "size": len(data), "type": "file"}
	}
	msg, _ := b["message"].(string)
	if msg == "" {
		msg = "Update " + path
	}
	var parents []string
	if tip != "" {
		parents = []string{tip}
	}
	sha2 := r.Git.Commit(files, parents, msg)
	r.Git.SetRef(ref, sha2)
	status := 201
	if oldOK {
		status = 200
	}
	return status, map[string]any{"content": content, "commit": map[string]any{"sha": sha2, "message": msg}}
}
