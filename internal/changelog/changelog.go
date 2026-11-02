// Package changelog lê o CHANGELOG.md (formato Keep a Changelog) para mostrar as
// novidades de cada versão na interface.
package changelog

import (
	"regexp"
	"strings"
)

// Release é uma versão do changelog.
type Release struct {
	Version  string    `json:"version"` // "0.2.0"
	Date     string    `json:"date"`    // como está no arquivo, ex. "2026-11-02"
	Sections []Section `json:"sections"`
}

// Section é um grupo de mudanças ("Adicionado", "Corrigido", ...).
type Section struct {
	Title string   `json:"title"`
	Items []string `json:"items"`
}

// "## [0.2.0] — 2026-11-02"; aceita travessão, hífen ou nada antes da data.
var headRe = regexp.MustCompile(`^##\s+\[([^\]]+)\]\s*(?:[—–-]\s*(.*))?$`)

// linkRe tira links do markdown: "[texto](url)" vira "texto".
var linkRe = regexp.MustCompile(`\[([^\]]+)\]\([^)]+\)`)

// Parse lê as versões lançadas, da mais nova para a mais antiga. A seção
// "Não lançado" fica de fora.
func Parse(md string) []Release {
	var out []Release
	var cur *Release
	var sec *Section
	for _, line := range strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n") {
		line = strings.TrimRight(line, " \t")
		if m := headRe.FindStringSubmatch(line); m != nil {
			cur, sec = nil, nil
			if !strings.EqualFold(m[1], "não lançado") && !strings.EqualFold(m[1], "unreleased") {
				out = append(out, Release{Version: m[1], Date: strings.TrimSpace(m[2])})
				cur = &out[len(out)-1]
			}
			continue
		}
		if cur == nil {
			continue
		}
		switch {
		case strings.HasPrefix(line, "### "):
			cur.Sections = append(cur.Sections, Section{Title: strings.TrimSpace(line[4:])})
			sec = &cur.Sections[len(cur.Sections)-1]
		case strings.HasPrefix(line, "- "):
			if sec == nil { // itens sem subtítulo
				cur.Sections = append(cur.Sections, Section{})
				sec = &cur.Sections[len(cur.Sections)-1]
			}
			sec.Items = append(sec.Items, clean(line[2:]))
		case strings.HasPrefix(line, "  ") && sec != nil && len(sec.Items) > 0:
			// continuação do item anterior (linha quebrada)
			sec.Items[len(sec.Items)-1] += " " + clean(strings.TrimSpace(line))
		}
	}
	return out
}

func clean(s string) string {
	s = linkRe.ReplaceAllString(s, "$1")
	return strings.NewReplacer("**", "", "`", "").Replace(s)
}
