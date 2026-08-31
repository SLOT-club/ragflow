@echo off
REM ============================================================
REM  avvia-modello.bat  -  trova da solo llama-server e il modello
REM  su TUTTI i dischi del PC (C:, D:, E:...) e li avvia sulla
REM  porta 8080, quella che swarmai rileva in automatico.
REM  Puoi metterlo dove vuoi (anche sul desktop).
REM ============================================================
setlocal enabledelayedexpansion
cd /d "%~dp0"

echo Cerco "llama-server" e un modello ".gguf" su TUTTI i dischi (C:, D:, E:...).
echo Puo' volerci qualche minuto la prima volta: attendi...
echo.

REM --- 1) llama-server.exe: prima accanto a questo file e nella cartella utente
REM        (veloce), poi su ogni disco esistente. ---
set "SRV="
for /r "%~dp0" %%f in (llama-server.exe) do if exist "%%f" if not defined SRV set "SRV=%%f"
if not defined SRV for /f "delims=" %%p in ('where /r "%USERPROFILE%" llama-server.exe 2^>nul') do if not defined SRV set "SRV=%%p"
if not defined SRV for /f "delims=" %%p in ('where /r "%USERPROFILE%" server.exe 2^>nul') do if not defined SRV set "SRV=%%p"
for %%D in (C D E F G H I J K L M N O P Q R S T U V W X Y Z) do if exist %%D:\ (
  if not defined SRV for /f "delims=" %%p in ('where /r %%D:\ llama-server.exe 2^>nul') do if not defined SRV set "SRV=%%p"
  if not defined SRV for /f "delims=" %%p in ('where /r %%D:\ server.exe 2^>nul') do if not defined SRV set "SRV=%%p"
)

if not defined SRV (
  echo Non ho trovato "llama-server.exe" su nessun disco.
  echo Forse llama.cpp non e' installato su questo PC.
  echo Scrivimi: ti do l'alternativa piu' semplice per avere un modello acceso.
  echo.
  pause
  exit /b 1
)

REM --- 2) modello .gguf: prima vicino al server, poi utente, poi ogni disco. ---
for %%d in ("%SRV%") do set "SRVDIR=%%~dpd"
set "MODEL="
for /r "%SRVDIR%" %%g in (*.gguf) do if not defined MODEL set "MODEL=%%g"
if not defined MODEL for /f "delims=" %%p in ('where /r "%USERPROFILE%" *.gguf 2^>nul') do if not defined MODEL set "MODEL=%%p"
if not defined MODEL for %%D in (C D E F G H I J K L M N O P Q R S T U V W X Y Z) do if exist %%D:\ (
  if not defined MODEL for /f "delims=" %%p in ('where /r %%D:\ *.gguf 2^>nul') do if not defined MODEL set "MODEL=%%p"
)

if not defined MODEL (
  echo Ho trovato llama-server qui:
  echo     "%SRV%"
  echo ...ma nessun file modello ".gguf" sul PC.
  echo Dimmi se hai gia' scaricato un modello, oppure ti indico quale prendere.
  echo.
  pause
  exit /b 1
)

echo Trovato tutto:
echo     server : "%SRV%"
echo     modello: "%MODEL%"
echo     porta  : 8080
echo.
echo LASCIA QUESTA FINESTRA APERTA: e' il motore del modello acceso.
echo Poi apri swarmai.exe ^-^-^> diventera' HOST e servira' lo swarm.
echo.
"%SRV%" -m "%MODEL%" --port 8080 --host 127.0.0.1
echo.
echo (Il modello si e' fermato. Puoi chiudere.)
pause
