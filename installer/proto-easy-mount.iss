; Inno Setup script for Proto Easy Mount. Build with scripts\build-windows.ps1,
; which passes the defines below and prepares dist\ and build\.

#ifndef AppVersion
  #define AppVersion "0.0.0-dev"
#endif
#ifndef AppVersionNumeric
  #define AppVersionNumeric "0.0.0.0"
#endif
#ifndef WinFspMsi
  #error WinFspMsi must point at the WinFsp installer
#endif
#ifndef WizardSmallImages
  #define WizardSmallImages "..\build\wizard-small-55.bmp"
#endif

#define AppName "Proto Easy Mount"
#define AppExe "proto-easy-mount-app.exe"
#define RunValue "ProtoEasyMount"
#define AppUserModelID "Protomothis.ProtoEasyMount"

[Setup]
AppId={{6F0B3C7E-2D4A-4E8B-9C51-7A2E9D4B1F30}
AppName={#AppName}
AppVersion={#AppVersion}
AppVerName={#AppName} {#AppVersion}
AppPublisher=Protomothis
VersionInfoVersion={#AppVersionNumeric}
VersionInfoProductVersion={#AppVersionNumeric}
; Installs for the current user by default: no admin rights needed, also for
; updates; only the WinFsp driver asks for elevation, and only when missing.
; The first page offers installing for all users instead (Program Files,
; needs admin, and so do its updates). Updates keep the mode chosen first
; (UsePreviousPrivileges); /ALLUSERS or /CURRENTUSER pick it on the command line.
PrivilegesRequired=lowest
PrivilegesRequiredOverridesAllowed=dialog commandline
DefaultDirName={autopf}\{#AppName}
DisableProgramGroupPage=yes
DisableDirPage=auto
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
MinVersion=10.0
OutputDir=..\dist
OutputBaseFilename=ProtoEasyMount-Setup-{#AppVersion}
SetupIconFile=..\build\app.ico
WizardSmallImageFile={#WizardSmallImages}
UninstallDisplayIcon={app}\{#AppExe}
UninstallDisplayName={#AppName}
WizardStyle=modern
Compression=lzma2/ultra64
SolidCompression=yes
; Close a running copy (it holds the files and the mounts) before replacing it.
CloseApplications=force
RestartApplications=no
ShowLanguageDialog=auto

[Languages]
Name: "korean"; MessagesFile: "compiler:Languages\Korean.isl"
Name: "english"; MessagesFile: "compiler:Default.isl"

[CustomMessages]
korean.TaskDesktop=바탕화면에 바로 가기 만들기
korean.TaskAutostart=컴퓨터를 켜면 자동 실행 (트레이에서 조용히 시작)
korean.TaskGroup=추가 작업:
korean.WinFspStatus=WinFsp 드라이버를 설치하는 중입니다…
korean.WinFspAsk=드라이브 연결에 필요한 WinFsp 드라이버를 설치합니다.%n%n다음에 뜨는 관리자 권한 확인 창에서 [예]를 눌러 주세요.
korean.WinFspFailed=WinFsp 드라이버가 설치되지 않았습니다.%n%n앱은 설치되지만 드라이브를 연결하려면 WinFsp가 필요합니다. 나중에 https://winfsp.dev/rel/ 에서 설치할 수 있습니다.
korean.LaunchApp=Proto Easy Mount 실행
english.TaskDesktop=Create a desktop shortcut
english.TaskAutostart=Start when I sign in to Windows (quietly, in the tray)
english.TaskGroup=Additional tasks:
english.WinFspStatus=Installing the WinFsp driver…
english.WinFspAsk=Mounting drives needs the WinFsp driver, which will be installed now.%n%nPlease choose Yes on the administrator prompt that follows.
english.WinFspFailed=The WinFsp driver was not installed.%n%nThe app is installed, but mounting drives needs WinFsp. You can install it later from https://winfsp.dev/rel/.
english.LaunchApp=Launch Proto Easy Mount

[Tasks]
Name: "desktopicon"; Description: "{cm:TaskDesktop}"; GroupDescription: "{cm:TaskGroup}"; Flags: unchecked
; Per user: for an all-users install each person turns it on in the app.
Name: "autostart"; Description: "{cm:TaskAutostart}"; GroupDescription: "{cm:TaskGroup}"; Check: not IsAdminInstallMode

[Files]
Source: "..\dist\{#AppExe}"; DestDir: "{app}"; Flags: ignoreversion
Source: "..\dist\proto-easy-mount.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "..\build\THIRD-PARTY-NOTICES.txt"; DestDir: "{app}"; Flags: ignoreversion
Source: "..\LICENSE"; DestDir: "{app}"; DestName: "LICENSE.txt"; Flags: ignoreversion
Source: "{#WinFspMsi}"; DestName: "winfsp.msi"; Flags: dontcopy

[Icons]
; The ID lets Windows show the app's notifications (and group its taskbar button).
Name: "{autoprograms}\{#AppName}"; Filename: "{app}\{#AppExe}"; AppUserModelID: "{#AppUserModelID}"
Name: "{autodesktop}\{#AppName}"; Filename: "{app}\{#AppExe}"; Tasks: desktopicon

[Registry]
; Same value the app writes from its own "start at login" switch.
Root: HKCU; Subkey: "Software\Microsoft\Windows\CurrentVersion\Run"; ValueType: string; ValueName: "{#RunValue}"; ValueData: """{app}\{#AppExe}"" --background"; Tasks: autostart

[Run]
Filename: "{app}\{#AppExe}"; Description: "{cm:LaunchApp}"; Flags: nowait postinstall skipifsilent
; The app's own updater runs setup quietly with /RELAUNCH=1 after quitting.
Filename: "{app}\{#AppExe}"; Flags: nowait runasoriginaluser; Check: Relaunch

[UninstallRun]
; The app keeps running in the tray; stop it so its files can be removed.
Filename: "{sys}\taskkill.exe"; Parameters: "/IM {#AppExe} /F"; Flags: runhidden; RunOnceId: "StopApp"

[Code]
function Relaunch: Boolean;
begin
  Result := WizardSilent and (ExpandConstant('{param:RELAUNCH|0}') = '1');
end;

function WinFspInstalled: Boolean;
var
  Dir: String;
begin
  Result := RegQueryStringValue(HKLM32, 'SOFTWARE\WinFsp', 'InstallDir', Dir) and
            FileExists(AddBackslash(Dir) + 'bin\winfsp-x64.dll');
end;

procedure InstallWinFsp;
var
  ResultCode: Integer;
begin
  if WinFspInstalled then
    Exit;
  if not WizardSilent then
    MsgBox(CustomMessage('WinFspAsk'), mbInformation, MB_OK);
  WizardForm.StatusLabel.Caption := CustomMessage('WinFspStatus');
  ExtractTemporaryFile('winfsp.msi');
  // The installer itself runs without admin rights; elevate just msiexec.
  ShellExec('runas', 'msiexec.exe',
    '/i "' + ExpandConstant('{tmp}\winfsp.msi') + '" /passive /norestart',
    '', SW_SHOW, ewWaitUntilTerminated, ResultCode);
  if not WinFspInstalled then
    SuppressibleMsgBox(CustomMessage('WinFspFailed'), mbError, MB_OK, IDOK);
end;

procedure CurStepChanged(CurStep: TSetupStep);
begin
  if CurStep = ssPostInstall then
    InstallWinFsp;
end;

procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);
begin
  // The app may have added this itself; WinFsp is shared and stays installed.
  if CurUninstallStep = usUninstall then
    RegDeleteValue(HKCU, 'Software\Microsoft\Windows\CurrentVersion\Run', '{#RunValue}');
end;
