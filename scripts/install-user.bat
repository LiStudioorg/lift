@echo off
setlocal EnableExtensions
chcp 65001 >nul

set "REPO=LiStudioorg/lift"
set "BIN_NAME=lift.exe"
set "API_URL=https://api.github.com/repos/%REPO%/releases/latest"
set "DOWNLOAD_BASE=https://github.com/%REPO%/releases/download"
set "FORCE=0"
set "TMP_DIR="
set "EXTRACTED="
set "EXTRACTED_FOUND="
set "ERROR_MSG="

if /I "%~1"=="-h" goto :usage
if /I "%~1"=="--help" goto :usage

:parse_args
if "%~1"=="" goto :args_done
if /I "%~1"=="-u" (
  set "FORCE=1"
  shift
  goto :parse_args
)
if /I "%~1"=="--upgrade" (
  set "FORCE=1"
  shift
  goto :parse_args
)
if /I "%~1"=="-h" goto :usage
if /I "%~1"=="--help" goto :usage
echo [错误] 未知参数: %~1
goto :usage_error

:args_done
if not defined LOCALAPPDATA (
  echo [错误] 未找到 LOCALAPPDATA 环境变量。
  exit /b 1
)
set "INSTALL_DIR=%LOCALAPPDATA%\lift"

where powershell.exe >nul 2>&1
if errorlevel 1 (
  echo [错误] 未找到 powershell.exe，无法获取版本信息和解压。
  exit /b 1
)

where curl.exe >nul 2>&1
if errorlevel 1 (
  echo [错误] 未找到 curl.exe，请使用 Windows 10 或更高版本。
  exit /b 1
)

set "LATEST="
for /f "usebackq delims=" %%V in (`powershell.exe -NoProfile -ExecutionPolicy Bypass -Command "$ProgressPreference='SilentlyContinue'; [Net.ServicePointManager]::SecurityProtocol=[Net.SecurityProtocolType]::Tls12; (Invoke-RestMethod -Uri '%API_URL%' -Headers @{'User-Agent'='lift-installer'}).tag_name"`) do set "LATEST=%%V"
if not defined LATEST (
  echo [错误] 无法获取 GitHub 最新版本。
  echo 请检查网络连接，或稍后重试。
  exit /b 1
)

set "ARCH=%PROCESSOR_ARCHITECTURE%"
if defined PROCESSOR_ARCHITEW6432 set "ARCH=%PROCESSOR_ARCHITEW6432%"
set "GOARCH="
if /I "%ARCH%"=="AMD64" set "GOARCH=amd64"
if /I "%ARCH%"=="ARM64" set "GOARCH=arm64"
if /I "%ARCH%"=="x86" set "GOARCH=386"
if not defined GOARCH (
  echo [错误] 不支持的处理器架构: %ARCH%
  exit /b 1
)

set "VERSION=%LATEST%"
if /I "%LATEST:~0,1%"=="v" set "VERSION=%LATEST:~1%"
set "ASSET=lift_%VERSION%_windows_%GOARCH%.zip"
set "DOWNLOAD_URL=%DOWNLOAD_BASE%/%LATEST%/%ASSET%"

set "CURRENT="
set "VER_FILE=%TEMP%\lift-version-%RANDOM%%RANDOM%.txt"
if exist "%INSTALL_DIR%\%BIN_NAME%" (
  "%INSTALL_DIR%\%BIN_NAME%" -v >"%VER_FILE%" 2>nul
  if exist "%VER_FILE%" (
    for /f "usebackq tokens=2" %%C in ("%VER_FILE%") do (
      if not defined CURRENT set "CURRENT=%%C"
    )
  )
  del /F /Q "%VER_FILE%" >nul 2>&1
)

if /I "%CURRENT%"=="%LATEST%" if "%FORCE%"=="0" (
  echo 当前已是最新版本 %LATEST%，无需安装。
  echo 如需强制重新安装，请使用 -u 或 --upgrade。
  exit /b 0
)

if defined CURRENT (
  echo 当前版本: %CURRENT%
  echo 准备升级到: %LATEST%
) else (
  echo 未检测到已安装版本，准备安装: %LATEST%
)

set "TMP_DIR=%TEMP%\lift-install-%RANDOM%%RANDOM%"
mkdir "%TMP_DIR%" >nul 2>&1
if errorlevel 1 (
  set "ERROR_MSG=无法创建临时目录: %TMP_DIR%"
  goto :fail
)

set "ZIP=%TMP_DIR%\%ASSET%"
set "EXTRACT=%TMP_DIR%\extract"
mkdir "%EXTRACT%" >nul 2>&1
if errorlevel 1 (
  set "ERROR_MSG=无法创建解压目录: %EXTRACT%"
  goto :fail
)

echo 正在下载 %ASSET% ...
curl.exe -fL -sS --retry 3 --retry-delay 2 --connect-timeout 15 -o "%ZIP%" "%DOWNLOAD_URL%"
if errorlevel 1 (
  set "ERROR_MSG=下载失败: %DOWNLOAD_URL%"
  goto :fail
)

tar.exe -xf "%ZIP%" -C "%EXTRACT%" >nul 2>&1
if errorlevel 1 (
  powershell.exe -NoProfile -ExecutionPolicy Bypass -Command "Expand-Archive -LiteralPath '%ZIP%' -DestinationPath '%EXTRACT%' -Force"
  if errorlevel 1 (
    set "ERROR_MSG=解压失败: %ASSET%"
    goto :fail
  )
)

set "EXTRACTED=%EXTRACT%\%BIN_NAME%"
if not exist "%EXTRACTED%" (
  for /r "%EXTRACT%" %%F in (%BIN_NAME%) do (
    if not defined EXTRACTED_FOUND set "EXTRACTED_FOUND=%%F"
  )
)
if defined EXTRACTED_FOUND set "EXTRACTED=%EXTRACTED_FOUND%"
if not exist "%EXTRACTED%" (
  set "ERROR_MSG=归档中未找到 %BIN_NAME%"
  goto :fail
)

if not exist "%INSTALL_DIR%" mkdir "%INSTALL_DIR%" >nul 2>&1
if errorlevel 1 (
  set "ERROR_MSG=无法创建安装目录: %INSTALL_DIR%"
  goto :fail
)

if exist "%INSTALL_DIR%\%BIN_NAME%.old" del /F /Q "%INSTALL_DIR%\%BIN_NAME%.old" >nul 2>&1
if exist "%INSTALL_DIR%\%BIN_NAME%" (
  move /Y "%INSTALL_DIR%\%BIN_NAME%" "%INSTALL_DIR%\%BIN_NAME%.old" >nul 2>&1
  if errorlevel 1 (
    set "ERROR_MSG=无法替换正在运行的 lift.exe，请关闭所有 lift 进程后重试。"
    goto :fail
  )
)

copy /Y /B "%EXTRACTED%" "%INSTALL_DIR%\%BIN_NAME%" >nul
if errorlevel 1 (
  del /F /Q "%INSTALL_DIR%\%BIN_NAME%" >nul 2>&1
  if exist "%INSTALL_DIR%\%BIN_NAME%.old" move /Y "%INSTALL_DIR%\%BIN_NAME%.old" "%INSTALL_DIR%\%BIN_NAME%" >nul 2>&1
  set "ERROR_MSG=复制 lift.exe 失败: %INSTALL_DIR%"
  goto :fail
)

"%INSTALL_DIR%\%BIN_NAME%" -v >nul 2>&1
if errorlevel 1 (
  del /F /Q "%INSTALL_DIR%\%BIN_NAME%" >nul 2>&1
  if exist "%INSTALL_DIR%\%BIN_NAME%.old" move /Y "%INSTALL_DIR%\%BIN_NAME%.old" "%INSTALL_DIR%\%BIN_NAME%" >nul 2>&1
  set "ERROR_MSG=安装后无法运行 lift.exe，已恢复旧版本。"
  goto :fail
)
if exist "%INSTALL_DIR%\%BIN_NAME%.old" del /F /Q "%INSTALL_DIR%\%BIN_NAME%.old" >nul 2>&1

set "USER_PATH="
for /f "usebackq delims=" %%P in (`powershell.exe -NoProfile -ExecutionPolicy Bypass -Command "[Environment]::GetEnvironmentVariable('Path','User')"`) do set "USER_PATH=%%P"
if defined USER_PATH (
  echo ;%USER_PATH%; | findstr /I /C:";%INSTALL_DIR%;" >nul
  if errorlevel 1 (
    setx PATH "%USER_PATH%;%INSTALL_DIR%" >nul
    if errorlevel 1 (
      set "ERROR_MSG=无法写入用户 PATH，请手动将 %INSTALL_DIR% 加入 PATH。"
      goto :fail
    )
    echo 已将 %INSTALL_DIR% 写入用户 PATH。
  ) else (
    echo %INSTALL_DIR% 已在用户 PATH 中。
  )
) else (
  setx PATH "%INSTALL_DIR%" >nul
  if errorlevel 1 (
    set "ERROR_MSG=无法写入用户 PATH，请手动将 %INSTALL_DIR% 加入 PATH。"
    goto :fail
  )
  echo 已将 %INSTALL_DIR% 写入用户 PATH。
)

rmdir /s /q "%TMP_DIR%" >nul 2>&1
echo.
echo 已安装 lift: %INSTALL_DIR%\%BIN_NAME%
echo 请打开新的终端，使 PATH 变更生效。
exit /b 0

:fail
echo [错误] %ERROR_MSG%
if defined TMP_DIR if exist "%TMP_DIR%" rmdir /s /q "%TMP_DIR%" >nul 2>&1
exit /b 1

:usage
echo 用法: install-user.bat [-u ^| --upgrade]
echo.
echo 选项:
echo   -u, --upgrade   即使已是最新版本也重新下载并覆盖安装
echo   -h, --help      显示帮助
echo.
echo 安装位置: %LOCALAPPDATA%\lift
exit /b 0

:usage_error
echo.
call :usage
exit /b 2
