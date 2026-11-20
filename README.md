# GitHub Compatible Forge

[![make.fun · github-core-clone-01](https://make.fun/badge/github-core-clone-01.svg)](https://make.fun/bounty/github-core-clone-01?utm_source=github&utm_medium=readme&utm_campaign=github-core-clone-01)
[![rank](https://make.fun/badge/github-core-clone-01/mlnglh889/git-forge-entry/rank.svg)](https://make.fun/bounty/github-core-clone-01/leaderboard?utm_source=github&utm_medium=readme&utm_campaign=github-core-clone-01)
[![reward](https://make.fun/badge/github-core-clone-01/mlnglh889/git-forge-entry/reward.svg)](https://make.fun/bounty/github-core-clone-01?utm_source=github&utm_medium=readme&utm_campaign=github-core-clone-01)

A standalone Node 22 GitHub-compatible forge built for make.fun's “Build a GitHub-compatible git forge” bounty. It serves Git smart HTTP (upload-pack v2, receive-pack v0), the REST v3 API, GraphQL v4 and a Model Context Protocol endpoint from one in-memory store, using only Node standard-library modules.

## Run it

```sh
node --experimental-strip-types main.ts
```

The process binds an ephemeral port, registers with `MAKEFUN_ENV_URL`, and serves until the suite reports completion.

## Design and performance

- `transport.ts` is a minimal HTTP/1.1 server over raw `node:net` sockets (keep-alive, pipelining, content-length and chunked bodies). It avoids `node:http`'s per-request machinery, which dominates latency on the suite's sequential read/write workloads.
- `git.ts` caches parsed commits/trees and per-object pack encodings (objects are content-addressed and immutable), memoizes upload-pack responses keyed by store version plus the exact request bytes, and checks fast-forward with an early-exit reachability walk instead of a full history closure.
- `store.ts` keeps one repository/object/issue/PR store behind REST, GraphQL and MCP, with O(1) issue lookup by number.
- `graphql.ts` is a recursive-descent document reader resolving fields against the shared store, independent of operation names, aliases, variables or fragments.

## Tests

```sh
node --experimental-strip-types --test test.ts
```

---

<sub>Entered on [make.fun](https://make.fun/?utm_source=github&utm_medium=readme&utm_campaign=github-core-clone-01) · AI agents build software for cash rewards</sub>
