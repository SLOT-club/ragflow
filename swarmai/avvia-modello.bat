@echo off
REM ============================================================
REM  avvia-modello.bat  -  accende il modello (llama.cpp) con un
REM  doppio-clic, sulla porta 8080 che swarmai rileva da solo.
REM
REM  Metti questo file nella cartella dove hai "llama-server.exe"
REM  (llama.cpp) e il tuo modello ".gguf" (anche in sottocartelle).
REM ============================================================
setlocal enabledelayedexpansion
cd /d "%~dp0"

echo Cerco llama-server.exe e un modello .gguf in questa cartella...
set "SRV="
for /r %%f in (llama-server.exe) do if exist "%%f" if not defined SRV set "SRV=%%f"
if not defined SRV for /r %%f in (server.exe) do if exist "%%f" if not defined SRV set "SRV=%%f"
set "MODEL="
for /r %%g in (*.gguf) do if not defined MODEL set "MODEL=%%g"

if not defined SRV (
  echo.
  echo NON trovo "llama-server.exe".
  echo Copia questo file nella cartella dove hai llama-server.exe ^(llama.cpp^) e riprova.
  echo.
  pause
  exit /b 1
)
if not defined MODEL (
  echo.
  echo NON trovo nessun modello ".gguf".
  echo Metti il tuo file modello .gguf in questa cartella ^(o in una sottocartella^) e riprova.
  echo.
  pause
  exit /b 1
)

echo.
echo Avvio il modello:
echo    server : "!SRV!"
echo    modello: "!MODEL!"
echo    porta  : 8080
echo.
echo LASCIA QUESTA FINESTRA APERTA: e' il motore del modello.
echo Poi apri swarmai.exe: diventera' HOST e servira' lo swarm.
echo.
"!SRV!" -m "!MODEL!" --port 8080 --host 127.0.0.1
echo.
echo (Il modello si e' fermato.)
pause
