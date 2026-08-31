@echo off
REM ============================================================
REM  metti-sul-desktop.bat  -  doppio-clic UNA volta.
REM  Crea sul tuo desktop un collegamento "Swarm AI" che avvia
REM  swarm.bat da questa cartella (i percorsi restano corretti).
REM ============================================================
setlocal
set "TARGET=%~dp0swarm.bat"
set "WORKDIR=%~dp0"
powershell -NoProfile -ExecutionPolicy Bypass -Command "$d=[Environment]::GetFolderPath('Desktop'); $lnk=Join-Path $d 'Swarm AI.lnk'; $s=(New-Object -ComObject WScript.Shell).CreateShortcut($lnk); $s.TargetPath='%TARGET%'; $s.WorkingDirectory='%WORKDIR%'; $s.IconLocation='%SystemRoot%\System32\shell32.dll,18'; $s.Save(); Write-Host ('Collegamento creato sul desktop: ' + $lnk)"
echo.
echo Fatto. Trovi l'icona "Swarm AI" sul desktop: doppio-clic per avviare.
pause
