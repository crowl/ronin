package tool

import "fmt"

type Error struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	Path       string `json:"path,omitempty"`
	PatchIndex *int   `json:"patch_index,omitempty"`
}

func (e Error) Error() string {
	if e.Path != "" {
		return fmt.Sprintf("%s: %s: %s", e.Code, e.Path, e.Message)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}
