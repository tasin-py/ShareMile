; ShareMile installer (Inno Setup 6). Build: iscc /DAppVersion=1.0.0 installer\ShareMile.iss
#ifndef AppVersion
  #define AppVersion "1.0.0"
#endif
#define AppName "ShareMile"
#define AppExe "ShareMile.exe"
#define AppURL "https://github.com/tasin-py/ShareMile"

[Setup]
AppId={{6B1F4C1E-2E7A-4B8E-9C61-5A3D2F7E8A11}
AppName={#AppName}
AppVersion={#AppVersion}
AppVerName={#AppName} {#AppVersion}
AppPublisher=Tasin
AppPublisherURL={#AppURL}
AppSupportURL={#AppURL}/issues
AppUpdatesURL={#AppURL}/releases
DefaultDirName={autopf}\{#AppName}
DefaultGroupName={#AppName}
DisableDirPage=no
DisableProgramGroupPage=yes
DisableWelcomePage=no
AllowNoIcons=yes
LicenseFile=..\LICENSE
PrivilegesRequired=lowest
PrivilegesRequiredOverridesAllowed=dialog commandline
OutputDir=..\dist
OutputBaseFilename=ShareMile-Setup
SetupIconFile=..\assets\icon.ico
UninstallDisplayIcon={app}\{#AppExe}
UninstallDisplayName={#AppName}
WizardStyle=modern dark
WizardSizePercent=110
Compression=lzma2/ultra64
SolidCompression=yes
CloseApplications=yes
RestartApplications=no
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
MinVersion=10.0
VersionInfoVersion={#AppVersion}
VersionInfoProductName={#AppName}
VersionInfoDescription={#AppName} Setup

[Files]
Source: "..\build\{#AppExe}"; DestDir: "{app}"; Flags: ignoreversion
Source: "..\LICENSE"; DestDir: "{app}"; DestName: "LICENSE.txt"; Flags: ignoreversion

[Icons]
Name: "{group}\{#AppName}"; Filename: "{app}\{#AppExe}"; Comment: "Fast direct file sharing"
Name: "{group}\Uninstall {#AppName}"; Filename: "{uninstallexe}"

[Run]
; Finish page. Every box is ticked by default and performs a real action.
Filename: "{app}\{#AppExe}"; Parameters: "--pin-taskbar"; Description: "Pin {#AppName} to taskbar"; Flags: postinstall skipifsilent runhidden waituntilterminated
Filename: "{app}\{#AppExe}"; Parameters: "--pin-start"; Description: "Pin {#AppName} to Start"; Flags: postinstall skipifsilent runhidden waituntilterminated
Filename: "{app}\{#AppExe}"; Parameters: "--desktop-shortcut"; Description: "Add {#AppName} to Desktop"; Flags: postinstall skipifsilent runhidden waituntilterminated
Filename: "{sys}\netsh.exe"; Parameters: "advfirewall firewall add rule name=""ShareMile"" dir=in action=allow program=""{app}\{#AppExe}"" enable=yes profile=any"; Description: "Allow {#AppName} through Windows Firewall (needed for internet sharing)"; Verb: runas; Flags: postinstall skipifsilent shellexec runhidden waituntilterminated; Check: not FirewallRuleExists
Filename: "{#AppURL}"; Description: "Open Project's GitHub Page"; Flags: postinstall skipifsilent shellexec nowait
Filename: "{app}\{#AppExe}"; Description: "Launch {#AppName}"; Flags: postinstall skipifsilent nowait
; Silent self-update: relaunch afterwards.
Filename: "{app}\{#AppExe}"; Parameters: "--updated"; Flags: nowait; Check: WantRelaunch

[UninstallRun]
Filename: "{sys}\taskkill.exe"; Parameters: "/F /IM {#AppExe}"; Flags: runhidden; RunOnceId: "KillApp"

[UninstallDelete]
Type: files; Name: "{userdesktop}\{#AppName}.lnk"
Type: files; Name: "{userappdata}\Microsoft\Internet Explorer\Quick Launch\User Pinned\TaskBar\{#AppName}.lnk"
Type: files; Name: "{app}\{#AppExe}.old"

[Code]
function WantRelaunch: Boolean;
begin
  Result := WizardSilent and (ExpandConstant('{param:RELAUNCH|0}') = '1');
end;

function FirewallRuleExists: Boolean;
var
  Code: Integer;
begin
  Result := Exec(ExpandConstant('{sys}\netsh.exe'), 'advfirewall firewall show rule name=ShareMile', '', SW_HIDE, ewWaitUntilTerminated, Code) and (Code = 0);
end;

procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);
var
  Code: Integer;
begin
  if CurUninstallStep = usPostUninstall then
  begin
    // Remove the firewall rule (asks for admin once; harmless if it was never added).
    if FirewallRuleExists then
      ShellExec('runas', ExpandConstant('{sys}\netsh.exe'), 'advfirewall firewall delete rule name=ShareMile', '', SW_HIDE, ewWaitUntilTerminated, Code);
    if SuppressibleMsgBox('Also remove ShareMile settings and saved network codes?', mbConfirmation, MB_YESNO or MB_DEFBUTTON2, IDNO) = IDYES then
      DelTree(ExpandConstant('{userappdata}\ShareMile'), True, True, True);
  end;
end;
