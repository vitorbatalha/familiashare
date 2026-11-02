<p align="center">
  <img src="assets/icon.png" width="128" alt="">
</p>

<h1 align="center">familiashare</h1>

<p align="center">
  Compartilhamento de tela entre amigos, para Windows — leve, em alta qualidade e sem servidor próprio.
</p>

---

O familiashare é um app pequeno para transmitir a tela (jogos, vídeos, o que for) para
amigos, com qualidade de verdade: resolução nativa, 60 fps e bitrate alto, codificados pela
placa de vídeo. Funciona como um "canal de voz" de telas: cada pessoa abre uma sala, os
amigos entram, e qualquer um pode compartilhar a tela ao mesmo tempo.

## Recursos

- **Alta qualidade com pouco custo**: captura e codificação na GPU (NVIDIA NVENC, AMD AMF,
  Intel QuickSync), H.264 ou AV1, até 60 fps, bitrate configurável (12 Mbps por padrão).
  A tela é codificada **uma vez** e enviada a todos.
- **Várias telas ao mesmo tempo**, em grade estilo Discord, com destaque, volume por
  pessoa e tela cheia.
- **Áudio do PC sem o que você não quer**: exclua apps do áudio transmitido (ex.: o
  Discord, para seus amigos não ouvirem a call duas vezes).
- **Amigos e salas**: adicione amigos por código; abra uma sala **pública** (amigos entram
  sem convite) ou **particular** (só convidados).
- **Direto entre os PCs** (WebRTC, P2P): o vídeo não passa por servidor nenhum.
- **Um único `.exe`** que se atualiza sozinho.

## Instalar

1. Baixe o `familiashare.exe` mais recente na página de
   [Releases](https://github.com/vitorbatalha/familiashare/releases).
2. Abra. Na primeira vez o Windows pode mostrar **"O Windows protegeu o computador"**
   (o app ainda não é assinado digitalmente): clique em **Mais informações → Executar assim
   mesmo**. Se o Firewall perguntar, **permita** o acesso à rede.
3. Pronto. O app avisa quando houver versão nova e se atualiza com um clique.

**Requisitos:** Windows 10 (versão 2004 ou mais nova) ou Windows 11, com o
[WebView2](https://go.microsoft.com/fwlink/p/?LinkId=2124703) (já vem no Windows 11).
Para transmitir com aceleração de hardware: GPU NVIDIA, AMD ou Intel com suporte a
codificação de vídeo (praticamente todas dos últimos ~8 anos).

## Usar

- **Amigos**: seu código de amigo fica no topo (botão de copiar ao lado). Cole o código de
  um amigo na aba **Amigos** e clique em **Adicionar**; ele aceita o pedido.
- **Sala**: escolha *Pública* ou *Particular* e **Abrir minha sala**. Amigos em salas
  públicas aparecem com **Entrar**.
- **Transmitir**: dentro da sala, **Compartilhar tela**. O botão de ajustes (ao lado)
  abre as configurações: monitor, resolução, fps, bitrate, codec, encoder e áudio.
- **Assistir**: clique em **Assistir** ao lado de quem está transmitindo. Clique numa tela
  para destacar; passe o mouse no 🔊 para o volume. Teclas: `F` tela cheia, `M` som,
  `I` estatísticas, `Esc` voltar.

## Para desenvolvedores

O app é escrito em **Go** (transmissão, WebRTC, integração com o Windows) com a interface
em HTML/JS rodando no WebView2, e usa o **FFmpeg** embutido para capturar e codificar.

- [CONTRIBUTING.md](CONTRIBUTING.md) — como compilar, testar e contribuir
- [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) — como o app funciona por dentro
- [docs/PROTOCOL.md](docs/PROTOCOL.md) — as mensagens trocadas entre os apps
- [CHANGELOG.md](CHANGELOG.md) — o que mudou em cada versão

```powershell
.\scripts\build.ps1         # gera dist\familiashare.exe
go test ./...               # testes automáticos
```

## Licença

O código do familiashare está sob a licença [MIT](LICENSE). O executável inclui o
[FFmpeg](https://ffmpeg.org) (LGPL v3) e outras bibliotecas de código aberto — veja
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
