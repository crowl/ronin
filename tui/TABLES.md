# Markdown tables in the TUI

Pipe tables render with Unicode borders, bold headers, a header separator, and
left/center/right alignment from the delimiter row. Cells use the existing inline
Markdown styling (bold, italic, code, and links). Widths are measured after styling,
using the TUI's terminal-cell sizing rules. Long content wraps within columns;
styles are reset at each cell boundary and restored on continuation lines.

A header and delimiter row must have the same number of cells. Delimiters require
at least three hyphens, optionally surrounded by alignment colons. Outer pipes are
optional. Escape literal pipes as `\|`, including in inline code. Missing body cells
are empty; extra cells cause the candidate table to fall back to ordinary Markdown
rather than silently dropping content. Body rows must contain an unescaped pipe.
Blank lines, fences, or non-pipe lines end a table. Tables inside fences are ignored.
This is a focused pipe-table implementation, not a full GFM parser; nested tables in
lists/quotes and HTML cell content are not specially interpreted.

Columns have a minimum content width of three cells. When borders, padding, and
minimum column widths cannot fit, each row becomes stacked `Header: value` fields,
with a blank line between records. Empty headers use `Column N`. Header-only tables
remain visible. At the exceptional viewport width of one cell, wide characters
are replaced by `?` to avoid overflowing. Other control characters in cells are
replaced before renderer-owned styling is applied.

Streaming switches to table rendering once the delimiter row is valid. Available
body rows render immediately; column widths may change as new content arrives.
Resizing switches between tabular and stacked presentation without modifying the
source message.

Processing limits: 32 columns, 256 rows including the header, and 64 KiB of table
source (excluding newline separators). Candidates exceeding these limits fall back
to ordinary Markdown. No dependencies were added.

Verification:

```sh
go test ./tui
go vet ./tui
go test ./tui -run '^$' -fuzz '^FuzzMarkdownTable$' -fuzztime=10s
```
