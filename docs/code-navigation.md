# Code navigation

`code_map` and `code_find` are available in terminal/prompt sessions and workflow agents, including read-only and managed-worktree agents. They do not grant shell access.

Example tool arguments:

```json
{"path":"internal","depth":2,"limit":40}
```

Use `code_map` with a directory for files/subdirectory counts, or a file such as `internal/auth.go` for declarations and signatures. A `kind` filter can request declarations across files.

```json
{"query":"validate session","language":"go","limit":20}
```

`code_find` ranks exact names, identifier matches, paths, signatures, then matching source lines. All query words must match the same candidate; camelCase and snake_case are split. This initial implementation uses bounded in-process lexical ranking, not embeddings or SQLite FTS. It searches comments and implementation text even when parsing fails. Results include SHA-256 and source ranges: use `read_file` for current exact text before editing. `known_sha256` on `read_file` is an omission hint, not an optimistic-lock precondition.

Every call scans/hashes eligible files and reuses unchanged declarations from a separate SQLite cache under `os.UserCacheDir()/ronin/codeindex`. Caches are keyed by canonical workspace path and extraction version, not Git remote/branch; worktrees remain isolated. No cache files are written inside the workspace. Indexing runs lazily, without a watcher or background daemon. Cache errors fail only the navigation call; an error identifies a corrupt database that can be removed to rebuild. Cache files contain source text: protect them like the workspace, and delete the cache directory to remove retained data. There is no automatic cache eviction yet.

Scope is `.go`, `.ts`, `.mts`, `.cts`, and `.tsx`. Nested workspace `.gitignore` rules apply to all files (including tracked files); global Git excludes and `.git/info/exclude` are not read. Matching is implemented privately in `codeindex` with no Git library or executable required at runtime. It is case-sensitive on all platforms, supports Git-style wildcards, negation, bracket classes, and escaped characters, and prunes excluded parents before reading child rules. Differential tests use Git when available. Symlinks and common dependency/build directories are skipped. Unlike Git, generated files are not detected specially. Limits: 1 MiB/file, 64 MiB total source, 128 MiB source/structure budget, 10,000 files, 30 seconds per tool call, 100 results and 64 KiB encoded result output. Narrow the working directory if a workspace limit is exceeded. A path filter narrows results, not the refresh scope. Truncation and capped diagnostic summaries are explicit.

Structural coverage includes functions, methods, named types/classes/interfaces, Go constants/variables, TypeScript identifier bindings, and enums. It is not semantic navigation: no references, type resolution, or call graph. Docs remain searchable source text rather than attached symbol metadata. Imports/re-exports, destructuring, anonymous exports, and some type/member forms are not yet modeled. Parser diagnostics indicate potentially incomplete structure, not compiler errors; files can change after refresh, so returned hashes describe the scanned contents, not a transactionally frozen filesystem.

## Experimental parser dependencies and releases

The root `go.mod` pins unmerged fixes from [binding PR #56](https://github.com/tree-sitter/go-tree-sitter/pull/56) and [TypeScript grammar PR #365](https://github.com/tree-sitter/tree-sitter-typescript/pull/365). Parser callback ownership has regression tests. Query callbacks remain disabled because of a separate upstream lifetime bug; a conservative native timeout is retained. `f<typeof import('module')>()` remains a known grammar gap with regression coverage. Revisit these replacements when fixes merge. The implementation and regression tests live in `codeindex/` and `codeindex/syntax/`.

The release workflow now uses native CGo jobs for all six existing OS/architecture targets, with static Linux builds and Windows toolchains. `workflow_dispatch` builds/test-packages without publishing. Only Linux/arm64 has been validated locally; all other targets, especially Windows/arm64, must pass the matrix before release. No separate Tree-sitter shared library is required.

[Back to README](../README.md)
