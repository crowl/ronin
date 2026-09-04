package tui

import "github.com/crowl/ronin/tool"

const maxToolDisplayBytes = 1 << 20
const maxToolDisplayArtifacts = 1024

func (b *toolCallBox) addDisplayArtifact(artifact tool.Artifact) {
	if b.DisplayTruncated {
		return
	}
	b.Revision++
	if len(b.Artifacts) >= maxToolDisplayArtifacts {
		b.DisplayTruncated = true
		return
	}
	bounded, used, truncated := boundWorkflowArtifact(artifact, maxToolDisplayBytes-b.DisplayBytes)
	if used > 0 {
		b.Artifacts = appendToolArtifact(b.Artifacts, bounded)
	}
	b.DisplayBytes += used
	b.DisplayTruncated = truncated
}
