// Package ui contém a interface (HTML/CSS/JS), embutida no executável.
// O app a serve num servidor HTTP local e a abre no WebView2.
package ui

import "embed"

//go:embed index.html
var Files embed.FS
