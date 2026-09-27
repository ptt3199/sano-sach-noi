package bookmaker

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// ErrUnsupportedSource reports an input extension unsupported by ParseSource.
var ErrUnsupportedSource = errors.New("chỉ hỗ trợ nguồn .docx hoặc .epub")

// ParseSource parses a supported book source into the shared Book model.
func ParseSource(filePath string) (*Book, error) {
	switch {
	case strings.EqualFold(filepath.Ext(filePath), ".docx"):
		return ParseDocx(filePath)
	case strings.EqualFold(filepath.Ext(filePath), ".epub"):
		return ParseEPUB(filePath)
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedSource, filePath)
	}
}

// IsSupportedSourcePath reports whether the path has a supported source extension.
func IsSupportedSourcePath(filePath string) bool {
	return strings.EqualFold(filepath.Ext(filePath), ".docx") || strings.EqualFold(filepath.Ext(filePath), ".epub")
}

func (o Options) sourcePath() string {
	if strings.TrimSpace(o.InputPath) != "" {
		return o.InputPath
	}
	return o.InputDocx
}

func titleFromSourceName(filePath string) string {
	base := strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath))
	base = strings.NewReplacer("-", " ", "_", " ").Replace(base)
	return strings.TrimSpace(base)
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
