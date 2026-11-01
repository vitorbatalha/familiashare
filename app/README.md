# familiashare

Compartilhamento de tela minimalista entre amigos. Um único `familiashare.exe` (Windows 10 2004+ / 11).

## Usar

- **Amigos:** cada pessoa tem um código de amigo fixo (tela inicial). Adicione o código de um amigo; ele aceita o pedido.
- **Salas:** *Abrir minha sala* como **pública** (seus amigos entram sem convite) ou **particular** (só quem você convidar).
  Na lista de amigos aparece quem está em sala; *Entrar* nas salas públicas, ou aceite um convite.
- **Transmitir:** dentro da sala, qualquer pessoa pode *Compartilhar minha tela* — várias ao mesmo tempo.
- **Assistir:** clique em *Assistir* ao lado de quem está transmitindo. Teclas: `F` tela cheia, `M` som, `I` estatísticas, `Esc` voltar.

Na primeira execução:
- O Windows SmartScreen pode avisar (o .exe não é assinado): *Mais informações → Executar assim mesmo*.
- O Firewall do Windows pode pedir permissão de rede: permita.

## Como funciona

- Vídeo: captura via Desktop Duplication + encoder da GPU (NVENC / AMF / QuickSync) usando FFmpeg embutido.
  Na **resolução nativa** a imagem nunca sai da GPU (~0,2 núcleo de CPU). Reduzir a resolução usa a CPU (~1,3 núcleo).
- O vídeo é codificado **uma vez** e enviado a todos (P2P via WebRTC). O upload necessário = bitrate × nº de amigos.
- Áudio: captura por processo do Windows. Excluir um app (ex.: Discord) exclui o app e todos os processos filhos dele.
  Excluindo vários apps, cada app permitido é capturado separadamente; os sons do próprio Windows ficam de fora nesse modo.
- Sem servidor próprio: presença, pedidos de amizade, convites e estado das salas passam pelo servidor público gratuito do PeerJS (o dono da sala coordena quem está nela); a mídia vai direto entre os PCs.
- Áudio: o próprio app sempre fica fora da captura (para o som de quem você assiste não voltar como eco).

## Limitações

- Se um amigo estiver em rede muito restritiva (alguns 4G/redes corporativas), a conexão P2P pode falhar; seria preciso um servidor TURN (`iceServers` em `host.go` e `ui/index.html`).
- Se o dono sair, a sala é encerrada para todos.
- Os códigos de amigo não têm senha: alguém que descubra seu código poderia se passar por você enquanto seu app está fechado. Para uso entre amigos, aceitável.
- Quadro-chave a cada 2 s: quem entra ou perde pacotes vê a imagem em até 2 s.
- Um app excluído que é *aberto* durante a transmissão pode vazar ~1 s de áudio até ser detectado.

## Compilar

Requer Go 1.25+ e um `ffmpeg.exe` (build *lgpl* do [BtbN](https://github.com/BtbN/FFmpeg-Builds/releases)):

```powershell
.\build.ps1 -FFmpeg C:\caminho\ffmpeg.exe
```

Diagnóstico (build sem `-H windowsgui`):
- `TELA_HEADLESS=<código>` transmite sem janela e imprime estatísticas.
- `TELA_AUDIOTEST=1` (com `TELA_EXCLUDE=discord,...`) mostra o volume capturado a cada segundo.

Logs e config ficam em `%LOCALAPPDATA%\familiashare` (na primeira execução, amigos e código da época em que o app se chamava "tela" são trazidos de `%LOCALAPPDATA%\tela`).
