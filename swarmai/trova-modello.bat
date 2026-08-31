@echo off
REM ============================================================
REM  trova-modello.bat  -  scopre su quale porta risponde il tuo
REM  modello (qualsiasi app: LM Studio, Jan, GPT4All, Ollama,
REM  llama.cpp...). Doppio-clic, leggi il risultato.
REM ============================================================
setlocal enabledelayedexpansion
echo Cerco un modello acceso sulle porte locali piu' comuni...
echo (se non trovi nulla, apri la tua app e attiva il "Local Server / API".)
echo.

set "FOUND="
for %%P in (8080 8081 1234 11434 1337 4891 8000 5000 5001 3000 8888) do (
  curl -s -m 1 "http://127.0.0.1:%%P/v1/models" 2>nul | findstr /i "\"data\" \"object\" \"id\"" >nul 2>&1
  if !errorlevel! == 0 (
    echo    TROVATO un modello su:  http://127.0.0.1:%%P
    if not defined FOUND set "FOUND=%%P"
  )
)

echo.
if not defined FOUND (
  echo Nessun modello acceso trovato sulle porte comuni.
  echo Nella tua app del modello ^(Hermes^) cerca un'impostazione tipo:
  echo    "Local Server"  /  "API Server"  /  "Developer"  /  "OpenAI compatible"
  echo ATTIVALA, lascia l'app aperta, poi rilancia questo file.
) else (
  echo Ottimo: il modello risponde sulla porta !FOUND!.
  echo Ora apri swarmai.exe: dovrebbe trovarlo da solo e diventare HOST.
  echo Se non lo trova, scrivimi il numero !FOUND! e lo aggancio io.
)
echo.
pause
