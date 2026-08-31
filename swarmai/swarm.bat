@echo off
REM ============================================================
REM  swarm.bat  -  Avvia Swarm AI con un doppio-clic (Windows).
REM
REM  Fa la stessa cosa di swarm.sh / swarm.ps1: capisce DA SOLO
REM  il ruolo di questo PC.
REM    - se c'e' un llama-server locale  -> HOST (serve lo swarm
REM      + pagina web su http://<IP>:8090)
REM    - altrimenti                      -> NODO (usa lo swarm sulla LAN)
REM
REM  Per SOLO usare lo swarm da un altro PC/telefono NON serve nulla:
REM  apri il browser su  http://<IP-DELL-HOST>:8090
REM
REM  Rete che blocca mDNS? avvia da terminale:  swarm.bat <TOKEN>
REM ============================================================
setlocal
cd /d "%~dp0"
powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0swarm.ps1" %*
echo.
echo Swarm fermato. Puoi chiudere questa finestra.
pause
