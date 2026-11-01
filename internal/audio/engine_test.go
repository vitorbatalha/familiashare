package audio

import (
	"sort"
	"strings"
	"testing"
)

// Uma "foto" de processos parecida com a real:
//
//	explorer(10) ─┬─ discord(20) ─ discord(21, tem som)
//	              ├─ chrome(30)  ─ chrome(31, tem som)
//	              └─ familiashare(40) ─ msedgewebview2(41, toca quem assistimos)
//	steam(50) ─ jogo(51, tem som)
func fakeProcs() map[uint32]procInfo {
	return map[uint32]procInfo{
		10: {"explorer.exe", 1},
		20: {"discord.exe", 10}, 21: {"discord.exe", 20},
		30: {"chrome.exe", 10}, 31: {"chrome.exe", 30},
		40: {"familiashare.exe", 10}, 41: {"msedgewebview2.exe", 40},
		50: {"steam.exe", 1}, 51: {"jogo.exe", 50},
	}
}

const self = 40

func pids(ts []target) []int {
	var out []int
	for _, t := range ts {
		out = append(out, int(t.pid))
	}
	sort.Ints(out)
	return out
}

func TestNoExclusionsCapturesEverythingButSelf(t *testing.T) {
	ts, mode := planTargets(nil, self, fakeProcs(), []uint32{21, 31, 41, 51})
	if len(ts) != 1 || ts[0] != (target{self, true}) {
		t.Fatalf("quero uma captura excluindo a árvore do app, veio %+v", ts)
	}
	if mode != "sistema inteiro" {
		t.Errorf("modo = %q", mode)
	}
}

func TestExcludedAppNotRunning(t *testing.T) {
	ts, mode := planTargets(normalizeExclusions([]string{"spotify"}), self, fakeProcs(), []uint32{21, 31})
	if len(ts) != 1 || ts[0] != (target{self, true}) || !strings.Contains(mode, "não estão abertos") {
		t.Fatalf("app excluído fechado: quero sistema inteiro, veio %+v (%s)", ts, mode)
	}
}

func TestExcludeDiscord(t *testing.T) {
	// Discord e o próprio app são duas árvores a excluir: o motor captura cada app
	// permitido que tem som. Nunca o Discord, nunca o app (senão vira eco), nunca o
	// explorer (a árvore dele contém o Discord).
	ts, _ := planTargets(normalizeExclusions([]string{"Discord"}), self, fakeProcs(), []uint32{21, 31, 41, 51})
	got := pids(ts)
	want := []int{31, 51}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("capturas = %v, quero %v (chrome e jogo)", got, want)
	}
	for _, x := range ts {
		if x.exclude {
			t.Errorf("no modo de vários apps todas as capturas são de inclusão: %+v", x)
		}
	}
}

func TestSessionCoveredByAncestorIsNotDuplicated(t *testing.T) {
	// steam(50) e jogo(51) têm som: incluir a árvore do steam já inclui o jogo
	ts, _ := planTargets(normalizeExclusions([]string{"discord"}), self, fakeProcs(), []uint32{50, 51})
	if got := pids(ts); len(got) != 1 || got[0] != 50 {
		t.Fatalf("capturas = %v, quero só [50] (o jogo vem junto)", got)
	}
}

func TestNormalizeExclusions(t *testing.T) {
	m := normalizeExclusions([]string{" Discord ", "spotify.exe", ""})
	if !m["discord.exe"] || !m["spotify.exe"] || len(m) != 2 {
		t.Errorf("normalização: %v", m)
	}
}
