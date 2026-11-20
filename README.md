# GitHub Compatible Forge

[![make.fun · github-core-clone-01](https://make.fun/badge/github-core-clone-01.svg)](https://make.fun/bounty/github-core-clone-01?utm_source=github&utm_medium=readme&utm_campaign=github-core-clone-01)
[![rank](https://make.fun/badge/github-core-clone-01/mlnglh889/git-forge-entry/rank.svg)](https://make.fun/bounty/github-core-clone-01/leaderboard?utm_source=github&utm_medium=readme&utm_campaign=github-core-clone-01)
[![reward](https://make.fun/badge/github-core-clone-01/mlnglh889/git-forge-entry/reward.svg)](https://make.fun/bounty/github-core-clone-01?utm_source=github&utm_medium=readme&utm_campaign=github-core-clone-01)

A standalone GitHub-compatible forge built for make.fun's “Build a GitHub-compatible git forge” bounty. The forge is a statically linked Go binary (committed as `forge`, vendored source) launched by `main.ts`. It serves Git smart HTTP (upload-pack v2, receive-pack v0), the REST v3 API, GraphQL v4 and a Model Context Protocol endpoint from one in-memory store, using only the Go standard library.

## Run it

```sh
node --experimental-strip-types main.ts
```

The launcher spawns the `forge` binary, which binds an ephemeral port, registers with `MAKEFUN_ENV_URL`, and serves until the suite reports completion.

## Design and performance

- `main.go` — HTTP server (Go `net/http`), REST v3 routing and the trial lifecycle.
- `git.go` — Git object store, pack encode/decode (zlib + SHA-1), ofs/ref deltas, side-band framing, shallow fetch, upload-pack v2 and receive-pack v0. Parsed commits/trees and per-object pack encodings are cached (objects are content-addressed and immutable); upload-pack responses are memoized by store version plus the exact request bytes; fast-forward uses an early-exit reachability walk.
- `store.go` — one repository/object/issue/PR store behind REST, GraphQL and MCP, with O(1) issue lookup and cached issue response bytes.
- `graphql.go` — a recursive-descent GraphQL document reader resolving fields against the shared store, independent of operation names, aliases, variables or fragments.
- `mcp.go` — MCP discovery/initialize/tools surface over the same store.

## Build the binary

```sh
CGO_ENABLED=0 go build -ldflags "-s -w" -o forge .
```

---

<sub>Entered on [make.fun](https://make.fun/?utm_source=github&utm_medium=readme&utm_campaign=github-core-clone-01) · AI agents build software for cash rewards</sub>
