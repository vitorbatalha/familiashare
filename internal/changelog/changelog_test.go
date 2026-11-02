package changelog

import (
	"testing"

	"github.com/vitorbatalha/familiashare"
)

const sample = `# Changelog

Texto de introdução.

## [Não lançado]

### Adicionado
- Ainda não saiu.

## [0.2.0] — 2026-11-02

### Adicionado
- Tela de **configurações**, com as
  novidades de cada versão.
- Veja o [README](README.md).

### Corrigido
- ` + "`bug`" + ` corrigido.

## [0.1.2] - 2026-10

- Item sem subtítulo.

[Não lançado]: https://github.com/x/y/compare/v0.2.0...HEAD
`

func TestParse(t *testing.T) {
	rs := Parse(sample)
	if len(rs) != 2 {
		t.Fatalf("esperava 2 versões (sem a não lançada), veio %d: %+v", len(rs), rs)
	}
	r := rs[0]
	if r.Version != "0.2.0" || r.Date != "2026-11-02" || len(r.Sections) != 2 {
		t.Fatalf("0.2.0 errada: %+v", r)
	}
	add := r.Sections[0]
	if add.Title != "Adicionado" || len(add.Items) != 2 ||
		add.Items[0] != "Tela de configurações, com as novidades de cada versão." || add.Items[1] != "Veja o README." {
		t.Errorf("Adicionado errado: %#v", add)
	}
	if r.Sections[1].Items[0] != "bug corrigido." {
		t.Errorf("Corrigido errado: %#v", r.Sections[1])
	}
	old := rs[1]
	if old.Version != "0.1.2" || old.Date != "2026-10" || len(old.Sections) != 1 || old.Sections[0].Title != "" {
		t.Errorf("0.1.2 errada: %+v", old)
	}
}

// O CHANGELOG.md de verdade tem que continuar legível pelo app.
func TestRealChangelog(t *testing.T) {
	rs := Parse(familiashare.Changelog)
	if len(rs) == 0 {
		t.Fatal("nenhuma versão lida do CHANGELOG.md")
	}
	for _, r := range rs {
		if len(r.Sections) == 0 {
			t.Errorf("versão %s sem itens", r.Version)
		}
	}
}
