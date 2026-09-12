# Mermaid flowcharts

`mermaid.Render(source, maxWidth)` returns plain Unicode text or an error. The
package imports only the Go standard library, runs no external commands, and
performs no I/O. Ronin renders completed `mermaid` Markdown fences automatically;
incomplete, unsupported, unroutable, or too-wide diagrams remain source blocks.
Resizing can switch between a diagram and its source. Rendered rows are not wrapped.

## Supported subset

- `graph` or `flowchart`, followed by `TD`, `TB`, or `LR`.
- Newlines and semicolons separate statements; `%%` starts a comment.
- ASCII identifiers: a letter or underscore, then letters, digits, or underscores.
- Bare nodes, rectangles `A[Label]`, rounded boxes `A(Label)`, and compact
  diamond-like decisions `A{Question?}`. Labels may be double-quoted.
- Directed edges `A --> B`, labels `A -->|yes| B`, and chains `A --> B --> C`.
- Forward references, repeated node declarations (last explicit label wins),
  disconnected nodes, branches, joins, cycles, and self-loops.

Subgraphs, other directions, styling, alternate edge types, `&` fan-out,
HTML/entities, multiline labels, escapes, and other Mermaid diagram types are
not supported. Unsupported syntax produces an error rather than partial output.

## Layout and limits

Layout uses deterministic ranks and fixed-size node boxes. Back edges are omitted
from rank constraints, not from the rendered graph. Connectors use obstacle-avoiding
orthogonal routing without crossings or shared segments. Each node currently has
one port per side; high-degree and crowded graphs may be unroutable. Routing is
greedy, not a global optimizer, and may fail even when a different layout would fit.
Edge labels occupy clear space beside their connectors.

Limits: 32 KiB source, 32 nodes, 64 edges, 80 cells per label, 512 cells per canvas
dimension, 65,536 canvas cells, and two million routing search steps per render.
The final trimmed diagram must fit `maxWidth`.
There is no automatic direction change, clipping, or wrapping.

Label sizing supports ordinary single-cell characters, combining marks, and common
CJK wide characters. Terminal controls, formatting characters, variation selectors,
and supplementary characters at or above U+1F000 are rejected. Full grapheme-cluster
and emoji presentation support is intentionally absent; ambiguous-width characters
are assumed to occupy one cell. Terminal font and width conventions can still differ.

## Verification

```sh
go test ./mermaid ./tui
go vet ./mermaid ./tui
go test ./mermaid -fuzz FuzzRender -fuzztime=10s
```

Inspired by the terminal diagrams in AlexanderGrooff/mermaid-ascii. This is a local
implementation, not a dependency on or port of that project's source.
