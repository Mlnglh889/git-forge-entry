package main

import (
	"bytes"
	"compress/zlib"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

const ZERO = "0000000000000000000000000000000000000000"

type GitObject struct {
	Type string
	Data []byte
}

func hashBytes(b []byte) []byte {
	h := sha1.Sum(b)
	return h[:]
}

func objectID(kind string, data []byte) string {
	h := sha1.New()
	h.Write([]byte(fmt.Sprintf("%s %d\x00", kind, len(data))))
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

func packet(data []byte) []byte {
	return append([]byte(fmt.Sprintf("%04x", len(data)+4)), data...)
}

func packetStr(s string) []byte { return packet([]byte(s)) }

var FLUSH = []byte("0000")
var DELIM = []byte("0001")

func isHex4(b []byte) bool {
	for _, c := range b {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

func packets(b []byte) ([][]byte, []byte) {
	var lines [][]byte
	offset := 0
	for offset+4 <= len(b) {
		h := b[offset : offset+4]
		if !isHex4(h) {
			break
		}
		n, _ := strconv.ParseUint(string(h), 16, 32)
		offset += 4
		if n == 0 {
			break
		}
		if n == 1 || n == 2 {
			continue
		}
		if n < 4 || offset+int(n)-4 > len(b) {
			panic(fmt.Errorf("Truncated packet"))
		}
		lines = append(lines, b[offset:offset+int(n)-4])
		offset += int(n) - 4
	}
	return lines, b[offset:]
}

type TreeEntry struct {
	Mode string
	Name string
	OID  string
}

func parseTree(b []byte) []TreeEntry {
	var entries []TreeEntry
	i := 0
	for i < len(b) {
		sp := bytes.IndexByte(b[i:], 32)
		if sp < 0 {
			panic(fmt.Errorf("Invalid tree"))
		}
		sp += i
		nul := bytes.IndexByte(b[sp:], 0)
		if nul < 0 {
			panic(fmt.Errorf("Invalid tree"))
		}
		nul += sp
		if nul+21 > len(b) {
			panic(fmt.Errorf("Invalid tree"))
		}
		entries = append(entries, TreeEntry{Mode: string(b[i:sp]), Name: string(b[sp+1 : nul]), OID: hex.EncodeToString(b[nul+1 : nul+21])})
		i = nul + 21
	}
	return entries
}

type CommitInfo struct {
	Tree    string
	Parents []string
	Message string
}

func parseCommit(o *GitObject) *CommitInfo {
	text := string(o.Data)
	blank := strings.Index(text, "\n\n")
	headers := text
	msg := ""
	if blank >= 0 {
		headers = text[:blank]
		msg = text[blank+2:]
	}
	ci := &CommitInfo{Message: msg}
	for _, line := range strings.Split(headers, "\n") {
		if strings.HasPrefix(line, "tree ") {
			ci.Tree = strings.TrimSpace(line[5:])
		} else if strings.HasPrefix(line, "parent ") {
			ci.Parents = append(ci.Parents, strings.TrimSpace(line[7:]))
		}
	}
	return ci
}

func applyDelta(base, d []byte) []byte {
	p := 0
	size := func() int {
		n, s := 0, 0
		for {
			x := d[p]
			p++
			n += int(x&127) << s
			s += 7
			if x&128 == 0 {
				break
			}
		}
		return n
	}
	if size() != len(base) {
		panic(fmt.Errorf("Delta base length mismatch"))
	}
	out := make([]byte, size())
	written := 0
	for p < len(d) {
		op := d[p]
		p++
		if op&128 != 0 {
			at, ln := 0, 0
			for b := 0; b < 4; b++ {
				if op&(1<<b) != 0 {
					at += int(d[p]) << (8 * b)
					p++
				}
			}
			for b := 0; b < 3; b++ {
				if op&(16<<b) != 0 {
					ln += int(d[p]) << (8 * b)
					p++
				}
			}
			if ln == 0 {
				ln = 65536
			}
			if at+ln > len(base) || written+ln > len(out) {
				panic(fmt.Errorf("Delta bounds"))
			}
			copy(out[written:], base[at:at+ln])
			written += ln
		} else {
			if op == 0 || p+int(op) > len(d) || written+int(op) > len(out) {
				panic(fmt.Errorf("Delta bounds"))
			}
			copy(out[written:], d[p:p+int(op)])
			p += int(op)
			written += int(op)
		}
	}
	if written != len(out) {
		panic(fmt.Errorf("Delta target mismatch"))
	}
	return out
}

type packEntry struct {
	start      int
	kind       int
	length     int
	data       []byte
	baseOffset int
	baseID     string
	object     *GitObject
	busy       bool
}

func unpack(b []byte, store map[string]*GitObject) {
	if len(b) == 0 {
		return
	}
	if len(b) < 32 || string(b[0:4]) != "PACK" {
		panic(fmt.Errorf("Invalid pack"))
	}
	ver := binary.BigEndian.Uint32(b[4:8])
	if ver != 2 && ver != 3 {
		panic(fmt.Errorf("Invalid pack"))
	}
	if !bytes.Equal(hashBytes(b[:len(b)-20]), b[len(b)-20:]) {
		panic(fmt.Errorf("Pack checksum mismatch"))
	}
	p := 12
	count := binary.BigEndian.Uint32(b[8:12])
	var entries []*packEntry
	offsets := map[int]*packEntry{}
	for n := uint32(0); n < count; n++ {
		start := p
		x := b[p]
		p++
		kind := int((x >> 4) & 7)
		ln := int(x & 15)
		s := 4
		for x&128 != 0 {
			x = b[p]
			p++
			ln += int(x&127) << s
			s += 7
		}
		e := &packEntry{start: start, kind: kind, length: ln}
		if kind == 6 {
			x = b[p]
			p++
			dist := int(x & 127)
			for x&128 != 0 {
				x = b[p]
				p++
				dist = (dist+1)*128 + int(x&127)
			}
			e.baseOffset = start - dist
		} else if kind == 7 {
			e.baseID = hex.EncodeToString(b[p : p+20])
			p += 20
		} else if kind < 1 || kind > 4 {
			panic(fmt.Errorf("Unknown pack type"))
		}
		br := bytes.NewReader(b[p : len(b)-20])
		zr, err := zlib.NewReader(br)
		if err != nil {
			panic(fmt.Errorf("Invalid pack"))
		}
		data, err := io.ReadAll(io.LimitReader(zr, 32*1024*1024+1))
		zr.Close()
		if err != nil || len(data) > 32*1024*1024 {
			panic(fmt.Errorf("Invalid pack"))
		}
		consumed := (len(b) - 20 - p) - br.Len()
		e.data = data
		p += consumed
		if len(e.data) != e.length {
			panic(fmt.Errorf("Pack object length mismatch"))
		}
		entries = append(entries, e)
		offsets[start] = e
	}
	if p != len(b)-20 {
		panic(fmt.Errorf("Trailing pack bytes"))
	}
	resolved := map[string]*GitObject{}
	var resolve func(e *packEntry) *GitObject
	resolve = func(e *packEntry) *GitObject {
		if e.object != nil {
			return e.object
		}
		if e.busy {
			return nil
		}
		var obj *GitObject
		if e.kind == 6 || e.kind == 7 {
			e.busy = true
			var base *GitObject
			if e.kind == 6 {
				base = resolve(offsets[e.baseOffset])
			} else {
				base = resolved[e.baseID]
				if base == nil {
					base = store[e.baseID]
				}
			}
			e.busy = false
			if base == nil {
				return nil
			}
			obj = &GitObject{Type: base.Type, Data: applyDelta(base.Data, e.data)}
		} else {
			types := []string{"", "commit", "tree", "blob", "tag"}
			obj = &GitObject{Type: types[e.kind], Data: e.data}
		}
		e.object = obj
		resolved[objectID(obj.Type, obj.Data)] = obj
		return obj
	}
	for pass := 0; pass <= len(entries); pass++ {
		left := 0
		for _, e := range entries {
			if resolve(e) == nil {
				left++
			}
		}
		if left == 0 {
			for id, o := range resolved {
				store[id] = o
			}
			return
		}
	}
	panic(fmt.Errorf("Unresolved pack delta"))
}

func packedObject(o *GitObject) []byte {
	n := len(o.Data)
	typeNum := map[string]int{"commit": 1, "tree": 2, "blob": 3, "tag": 4}[o.Type]
	x := typeNum*16 + (n & 15)
	n >>= 4
	var hdr []byte
	for n > 0 {
		hdr = append(hdr, byte(x|128))
		x = n & 127
		n >>= 7
	}
	hdr = append(hdr, byte(x))
	var buf bytes.Buffer
	zw, _ := zlib.NewWriterLevel(&buf, 1)
	zw.Write(o.Data)
	zw.Close()
	return append(hdr, buf.Bytes()...)
}

func packObjects(objects []*GitObject) []byte {
	h := make([]byte, 12)
	copy(h, "PACK")
	binary.BigEndian.PutUint32(h[4:], 2)
	binary.BigEndian.PutUint32(h[8:], uint32(len(objects)))
	parts := [][]byte{h}
	for _, o := range objects {
		parts = append(parts, packedObject(o))
	}
	b := bytes.Join(parts, nil)
	return append(b, hashBytes(b)...)
}

type FileEntry struct {
	OID  string
	Mode string
}

type GitStore struct {
	Objects       map[string]*GitObject
	Refs          map[string]string
	DefaultBranch string
	V             int
	fetchCache    map[string][]byte
	cacheV        int
	commitCache   map[string]*CommitInfo
	treeCache     map[string][]TreeEntry
	packedCache   map[string][]byte
}

func NewGitStore() *GitStore {
	return &GitStore{Objects: map[string]*GitObject{}, Refs: map[string]string{}, DefaultBranch: "main",
		fetchCache: map[string][]byte{}, commitCache: map[string]*CommitInfo{}, treeCache: map[string][]TreeEntry{}, packedCache: map[string][]byte{}}
}

func (s *GitStore) Put(kind string, data []byte) string {
	id := objectID(kind, data)
	s.Objects[id] = &GitObject{Type: kind, Data: data}
	s.V++
	return id
}

func (s *GitStore) SetRef(name, id string) { s.Refs[name] = id; s.V++ }
func (s *GitStore) DeleteRef(name string)  { delete(s.Refs, name); s.V++ }

func (s *GitStore) CommitView(id string) *CommitInfo {
	o := s.Objects[id]
	if o == nil || o.Type != "commit" {
		return nil
	}
	if c, ok := s.commitCache[id]; ok {
		return c
	}
	c := parseCommit(o)
	s.commitCache[id] = c
	return c
}

func (s *GitStore) cachedTree(o *GitObject) []TreeEntry {
	id := objectID(o.Type, o.Data)
	if t, ok := s.treeCache[id]; ok {
		return t
	}
	t := parseTree(o.Data)
	s.treeCache[id] = t
	return t
}

func (s *GitStore) Ancestors(tip string) map[string]bool {
	seen := map[string]bool{}
	if tip == "" {
		return seen
	}
	todo := []string{tip}
	for len(todo) > 0 {
		id := todo[len(todo)-1]
		todo = todo[:len(todo)-1]
		if seen[id] {
			continue
		}
		seen[id] = true
		if c := s.CommitView(id); c != nil {
			todo = append(todo, c.Parents...)
		}
	}
	return seen
}

func (s *GitStore) AncestorsOrder(tip string) []string {
	var order []string
	seen := map[string]bool{}
	if tip == "" {
		return order
	}
	todo := []string{tip}
	for len(todo) > 0 {
		id := todo[len(todo)-1]
		todo = todo[:len(todo)-1]
		if seen[id] {
			continue
		}
		seen[id] = true
		order = append(order, id)
		if c := s.CommitView(id); c != nil {
			todo = append(todo, c.Parents...)
		}
	}
	return order
}

func (s *GitStore) IsAncestor(old, tip string) bool {
	if old == tip {
		return true
	}
	seen := map[string]bool{}
	todo := []string{tip}
	for len(todo) > 0 {
		id := todo[len(todo)-1]
		todo = todo[:len(todo)-1]
		if id == old {
			return true
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		if o := s.Objects[id]; o != nil && o.Type == "commit" {
			todo = append(todo, s.CommitView(id).Parents...)
		}
	}
	return false
}

func (s *GitStore) Files(tip string) map[string]FileEntry {
	out := map[string]FileEntry{}
	root := ""
	if tip != "" {
		if c := s.CommitView(tip); c != nil {
			root = c.Tree
		}
	}
	var walk func(id, path string)
	walk = func(id, path string) {
		o := s.Objects[id]
		if o == nil {
			return
		}
		for _, e := range s.cachedTree(o) {
			if e.Mode == "40000" || e.Mode == "040000" {
				walk(e.OID, path+e.Name+"/")
			} else {
				out[path+e.Name] = FileEntry{OID: e.OID, Mode: e.Mode}
			}
		}
	}
	if root != "" {
		walk(root, "")
	}
	return out
}

func (s *GitStore) WriteTree(files map[string]FileEntry) string {
	root := map[string]any{}
	for path, v := range files {
		parts := strings.Split(path, "/")
		n := root
		for _, part := range parts[:len(parts)-1] {
			if _, ok := n[part]; !ok {
				n[part] = map[string]any{}
			}
			n = n[part].(map[string]any)
		}
		n[parts[len(parts)-1]] = v
	}
	var build func(n map[string]any) string
	build = func(n map[string]any) string {
		type ent struct {
			name string
			mode string
			oid  string
		}
		var entries []ent
		for name, v := range n {
			if sub, ok := v.(map[string]any); ok {
				entries = append(entries, ent{name, "40000", build(sub)})
			} else {
				fe := v.(FileEntry)
				entries = append(entries, ent{name, fe.Mode, fe.OID})
			}
		}
		sort.Slice(entries, func(i, j int) bool {
			a := entries[i].name
			if entries[i].mode == "40000" {
				a += "/"
			}
			b := entries[j].name
			if entries[j].mode == "40000" {
				b += "/"
			}
			return a < b
		})
		var buf bytes.Buffer
		for _, e := range entries {
			buf.WriteString(e.mode + " " + e.name + "\x00")
			oid, _ := hex.DecodeString(e.oid)
			buf.Write(oid)
		}
		return s.Put("tree", buf.Bytes())
	}
	return build(root)
}

func (s *GitStore) Commit(files map[string]FileEntry, parents []string, message string) string {
	var sb strings.Builder
	sb.WriteString("tree " + s.WriteTree(files) + "\n")
	for _, p := range parents {
		sb.WriteString("parent " + p + "\n")
	}
	sb.WriteString("author Forge <forge@local> 1735689600 +0000\ncommitter Forge <forge@local> 1735689600 +0000\n\n" + message)
	return s.Put("commit", []byte(sb.String()))
}

type ClosureResult struct {
	Objects    []*GitObject
	Seen       map[string]bool
	Boundaries map[string]bool
}

func (s *GitStore) Closure(roots []string, excluded map[string]bool, depth int) *ClosureResult {
	seen := map[string]bool{}
	boundaries := map[string]bool{}
	var objects []*GitObject
	var visit func(id string, d int)
	visit = func(id string, d int) {
		if seen[id] || excluded[id] {
			return
		}
		o := s.Objects[id]
		if o == nil {
			return
		}
		seen[id] = true
		objects = append(objects, o)
		if o.Type == "commit" {
			c := s.CommitView(id)
			visit(c.Tree, -1)
			if d == 1 {
				if len(c.Parents) > 0 {
					boundaries[id] = true
				}
			} else {
				for _, p := range c.Parents {
					nd := d - 1
					if d == -1 {
						nd = -1
					}
					visit(p, nd)
				}
			}
		} else if o.Type == "tree" {
			for _, e := range s.cachedTree(o) {
				if e.Mode != "160000" {
					visit(e.OID, -1)
				}
			}
		} else if o.Type == "tag" {
			for _, line := range strings.Split(string(o.Data), "\n") {
				if strings.HasPrefix(line, "object ") {
					visit(strings.TrimSpace(line[7:]), d)
					break
				}
			}
		}
	}
	for _, id := range roots {
		visit(id, depth)
	}
	return &ClosureResult{Objects: objects, Seen: seen, Boundaries: boundaries}
}

func (s *GitStore) Peel(id string) string {
	seen := map[string]bool{}
	for {
		o := s.Objects[id]
		if o == nil || o.Type != "tag" || seen[id] {
			break
		}
		seen[id] = true
		next := id
		for _, line := range strings.Split(string(o.Data), "\n") {
			if strings.HasPrefix(line, "object ") {
				next = strings.TrimSpace(line[7:])
				break
			}
		}
		id = next
	}
	return id
}

func bands(data []byte) [][]byte {
	var out [][]byte
	for i := 0; i < len(data); i += 64000 {
		end := i + 64000
		if end > len(data) {
			end = len(data)
		}
		chunk := append([]byte{1}, data[i:end]...)
		out = append(out, packet(chunk))
	}
	return out
}

func (s *GitStore) cachedPack(objects []*GitObject) []byte {
	var ids []string
	for _, o := range objects {
		ids = append(ids, objectID(o.Type, o.Data))
	}
	key := strings.Join(ids, ",")
	if p, ok := s.packedCache[key]; ok {
		return p
	}
	var parts [][]byte
	h := make([]byte, 12)
	copy(h, "PACK")
	binary.BigEndian.PutUint32(h[4:], 2)
	binary.BigEndian.PutUint32(h[8:], uint32(len(objects)))
	parts = append(parts, h)
	for _, o := range objects {
		oid := objectID(o.Type, o.Data)
		p, ok := s.packedCache["obj:"+oid]
		if !ok {
			p = packedObject(o)
			s.packedCache["obj:"+oid] = p
		}
		parts = append(parts, p)
	}
	b := bytes.Join(parts, nil)
	full := append(b, hashBytes(b)...)
	s.packedCache[key] = full
	return full
}

func gitResponse(s *GitStore, service string, advertise, v2 bool, input []byte) []byte {
	if service != "git-upload-pack" {
		return gitResponseUncached(s, service, advertise, v2, input)
	}
	if s.cacheV != s.V {
		s.fetchCache = map[string][]byte{}
		s.cacheV = s.V
	}
	key := fmt.Sprintf("%t|%t|", advertise, v2) + string(input)
	if hit, ok := s.fetchCache[key]; ok {
		return hit
	}
	out := gitResponseUncached(s, service, advertise, v2, input)
	if len(s.fetchCache) >= 16 {
		s.fetchCache = map[string][]byte{}
	}
	s.fetchCache[key] = out
	return out
}

func gitResponseUncached(s *GitStore, service string, advertise, v2 bool, input []byte) []byte {
	if advertise {
		if service == "git-upload-pack" && v2 {
			var out [][]byte
			for _, l := range []string{"version 2\n", "agent=forge/2\n", "ls-refs=unborn\n", "fetch=shallow\n", "object-format=sha1\n"} {
				out = append(out, packetStr(l))
			}
			return bytes.Join(append(out, FLUSH), nil)
		}
		caps := "side-band-64k shallow ofs-delta symref=HEAD:refs/heads/" + s.DefaultBranch
		if service == "git-receive-pack" {
			caps = "report-status side-band-64k delete-refs ofs-delta"
		}
		type row struct{ ref, id string }
		var rows []row
		for ref, id := range s.Refs {
			rows = append(rows, row{ref, id})
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].ref < rows[j].ref })
		if service == "git-upload-pack" {
			if id, ok := s.Refs["refs/heads/"+s.DefaultBranch]; ok {
				rows = append([]row{{"HEAD", id}}, rows...)
			}
		}
		out := [][]byte{packetStr("# service=" + service + "\n"), FLUSH}
		if len(rows) == 0 {
			out = append(out, packetStr(ZERO+" capabilities^{}\x00"+caps+"\n"))
		} else {
			for i, rw := range rows {
				suffix := ""
				if i == 0 {
					suffix = "\x00" + caps
				}
				out = append(out, packetStr(rw.id+" "+rw.ref+suffix+"\n"))
				if o := s.Objects[rw.id]; o != nil && o.Type == "tag" {
					out = append(out, packetStr(s.Peel(rw.id)+" "+rw.ref+"^{}\n"))
				}
			}
		}
		return bytes.Join(append(out, FLUSH), nil)
	}
	linesB, rest := packets(input)
	var lines []string
	for _, b := range linesB {
		lines = append(lines, strings.TrimRight(string(b), "\n"))
	}
	if service == "git-receive-pack" {
		errStr := "ok"
		func() {
			defer func() {
				if e := recover(); e != nil {
					errStr = fmt.Sprint(e)
				}
			}()
			unpack(rest, s.Objects)
			s.V++
		}()
		report := [][]byte{packetStr("unpack " + errStr + "\n")}
		for _, line := range lines {
			cmd := strings.SplitN(line, "\x00", 2)[0]
			fields := strings.Fields(strings.TrimSpace(cmd))
			if len(fields) < 3 {
				continue
			}
			old, id, ref := fields[0], fields[1], fields[2]
			reason := ""
			if errStr != "ok" {
				reason = "unpacker error"
			} else {
				cur := s.Refs[ref]
				if cur == "" {
					cur = ZERO
				}
				if cur != old {
					reason = "stale info"
				} else if id != ZERO && s.Objects[id] == nil {
					reason = "missing object"
				} else if old != ZERO && id != ZERO && !s.IsAncestor(old, id) {
					reason = "non-fast-forward"
				}
			}
			if reason != "" {
				report = append(report, packetStr("ng "+ref+" "+reason+"\n"))
			} else {
				if id == ZERO {
					s.DeleteRef(ref)
				} else {
					s.SetRef(ref, id)
				}
				report = append(report, packetStr("ok "+ref+"\n"))
			}
		}
		report = append(report, FLUSH)
		b := bytes.Join(report, nil)
		if len(lines) > 0 && strings.Contains(lines[0], "side-band-64k") {
			return bytes.Join(append(bands(b), FLUSH), nil)
		}
		return b
	}
	if len(lines) > 0 && strings.HasPrefix(lines[0], "command=ls-refs") {
		var prefixes []string
		for _, x := range lines {
			if strings.HasPrefix(x, "ref-prefix ") {
				prefixes = append(prefixes, x[11:])
			}
		}
		keep := func(n string) bool {
			if len(prefixes) == 0 {
				return true
			}
			for _, p := range prefixes {
				if strings.HasPrefix(n, p) {
					return true
				}
			}
			return false
		}
		var out [][]byte
		head := "refs/heads/" + s.DefaultBranch
		if keep("HEAD") {
			if id, ok := s.Refs[head]; ok {
				out = append(out, packetStr(id+" HEAD symref-target:"+head+"\n"))
			} else if contains(lines, "unborn") {
				out = append(out, packetStr("unborn HEAD symref-target:"+head+"\n"))
			}
		}
		type row struct{ ref, id string }
		var rows []row
		for ref, id := range s.Refs {
			rows = append(rows, row{ref, id})
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].ref < rows[j].ref })
		for _, rw := range rows {
			if keep(rw.ref) {
				peeled := ""
				if contains(lines, "peel") {
					if o := s.Objects[rw.id]; o != nil && o.Type == "tag" {
						peeled = " peeled:" + s.Peel(rw.id)
					}
				}
				out = append(out, packetStr(rw.id+" "+rw.ref+peeled+"\n"))
			}
		}
		return bytes.Join(append(out, FLUSH), nil)
	}
	var wants, haves, shallow []string
	depth := -1
	for _, x := range lines {
		if strings.HasPrefix(x, "want ") {
			wants = append(wants, strings.Fields(x)[1])
		} else if strings.HasPrefix(x, "have ") {
			haves = append(haves, strings.Fields(x)[1])
		} else if strings.HasPrefix(x, "deepen ") {
			depth, _ = strconv.Atoi(strings.TrimSpace(x[7:]))
		} else if strings.HasPrefix(x, "shallow ") {
			shallow = append(shallow, x[8:])
		}
	}
	excluded := map[string]bool{}
	if depth < 0 {
		excluded = s.Closure(haves, map[string]bool{}, -1).Seen
	}
	result := s.Closure(wants, excluded, depth)
	var out [][]byte
	if v2 {
		if len(result.Boundaries) > 0 || len(shallow) > 0 {
			out = append(out, packetStr("shallow-info\n"))
			for id := range result.Boundaries {
				out = append(out, packetStr("shallow "+id+"\n"))
			}
			for _, id := range shallow {
				if !result.Boundaries[id] {
					out = append(out, packetStr("unshallow "+id+"\n"))
				}
			}
			out = append(out, DELIM)
		}
		out = append(out, packetStr("packfile\n"))
	} else {
		for id := range result.Boundaries {
			out = append(out, packetStr("shallow "+id+"\n"))
		}
		out = append(out, packetStr("NAK\n"))
	}
	out = append(out, bands(s.cachedPack(result.Objects))...)
	out = append(out, FLUSH)
	return bytes.Join(out, nil)
}

func contains(arr []string, s string) bool {
	for _, x := range arr {
		if x == s {
			return true
		}
	}
	return false
}
