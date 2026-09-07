package embedded

import (
	"embed"
	"io/fs"
)

// FS contains only catalog metadata. Runtime and model binaries are downloaded on demand.
//
//go:embed manifest
var FS embed.FS

func Files() fs.FS { return FS }
