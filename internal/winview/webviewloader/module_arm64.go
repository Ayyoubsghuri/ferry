//go:build windows

package webviewloader

import _ "embed"

//go:embed sdk/arm64/WebView2Loader.dll
var WebView2Loader []byte
