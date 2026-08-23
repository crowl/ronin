package gosemantic

import (
	"fmt"
	"strings"
)

const maxSummaryDocRunes = 240

func findSymbolSummary(result FindSymbolResult) string {
	var b strings.Builder
	previousPackage := ""
	for _, match := range result.Matches {
		if match.PkgPath != previousPackage {
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			fmt.Fprintf(&b, "Package: %s\n", match.PkgPath)
			previousPackage = match.PkgPath
		}
		writeSymbolSummary(&b, "", match)
	}
	writeWarningsSummary(&b, result.Warnings)
	return strings.TrimSpace(b.String())
}

func outlinePackageSummary(result OutlinePackageResult) string {
	var b strings.Builder
	for i, pkg := range result.Packages {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "Package: %s\n", pkg.Package)
		if len(pkg.Types) > 0 {
			b.WriteString("Types:\n")
			for _, typ := range pkg.Types {
				writeSymbolSummary(&b, "", typ.Symbol)
				if len(typ.Methods) > 0 {
					b.WriteString("  Methods:\n")
					for _, method := range typ.Methods {
						writeSymbolSummary(&b, "  ", method)
					}
				}
			}
		}
		writeSymbolGroup(&b, "Functions", pkg.Functions)
		writeSymbolGroup(&b, "Constants", pkg.Constants)
		writeSymbolGroup(&b, "Variables", pkg.Variables)
	}
	writeWarningsSummary(&b, result.Warnings)
	return strings.TrimSpace(b.String())
}

func writeSymbolGroup(b *strings.Builder, title string, symbols []Symbol) {
	if len(symbols) == 0 {
		return
	}
	fmt.Fprintf(b, "%s:\n", title)
	for _, symbol := range symbols {
		writeSymbolSummary(b, "", symbol)
	}
}

func writeSymbolSummary(b *strings.Builder, indent string, symbol Symbol) {
	fmt.Fprintf(b, "%s- %s: %s (%s)\n", indent, symbol.Kind, symbol.Signature, symbolLocation(symbol))
	if doc := summaryDoc(symbol.Doc); doc != "" {
		fmt.Fprintf(b, "%s  %s\n", indent, doc)
	}
}

func symbolLocation(symbol Symbol) string {
	if symbol.StartLine == symbol.EndLine {
		return fmt.Sprintf("%s:%d", symbol.File, symbol.StartLine)
	}
	return fmt.Sprintf("%s:%d-%d", symbol.File, symbol.StartLine, symbol.EndLine)
}

func summaryDoc(doc string) string {
	doc = strings.Join(strings.Fields(doc), " ")
	runes := []rune(doc)
	if len(runes) <= maxSummaryDocRunes {
		return doc
	}
	return string(runes[:maxSummaryDocRunes-3]) + "..."
}

func writeWarningsSummary(b *strings.Builder, warnings []string) {
	if len(warnings) == 0 {
		return
	}
	if b.Len() > 0 {
		b.WriteString("\n")
	}
	b.WriteString("Warnings:\n")
	for _, warning := range warnings {
		fmt.Fprintf(b, "- %s\n", strings.Join(strings.Fields(warning), " "))
	}
}
