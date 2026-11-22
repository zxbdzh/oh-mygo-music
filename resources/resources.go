package resources

import _ "embed"

// Icon 与安装包共用同一份原图，避免界面维护另一套近似标识。
//
//go:embed icon.png
var Icon []byte
