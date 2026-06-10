; Inno Setup script for Packwright (Windows installer).
;
; Consumed by ISCC.exe in .github/workflows/release.yml. The release job
; passes the version via /DAppVersion=<x.y.z>; the script falls back to
; "0.0.0-dev" for local hand-runs.
;
; This script bundles the Wails-built packwright.exe (which is the same
; binary used for the TUI — `packwright` with no args is the TUI). The
; installer registers Start Menu / Add-Remove-Programs entries and adds
; the install dir to PATH so the TUI is one shell command away.

#ifndef AppVersion
#define AppVersion "0.0.0-dev"
#endif

#define AppName        "Packwright"
#define AppPublisher   "Packwright"
#define AppURL         "https://github.com/bannaarr01/packwright"
#define AppExeName     "packwright.exe"

[Setup]
AppId={{9F2C8E1B-7F3D-4D6A-9F5C-7B5A0D2E4F11}
AppName={#AppName}
AppVersion={#AppVersion}
AppVerName={#AppName} {#AppVersion}
AppPublisher={#AppPublisher}
AppPublisherURL={#AppURL}
AppSupportURL={#AppURL}/issues
AppUpdatesURL={#AppURL}/releases
DefaultDirName={autopf}\{#AppName}
DefaultGroupName={#AppName}
DisableProgramGroupPage=yes
LicenseFile=..\..\LICENSE
PrivilegesRequired=lowest
PrivilegesRequiredOverridesAllowed=dialog
OutputBaseFilename=packwright-{#AppVersion}-windows-amd64-setup
OutputDir=Output
Compression=lzma2
SolidCompression=yes
WizardStyle=modern
ArchitecturesAllowed=x64
ArchitecturesInstallIn64BitMode=x64
ChangesEnvironment=yes
UninstallDisplayIcon={app}\{#AppExeName}
UninstallDisplayName={#AppName} {#AppVersion}
VersionInfoVersion={#AppVersion}
VersionInfoCompany={#AppPublisher}
VersionInfoDescription={#AppName} Installer
VersionInfoProductName={#AppName}

[Languages]
Name: "english"; MessagesFile: "compiler:Default.isl"

[Tasks]
Name: "desktopicon"; Description: "Create a &desktop shortcut"; GroupDescription: "Additional shortcuts:"
Name: "modifypath"; Description: "Add Packwright to the user &PATH (so `packwright` works in any terminal)"; GroupDescription: "Integration:"; Flags: checkedonce

[Files]
; Wails writes the .exe to build/bin/packwright.exe relative to repo root.
; This script lives at build/windows/installer.iss, so build/bin is ..\bin\.
Source: "..\bin\{#AppExeName}"; DestDir: "{app}"; Flags: ignoreversion
Source: "..\..\LICENSE";         DestDir: "{app}"; Flags: ignoreversion
Source: "..\..\NOTICE";          DestDir: "{app}"; Flags: ignoreversion
Source: "..\..\README.md";       DestDir: "{app}"; Flags: ignoreversion

[Icons]
Name: "{group}\{#AppName} (GUI)"; Filename: "{app}\{#AppExeName}"; Parameters: "--gui"
Name: "{group}\{#AppName} (TUI)"; Filename: "{app}\{#AppExeName}"
Name: "{group}\Uninstall {#AppName}"; Filename: "{uninstallexe}"
Name: "{autodesktop}\{#AppName}"; Filename: "{app}\{#AppExeName}"; Parameters: "--gui"; Tasks: desktopicon

[Run]
Filename: "{app}\{#AppExeName}"; Parameters: "--gui"; \
  Description: "Launch {#AppName}"; Flags: nowait postinstall skipifsilent

[Registry]
; Add {app} to the per-user PATH if the "modifypath" task is selected.
Root: HKCU; Subkey: "Environment"; ValueType: expandsz; ValueName: "Path"; \
  ValueData: "{olddata};{app}"; \
  Check: NeedsAddPath(ExpandConstant('{app}')); Tasks: modifypath

[Code]
function NeedsAddPath(Param: string): Boolean;
var
  OrigPath: string;
begin
  if not RegQueryStringValue(HKEY_CURRENT_USER, 'Environment', 'Path', OrigPath) then
  begin
    Result := True;
    exit;
  end;
  Result := Pos(';' + Param + ';', ';' + OrigPath + ';') = 0;
end;
