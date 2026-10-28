@echo off
rem Switch the console to UTF-8 so any non-ASCII text in this file is not garbled in GBK terminals.
chcp 65001 >nul 2>&1
rem ARTEX supervisor startup script (Windows)
rem
rem Usage:
rem   start.bat                  run in the foreground (Ctrl-C to stop)
rem   start.bat -addr :9000      extra arguments are passed through to artex unchanged
rem
rem It does one thing only: run artex.exe, and after the process exits decide from the exit code whether to relaunch it.
rem
rem   0      stopped normally by the user  -> leave the loop
rem   75     restart requested by artex    -> rerun immediately (the UI's "one-click update" or "rollback" was used)
rem   other  crash                         -> rerun after a backoff (1->2->4... up to 60 seconds)
rem
rem Downloading, SHA256 verification and swapping are not done here; artex does all of it itself at startup
rem (the selfupdate package). The script stays dumb; see the notes at the top of start.sh.

setlocal enabledelayedexpansion
cd /d "%~dp0"

set "BIN=artex.exe"
if not exist "%BIN%" (
	echo [artex] executable not found: %BIN% 1>&2
	exit /b 1
)

set "RESTART_CODE=75"
set "MAX_DELAY=60"
set /a delay=1

:loop
"%BIN%" %*
set "code=!ERRORLEVEL!"

if "!code!"=="0" (
	echo [artex] exited normally
	exit /b 0
)

if "!code!"=="%RESTART_CODE%" (
	rem Update/rollback is ready: after the rerun artex completes the swap at startup.
	echo [artex] restart requested, applying new version...
	set /a delay=1
	goto loop
)

echo [artex] exited abnormally ^(code=!code!^), restarting in !delay!s 1>&2
rem timeout fails in a redirected console, so ping is the fallback (a delay of N seconds needs N+1 pings).
set /a pings=!delay!+1
ping -n !pings! 127.0.0.1 >nul 2>&1
set /a delay=!delay!*2
if !delay! gtr %MAX_DELAY% set /a delay=%MAX_DELAY%
goto loop
