// Package familiashare guarda os arquivos da raiz do repositório que vão embutidos no app.
package familiashare

import _ "embed"

// Changelog é o CHANGELOG.md, mostrado em Configurações → Novidades.
//
//go:embed CHANGELOG.md
var Changelog string
