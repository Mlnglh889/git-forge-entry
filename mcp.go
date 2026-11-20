package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
)

type mcpTool struct {
	Name        string
	Description string
	Required    []string
	Properties  map[string]any
}

var mcpTools = []mcpTool{
	{"create_repo", "Create a repository owned by the authenticated viewer.", []string{"name"}, map[string]any{"name": map[string]any{"type": "string"}, "description": map[string]any{"type": "string"}, "private": map[string]any{"type": "boolean"}}},
	{"get_repo", "Read a repository and its stable identity.", []string{"owner", "repo"}, map[string]any{"owner": map[string]any{"type": "string"}, "repo": map[string]any{"type": "string"}}},
	{"create_issue", "Create an open issue in a repository.", []string{"owner", "repo", "title"}, map[string]any{"owner": map[string]any{"type": "string"}, "repo": map[string]any{"type": "string"}, "title": map[string]any{"type": "string"}, "body": map[string]any{"type": "string"}}},
	{"list_issues", "List repository issues filtered by state.", []string{"owner", "repo"}, map[string]any{"owner": map[string]any{"type": "string"}, "repo": map[string]any{"type": "string"}, "state": map[string]any{"type": "string"}}},
	{"create_pull_request", "Open a pull request between existing branches.", []string{"owner", "repo", "title", "head", "base"}, map[string]any{"owner": map[string]any{"type": "string"}, "repo": map[string]any{"type": "string"}, "title": map[string]any{"type": "string"}, "body": map[string]any{"type": "string"}, "head": map[string]any{"type": "string"}, "base": map[string]any{"type": "string"}}},
	{"merge_pull_request", "Squash merge a non-conflicting pull request.", []string{"owner", "repo", "pull_number"}, map[string]any{"owner": map[string]any{"type": "string"}, "repo": map[string]any{"type": "string"}, "pull_number": map[string]any{"type": "integer"}}},
}

func mcpToolsList() []any {
	var out []any
	for _, t := range mcpTools {
		out = append(out, map[string]any{"name": t.Name, "description": t.Description,
			"inputSchema": map[string]any{"type": "object", "properties": t.Properties, "required": t.Required}})
	}
	return out
}

func mcpOK(id any, result any) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": id, "result": result}
}

func mcpErr(id any, code int, message string) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": message}}
}

func (a *App) mcpCall(x map[string]any) any {
	id := x["id"]
	method, _ := x["method"].(string)
	params, _ := x["params"].(map[string]any)
	if params == nil {
		params = map[string]any{}
	}
	switch method {
	case "server/discover":
		return mcpOK(id, map[string]any{"serverInfo": map[string]any{"name": "Local Forge", "version": "2.0.0"}, "capabilities": map[string]any{"tools": map[string]any{}}, "supportedVersions": []string{"2026-07-28", "2025-11-25"}})
	case "initialize":
		pv, _ := params["protocolVersion"].(string)
		if pv == "" {
			pv = "2025-11-25"
		}
		return mcpOK(id, map[string]any{"protocolVersion": pv, "serverInfo": map[string]any{"name": "Local Forge", "version": "2.0.0"}, "capabilities": map[string]any{"tools": map[string]any{}}})
	case "notifications/initialized":
		return nil
	case "ping":
		return mcpOK(id, map[string]any{})
	case "tools/list":
		return mcpOK(id, map[string]any{"tools": mcpToolsList()})
	case "tools/call":
	default:
		return mcpErr(id, -32601, "Method not found")
	}
	name, _ := params["name"].(string)
	var tool *mcpTool
	for i := range mcpTools {
		if mcpTools[i].Name == name {
			tool = &mcpTools[i]
			break
		}
	}
	if tool == nil {
		return mcpErr(id, -32601, "Unknown tool")
	}
	args, _ := params["arguments"].(map[string]any)
	if args == nil {
		args = map[string]any{}
	}
	for _, req := range tool.Required {
		v := args[req]
		if v == nil || v == "" {
			return mcpErr(id, -32602, "Missing required argument: "+req)
		}
	}
	var value any
	err := func() (e any) {
		defer func() { e = recover() }()
		f := a.forge
		switch name {
		case "create_repo":
			value = f.Create(args).View()
		case "get_repo":
			owner, _ := args["owner"].(string)
			repo, _ := args["repo"].(string)
			value = f.Get(owner, repo).View()
		case "create_issue":
			owner, _ := args["owner"].(string)
			repo, _ := args["repo"].(string)
			rp := f.Get(owner, repo)
			value = rp.IssueView(rp.Issue(args))
		case "list_issues":
			owner, _ := args["owner"].(string)
			repo, _ := args["repo"].(string)
			rp := f.Get(owner, repo)
			state, _ := args["state"].(string)
			items := []any{}
			for _, i := range rp.Issues {
				if state == "" || state == "all" || i.State == state {
					items = append(items, rp.IssueView(i))
				}
			}
			value = items
		case "create_pull_request":
			owner, _ := args["owner"].(string)
			repo, _ := args["repo"].(string)
			rp := f.Get(owner, repo)
			value = rp.PullView(rp.Pull(args))
		case "merge_pull_request":
			owner, _ := args["owner"].(string)
			repo, _ := args["repo"].(string)
			rp := f.Get(owner, repo)
			num := toInt(args["pull_number"])
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
			value = rp.Merge(p, args)
		}
		return nil
	}()
	if err != nil {
		msg := fmt.Sprint(err)
		if he, ok := err.(*httpError); ok {
			msg = he.msg
		}
		return mcpOK(id, map[string]any{"isError": true, "content": []any{map[string]any{"type": "text", "text": msg}}})
	}
	txt, _ := json.Marshal(value)
	return mcpOK(id, map[string]any{"content": []any{map[string]any{"type": "text", "text": string(txt)}}, "structuredContent": value})
}

func toInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case string:
		i, _ := strconv.Atoi(n)
		return i
	}
	return 0
}

func (a *App) mcp(w http.ResponseWriter, raw []byte) {
	var x any
	if err := json.Unmarshal(raw, &x); err != nil {
		writeJSON(w, 400, map[string]any{"jsonrpc": "2.0", "id": nil, "error": map[string]any{"code": -32700, "message": "Parse error"}}, nil)
		return
	}
	var value any
	switch v := x.(type) {
	case []any:
		arr := []any{}
		for _, item := range v {
			if m, ok := item.(map[string]any); ok {
				arr = append(arr, a.mcpCall(m))
			}
		}
		value = arr
	case map[string]any:
		value = a.mcpCall(v)
	default:
		value = a.mcpCall(map[string]any{})
	}
	if value == nil {
		writeJSON(w, 202, nil, nil)
		return
	}
	writeJSON(w, 200, value, nil)
}
