package update

import "testing"

func TestParseTrailer(t *testing.T) {
	tail := append(make([]byte, 20), []byte("\nFAMILIASHARE-BUILD:20261029-141516\n")...)
	id, ok := parseTrailer(tail)
	if !ok || id != "20261029-141516" {
		t.Fatalf("parseTrailer = %q, %v", id, ok)
	}
	if _, ok := parseTrailer(make([]byte, 64)); ok {
		t.Error("64 zeros (versão antiga, sem etiqueta) não deveriam ter etiqueta")
	}
	if _, ok := parseTrailer([]byte("<html>Google Drive - Virus scan warning</html>")); ok {
		t.Error("a página de aviso do Drive não pode ser lida como versão")
	}
}

func TestNewer(t *testing.T) {
	if !newer("20261029-141516", "20261029-141510") {
		t.Error("6 s depois deveria ser mais novo")
	}
	if newer("20261029-141510", "20261029-141510") {
		t.Error("mesma versão não é mais nova")
	}
	if newer("20261028-235959", "20261029-000000") {
		t.Error("dia anterior não é mais novo")
	}
}
